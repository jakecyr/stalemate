// Package store persists the ledger and durable jobs in SQLite or PostgreSQL.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jakecyr/stalemate/internal/domain"
	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("record not found")
var ErrBudget = errors.New("AI budget exhausted")

type Store struct {
	DB     *sql.DB
	driver string
}
type Tx struct {
	tx *sql.Tx
	s  *Store
}

func Open(ctx context.Context, driver, dsn string) (*Store, error) {
	if driver == "sqlite" && dsn != ":memory:" && !strings.HasPrefix(dsn, "file:") {
		if err := os.MkdirAll(filepath.Dir(dsn), 0700); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(dsn, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, err
		}
		f.Close()
		if err = os.Chmod(dsn, 0600); err != nil {
			return nil, err
		}
	}
	dbDriver := driver
	if driver == "postgres" {
		dbDriver = "pgx"
	}
	db, err := sql.Open(dbDriver, dsn)
	if err != nil {
		return nil, err
	}
	s := &Store{DB: db, driver: driver}
	db.SetMaxOpenConns(1)
	if err = db.PingContext(ctx); err != nil {
		db.Close()
		return nil, errors.New("database connection failed (check driver and DSN)")
	}
	if driver == "sqlite" {
		for _, p := range []string{"PRAGMA journal_mode=WAL", "PRAGMA busy_timeout=5000", "PRAGMA foreign_keys=ON"} {
			if _, err = db.ExecContext(ctx, p); err != nil {
				db.Close()
				return nil, err
			}
		}
	}
	if err = s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) Close() error { return s.DB.Close() }
func (s *Store) Bind(q string) string {
	if s.driver != "postgres" {
		return q
	}
	n := 0
	var b strings.Builder
	for _, r := range q {
		if r == '?' {
			n++
			fmt.Fprintf(&b, "$%d", n)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
func (s *Store) Exec(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return s.DB.ExecContext(ctx, s.Bind(q), args...)
}
func (s *Store) Row(ctx context.Context, q string, args ...any) *sql.Row {
	return s.DB.QueryRowContext(ctx, s.Bind(q), args...)
}
func (s *Store) Transaction(ctx context.Context, fn func(*Tx) error) error {
	t, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer t.Rollback()
	if err = fn(&Tx{tx: t, s: s}); err != nil {
		return err
	}
	return t.Commit()
}
func (t *Tx) Exec(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return t.tx.ExecContext(ctx, t.s.Bind(q), args...)
}
func (t *Tx) Row(ctx context.Context, q string, args ...any) *sql.Row {
	return t.tx.QueryRowContext(ctx, t.s.Bind(q), args...)
}
func (s *Store) migrate(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied BIGINT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS projects (id TEXT PRIMARY KEY,name TEXT NOT NULL,path TEXT NOT NULL UNIQUE,module TEXT NOT NULL,cloud_allowed INTEGER NOT NULL,body TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS snapshots (project TEXT PRIMARY KEY,body TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS sessions (id TEXT PRIMARY KEY,project TEXT NOT NULL,path TEXT NOT NULL,cursor BIGINT NOT NULL,body TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS artifacts (id TEXT PRIMARY KEY,project TEXT NOT NULL,session TEXT NOT NULL,body TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS entities (id TEXT PRIMARY KEY,project TEXT NOT NULL,kind TEXT NOT NULL,body TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS relationships (id TEXT PRIMARY KEY,from_id TEXT NOT NULL,to_id TEXT NOT NULL,type TEXT NOT NULL,source TEXT NOT NULL,valid_to BIGINT NOT NULL,body TEXT NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS relationships_reverse ON relationships(to_id,valid_to,type)`,
		`CREATE INDEX IF NOT EXISTS relationships_forward ON relationships(from_id,valid_to)`,
		`CREATE TABLE IF NOT EXISTS claims (id TEXT PRIMARY KEY,project TEXT NOT NULL,entity TEXT NOT NULL,claim_key TEXT NOT NULL,status TEXT NOT NULL,created BIGINT NOT NULL,body TEXT NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS claims_current ON claims(project,entity,claim_key,status)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS claims_one_active ON claims(project,claim_key) WHERE status='active'`,
		`CREATE TABLE IF NOT EXISTS memories (id TEXT PRIMARY KEY,project TEXT NOT NULL,session TEXT NOT NULL,model TEXT NOT NULL,created BIGINT NOT NULL,body TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS impacts (id TEXT PRIMARY KEY,changed TEXT NOT NULL,affected TEXT NOT NULL,status TEXT NOT NULL,created BIGINT NOT NULL,body TEXT NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS impacts_affected ON impacts(affected,status,created)`,
		`CREATE TABLE IF NOT EXISTS jobs (id TEXT PRIMARY KEY,kind TEXT NOT NULL,payload TEXT NOT NULL,status TEXT NOT NULL,attempts INTEGER NOT NULL,available BIGINT NOT NULL,lease BIGINT NOT NULL,owner TEXT NOT NULL,error TEXT NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS jobs_ready ON jobs(status,available,lease)`,
		`CREATE TABLE IF NOT EXISTS usage_ledger (id TEXT PRIMARY KEY,operation TEXT NOT NULL,model TEXT NOT NULL,input_tokens BIGINT NOT NULL,output_tokens BIGINT NOT NULL,reserved DOUBLE PRECISION NOT NULL,cost DOUBLE PRECISION NOT NULL,status TEXT NOT NULL,created BIGINT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS cache (id TEXT PRIMARY KEY,body TEXT NOT NULL,created BIGINT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS named_leases (name TEXT PRIMARY KEY,owner TEXT NOT NULL,expires BIGINT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS automation_runs (id TEXT PRIMARY KEY,impact TEXT NOT NULL,project TEXT NOT NULL,status TEXT NOT NULL,created BIGINT NOT NULL,body TEXT NOT NULL)`,
	}
	return s.Transaction(ctx, func(t *Tx) error {
		for _, q := range stmts {
			if _, err := t.Exec(ctx, q); err != nil {
				return fmt.Errorf("migration: %w", err)
			}
		}
		var v int
		if err := t.Row(ctx, `SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&v); err != nil {
			return err
		}
		if v > 1 {
			return errors.New("database schema is newer than this binary")
		}
		_, err := t.Exec(ctx, `INSERT INTO schema_migrations(version,applied) VALUES(1,?) ON CONFLICT(version) DO NOTHING`, domain.Now())
		return err
	})
}
func decodeRow[T any](r *sql.Row) (T, error) {
	var v T
	var b string
	err := r.Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	if err != nil {
		return v, err
	}
	err = json.Unmarshal([]byte(b), &v)
	return v, err
}
func list[T any](ctx context.Context, s *Store, q string, args ...any) ([]T, error) {
	r, err := s.DB.QueryContext(ctx, s.Bind(q), args...)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	out := []T{}
	for r.Next() {
		var b string
		var v T
		if err = r.Scan(&b); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(b), &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, r.Err()
}
func (s *Store) PutProject(ctx context.Context, p domain.Project) error {
	_, err := s.Exec(ctx, `INSERT INTO projects(id,name,path,module,cloud_allowed,body) VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,module=excluded.module,cloud_allowed=excluded.cloud_allowed,body=excluded.body`, p.ID, p.Name, p.Path, p.Module, boolInt(p.CloudAllowed), domain.JSON(p))
	return err
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func (s *Store) Projects(ctx context.Context) ([]domain.Project, error) {
	return list[domain.Project](ctx, s, `SELECT body FROM projects ORDER BY name,id`)
}
func (s *Store) Project(ctx context.Context, id string) (domain.Project, error) {
	return decodeRow[domain.Project](s.Row(ctx, `SELECT body FROM projects WHERE id=? OR path=? OR name=? ORDER BY CASE WHEN id=? THEN 0 WHEN path=? THEN 1 ELSE 2 END LIMIT 1`, id, id, id, id, id))
}
func (s *Store) Snapshot(ctx context.Context, id string) (domain.Snapshot, error) {
	return decodeRow[domain.Snapshot](s.Row(ctx, `SELECT body FROM snapshots WHERE project=?`, id))
}
func (t *Tx) PutSnapshot(ctx context.Context, id string, v domain.Snapshot) error {
	_, err := t.Exec(ctx, `INSERT INTO snapshots(project,body) VALUES(?,?) ON CONFLICT(project) DO UPDATE SET body=excluded.body`, id, domain.JSON(v))
	return err
}
func (s *Store) Session(ctx context.Context, id string) (domain.Session, error) {
	return decodeRow[domain.Session](s.Row(ctx, `SELECT body FROM sessions WHERE id=?`, id))
}
func (t *Tx) Session(ctx context.Context, id string) (domain.Session, error) {
	return decodeRow[domain.Session](t.Row(ctx, `SELECT body FROM sessions WHERE id=?`, id))
}
func (t *Tx) PutSession(ctx context.Context, v domain.Session) error {
	_, err := t.Exec(ctx, `INSERT INTO sessions(id,project,path,cursor,body) VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET project=excluded.project,path=excluded.path,cursor=excluded.cursor,body=excluded.body`, v.ID, v.Project, v.Path, v.Cursor, domain.JSON(v))
	return err
}
func (s *Store) Sessions(ctx context.Context, project string) ([]domain.Session, error) {
	return list[domain.Session](ctx, s, `SELECT body FROM sessions WHERE project=? ORDER BY id`, project)
}
func (t *Tx) PutEvidence(ctx context.Context, v domain.Evidence) error {
	_, err := t.Exec(ctx, `INSERT INTO artifacts(id,project,session,body) VALUES(?,?,?,?) ON CONFLICT(id) DO NOTHING`, v.ID, v.Project, v.Session, domain.JSON(v))
	return err
}
func (s *Store) Evidence(ctx context.Context, id string) (domain.Evidence, error) {
	return decodeRow[domain.Evidence](s.Row(ctx, `SELECT body FROM artifacts WHERE id=?`, id))
}
func (t *Tx) PutEntity(ctx context.Context, v domain.Entity) error {
	_, err := t.Exec(ctx, `INSERT INTO entities(id,project,kind,body) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET body=excluded.body`, v.ID, v.Project, v.Kind, domain.JSON(v))
	return err
}
func (t *Tx) PutRelationship(ctx context.Context, v domain.Relationship) error {
	_, err := t.Exec(ctx, `INSERT INTO relationships(id,from_id,to_id,type,source,valid_to,body) VALUES(?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET valid_to=excluded.valid_to,body=excluded.body`, v.ID, v.From, v.To, v.Type, v.Source, v.ValidTo, domain.JSON(v))
	return err
}
func (s *Store) Relationships(ctx context.Context, history bool) ([]domain.Relationship, error) {
	q := `SELECT body FROM relationships`
	if !history {
		q += ` WHERE valid_to=0`
	}
	q += ` ORDER BY id`
	return list[domain.Relationship](ctx, s, q)
}
func (s *Store) Claims(ctx context.Context, project string, history bool) ([]domain.Claim, error) {
	q := `SELECT body FROM claims WHERE (?='' OR project=?)`
	if !history {
		q += ` AND status!='superseded'`
	}
	q += ` ORDER BY created,id`
	return list[domain.Claim](ctx, s, q, project, project)
}
func (s *Store) Claim(ctx context.Context, id string) (domain.Claim, error) {
	return decodeRow[domain.Claim](s.Row(ctx, `SELECT body FROM claims WHERE id=?`, id))
}
func (t *Tx) PutClaim(ctx context.Context, v domain.Claim) error {
	_, err := t.Exec(ctx, `INSERT INTO claims(id,project,entity,claim_key,status,created,body) VALUES(?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET status=excluded.status,body=excluded.body`, v.ID, v.Project, v.Entity, v.Key, v.Status, v.Created, domain.JSON(v))
	return err
}
func (t *Tx) PutMemory(ctx context.Context, v domain.Memory) error {
	_, err := t.Exec(ctx, `INSERT INTO memories(id,project,session,model,created,body) VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`, v.ID, v.Project, v.Session, v.Model, v.Created, domain.JSON(v))
	return err
}
func (s *Store) Memories(ctx context.Context, project string) ([]domain.Memory, error) {
	return list[domain.Memory](ctx, s, `SELECT body FROM memories WHERE (?='' OR project=?) ORDER BY created DESC LIMIT 10000`, project, project)
}
func (s *Store) Search(ctx context.Context, project, query, model string, vector []float64, limit int) ([]domain.Match, error) {
	mem, err := s.Memories(ctx, project)
	if err != nil {
		return nil, err
	}
	if limit < 1 || limit > 100 {
		limit = 10
	}
	matches := []domain.Match{}
	words := strings.Fields(strings.ToLower(query))
	for _, m := range mem {
		lex := 0.0
		for _, w := range words {
			if strings.Contains(strings.ToLower(m.Text), w) {
				lex++
			}
		}
		if len(words) > 0 {
			lex /= float64(len(words))
		}
		score := lex
		if len(vector) > 0 && m.Model == model && len(vector) == len(m.Embedding) {
			score = 0.8*math.Max(0, Cosine(vector, m.Embedding)) + 0.2*lex
		}
		if score > 0 {
			m.Embedding = nil
			matches = append(matches, domain.Match{Memory: m, Score: score})
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Score == matches[j].Score {
			return matches[i].Memory.ID < matches[j].Memory.ID
		}
		return matches[i].Score > matches[j].Score
	})
	if len(matches) > limit {
		matches = matches[:limit]
	}
	return matches, nil
}
func Cosine(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, x, y float64
	for i, v := range a {
		dot += v * b[i]
		x += v * v
		y += b[i] * b[i]
	}
	if x == 0 || y == 0 {
		return 0
	}
	v := dot / math.Sqrt(x*y)
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Max(-1, math.Min(1, v))
}
func (t *Tx) PutImpact(ctx context.Context, v domain.Impact) (bool, error) {
	r, err := t.Exec(ctx, `INSERT INTO impacts(id,changed,affected,status,created,body) VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`, v.ID, v.Changed, v.Affected, v.Status, v.Created, domain.JSON(v))
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n > 0, err
}
func (s *Store) Impact(ctx context.Context, id string) (domain.Impact, error) {
	return decodeRow[domain.Impact](s.Row(ctx, `SELECT body FROM impacts WHERE id=?`, id))
}
func (s *Store) Impacts(ctx context.Context, project, status string) ([]domain.Impact, error) {
	return list[domain.Impact](ctx, s, `SELECT body FROM impacts WHERE (?='' OR affected=? OR changed=?) AND (?='' OR status=?) ORDER BY created DESC,id LIMIT 1000`, project, project, project, status, status)
}
func (s *Store) SetImpactStatus(ctx context.Context, id, status string) error {
	if status != "acknowledged" && status != "resolved" && status != "dismissed" && status != "open" {
		return errors.New("invalid impact status")
	}
	return s.Transaction(ctx, func(t *Tx) error {
		v, err := decodeRow[domain.Impact](t.Row(ctx, `SELECT body FROM impacts WHERE id=?`, id))
		if err != nil {
			return err
		}
		v.Status = status
		_, err = t.Exec(ctx, `UPDATE impacts SET status=?,body=? WHERE id=?`, status, domain.JSON(v), id)
		return err
	})
}

func (t *Tx) Enqueue(ctx context.Context, kind, key string, payload any, delay time.Duration) error {
	_, err := t.Exec(ctx, `INSERT INTO jobs(id,kind,payload,status,attempts,available,lease,owner,error) VALUES(?,?,?,'pending',0,?,0,'','') ON CONFLICT(id) DO NOTHING`, domain.ID(kind, key), kind, domain.JSON(payload), domain.Now()+delay.Milliseconds())
	return err
}
func (s *Store) Enqueue(ctx context.Context, kind, key string, payload any, delay time.Duration) error {
	return s.Transaction(ctx, func(t *Tx) error { return t.Enqueue(ctx, kind, key, payload, delay) })
}
func (s *Store) ClaimJob(ctx context.Context, owner string, lease time.Duration) (domain.Job, error) {
	var j domain.Job
	now := domain.Now()
	err := s.Transaction(ctx, func(t *Tx) error {
		q := `SELECT id,kind,payload,status,attempts,available,lease,owner,error FROM jobs WHERE (status='pending' AND available<=?) OR (status='running' AND lease<?) ORDER BY available,id LIMIT 1`
		if s.driver == "postgres" {
			q += ` FOR UPDATE SKIP LOCKED`
		}
		err := t.Row(ctx, q, now, now).Scan(&j.ID, &j.Kind, &j.Payload, &j.Status, &j.Attempts, &j.Available, &j.Lease, &j.Owner, &j.Error)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		j.Status = "running"
		j.Owner = owner
		j.Lease = now + lease.Milliseconds()
		j.Attempts++
		r, err := t.Exec(ctx, `UPDATE jobs SET status='running',owner=?,lease=?,attempts=? WHERE id=? AND ((status='pending' AND available<=?) OR (status='running' AND lease<?))`, owner, j.Lease, j.Attempts, j.ID, now, now)
		if err != nil {
			return err
		}
		n, err := r.RowsAffected()
		if n != 1 {
			return ErrNotFound
		}
		return err
	})
	return j, err
}
func (s *Store) RenewJob(ctx context.Context, j domain.Job, lease time.Duration) error {
	r, err := s.Exec(ctx, `UPDATE jobs SET lease=? WHERE id=? AND owner=? AND status='running'`, domain.Now()+lease.Milliseconds(), j.ID, j.Owner)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return errors.New("job lease lost")
	}
	return nil
}
func (s *Store) FinishJob(ctx context.Context, j domain.Job, workErr error) error {
	status := "done"
	msg := ""
	available := domain.Now()
	if workErr != nil {
		status = "pending"
		if j.Attempts >= 5 {
			status = "failed"
		}
		msg = workErr.Error()
		if len(msg) > 500 {
			msg = msg[:500]
		}
		available += int64(time.Second/time.Millisecond) * int64(1<<min(j.Attempts, 10))
	}
	r, err := s.Exec(ctx, `UPDATE jobs SET status=?,error=?,available=?,lease=0 WHERE id=? AND owner=? AND status='running'`, status, msg, available, j.ID, j.Owner)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return errors.New("job lease lost")
	}
	return nil
}
func (s *Store) Jobs(ctx context.Context) ([]domain.Job, error) {
	r, err := s.DB.QueryContext(ctx, `SELECT id,kind,payload,status,attempts,available,lease,owner,error FROM jobs ORDER BY available DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	v := []domain.Job{}
	for r.Next() {
		var j domain.Job
		if err = r.Scan(&j.ID, &j.Kind, &j.Payload, &j.Status, &j.Attempts, &j.Available, &j.Lease, &j.Owner, &j.Error); err != nil {
			return nil, err
		}
		v = append(v, j)
	}
	return v, r.Err()
}
func (s *Store) RetryJob(ctx context.Context, id string) error {
	r, err := s.Exec(ctx, `UPDATE jobs SET status='pending',attempts=0,available=?,error='' WHERE id=? AND status='failed'`, domain.Now(), id)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return errors.New("job is not failed or does not exist")
	}
	return nil
}
func (s *Store) Cache(ctx context.Context, id string) (string, error) {
	var b string
	err := s.Row(ctx, `SELECT body FROM cache WHERE id=?`, id).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return b, err
}
func (s *Store) PutCache(ctx context.Context, id, body string) error {
	_, err := s.Exec(ctx, `INSERT INTO cache(id,body,created) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET body=excluded.body,created=excluded.created`, id, body, domain.Now())
	return err
}

// Reservations are persistent and pessimistic, including failed/ambiguous calls.
// They are never silently released after a crash or an HTTP timeout.
func (s *Store) Reserve(ctx context.Context, id, operation, model string, reserve, budget float64) error {
	if reserve <= 0 || math.IsNaN(reserve) || math.IsInf(reserve, 0) {
		return errors.New("invalid reservation")
	}
	return s.Transaction(ctx, func(t *Tx) error {
		// PostgreSQL needs a cross-process lock; SQLite serializes writes.
		if s.driver == "postgres" {
			if _, err := t.Exec(ctx, `SELECT pg_advisory_xact_lock(74332026)`); err != nil {
				return err
			}
		}
		var used float64
		if err := t.Row(ctx, `SELECT COALESCE(SUM(CASE WHEN status='complete' THEN cost ELSE reserved END),0) FROM usage_ledger`).Scan(&used); err != nil {
			return err
		}
		if used+reserve > budget {
			return ErrBudget
		}
		_, err := t.Exec(ctx, `INSERT INTO usage_ledger(id,operation,model,input_tokens,output_tokens,reserved,cost,status,created) VALUES(?,?,?,0,0,?,0,'reserved',?)`, id, operation, model, reserve, domain.Now())
		return err
	})
}
func (s *Store) Settle(ctx context.Context, id string, input, output int, cost float64) error {
	_, err := s.Exec(ctx, `UPDATE usage_ledger SET input_tokens=?,output_tokens=?,cost=?,status='complete' WHERE id=?`, input, output, cost, id)
	return err
}

type Usage struct {
	Spent    float64 `json:"spent_usd"`
	Reserved float64 `json:"reserved_usd"`
	Calls    int     `json:"calls"`
	Input    int64   `json:"input_tokens"`
	Output   int64   `json:"output_tokens"`
}

func (s *Store) Usage(ctx context.Context) (Usage, error) {
	var u Usage
	err := s.Row(ctx, `SELECT COALESCE(SUM(CASE WHEN status='complete' THEN cost ELSE 0 END),0),COALESCE(SUM(CASE WHEN status='reserved' THEN reserved ELSE 0 END),0),COUNT(*),COALESCE(SUM(input_tokens),0),COALESCE(SUM(output_tokens),0) FROM usage_ledger`).Scan(&u.Spent, &u.Reserved, &u.Calls, &u.Input, &u.Output)
	return u, err
}
func (s *Store) PutRun(ctx context.Context, v domain.AutomationRun) error {
	_, err := s.Exec(ctx, `INSERT INTO automation_runs(id,impact,project,status,created,body) VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET status=excluded.status,body=excluded.body`, v.ID, v.Impact, v.Project, v.Status, v.Created, domain.JSON(v))
	return err
}
func (s *Store) Runs(ctx context.Context) ([]domain.AutomationRun, error) {
	return list[domain.AutomationRun](ctx, s, `SELECT body FROM automation_runs ORDER BY created DESC LIMIT 200`)
}

func (s *Store) AcquireLease(ctx context.Context, name, owner string, duration time.Duration) error {
	r, err := s.Exec(ctx, `INSERT INTO named_leases(name,owner,expires) VALUES(?,?,?) ON CONFLICT(name) DO UPDATE SET owner=excluded.owner,expires=excluded.expires WHERE named_leases.expires<?`, name, owner, domain.Now()+duration.Milliseconds(), domain.Now())
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return errors.New("operation is already running in another process")
	}
	return nil
}
func (s *Store) RenewLease(ctx context.Context, name, owner string, duration time.Duration) error {
	r, err := s.Exec(ctx, `UPDATE named_leases SET expires=? WHERE name=? AND owner=?`, domain.Now()+duration.Milliseconds(), name, owner)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return errors.New("operation lease lost")
	}
	return nil
}
func (s *Store) ReleaseLease(ctx context.Context, name, owner string) error {
	_, err := s.Exec(ctx, `DELETE FROM named_leases WHERE name=? AND owner=?`, name, owner)
	return err
}
