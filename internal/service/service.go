// Package service coordinates capture, association and reaction as separate jobs.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jakecyr/stalemate/internal/ai"
	"github.com/jakecyr/stalemate/internal/capture"
	"github.com/jakecyr/stalemate/internal/config"
	"github.com/jakecyr/stalemate/internal/domain"
	"github.com/jakecyr/stalemate/internal/privacy"
	"github.com/jakecyr/stalemate/internal/repo"
	"github.com/jakecyr/stalemate/internal/store"
)

type Service struct {
	Config   config.Config
	Store    *store.Store
	AI       ai.Provider
	Filter   *privacy.Filter
	Sources  []capture.Source
	Log      *slog.Logger
	mu       sync.Mutex
	Handlers map[string]func(context.Context, domain.Job) error
}

func New(c config.Config, s *store.Store, p ai.Provider, f *privacy.Filter) *Service {
	return &Service{Config: c, Store: s, AI: p, Filter: f, Log: slog.Default(), Sources: []capture.Source{&capture.Codex{Roots: []string{filepath.Join(c.Capture.CodexHome, "sessions"), filepath.Join(c.Capture.CodexHome, "archived_sessions"), filepath.Join(c.DataDir, "spool")}}}, Handlers: map[string]func(context.Context, domain.Job) error{}}
}
func (s *Service) Register(ctx context.Context, path, name string, cloud bool) (domain.Project, error) {
	p, err := repo.Canonical(path)
	if err != nil {
		return domain.Project{}, err
	}
	snap, err := repo.Build(ctx, p, s.Filter)
	if err != nil {
		return domain.Project{}, err
	}
	if name == "" {
		name = filepath.Base(p)
	}
	v := domain.Project{ID: domain.ID("project", p), Name: name, Path: p, Module: snap.Module, CloudAllowed: cloud}
	if err = s.Store.PutProject(ctx, v); err != nil {
		return v, err
	}
	return v, s.Store.Enqueue(ctx, "scan", domain.ID(v.ID, fmt.Sprint(domain.Now())), map[string]string{"project": v.ID}, 0)
}

// Poll only admits registered project paths. Active transcripts are deferred
// unless a completed-turn event is present; replay begins at a committed cursor.
func (s *Service) Poll(ctx context.Context, force bool) error {
	projects, err := s.Store.Projects(ctx)
	if err != nil {
		return err
	}
	for _, source := range s.Sources {
		files, e := source.Discover(ctx)
		if e != nil {
			return e
		}
		for _, path := range files {
			if e = ctx.Err(); e != nil {
				return e
			}
			h, e := source.Header(path)
			if e != nil {
				continue
			}
			p, ok := capture.ProjectFor(h.CWD, projects)
			if !ok {
				continue
			}
			id := domain.ID(source.Name(), h.ID)
			session, e := s.Store.Session(ctx, id)
			if e != nil && !errors.Is(e, store.ErrNotFound) {
				return e
			}
			cursor := session.Cursor
			if session.Prefix != "" && session.Prefix != h.Prefix {
				cursor = 0
			}
			info, e := os.Stat(path)
			if e != nil {
				continue
			}
			if cursor > info.Size() {
				cursor = 0
			}
			if cursor == info.Size() {
				continue
			}
			batch, e := source.Read(path, cursor, s.Config.Capture.MaxBytes)
			if e != nil {
				s.Log.Warn("capture deferred", "file", filepath.Base(path), "error", e)
				continue
			}
			if !batch.Changed {
				continue
			}
			if !force && !batch.Completed && time.Since(batch.ModTime) < time.Duration(s.Config.Capture.IdleSeconds)*time.Second {
				continue
			}
			ss := domain.Session{ID: id, Project: p.ID, Source: source.Name(), Path: path, Cursor: batch.Cursor, Prefix: h.Prefix, LastSeen: domain.Now(), Status: "captured"}
			if batch.Completed {
				ss.Status = "turn_completed"
			}
			text := s.Filter.Text(batch.Text)
			evidence := domain.Evidence{ID: domain.ID("session", id, h.Prefix, fmt.Sprint(cursor), fmt.Sprint(batch.Cursor)), Project: p.ID, Session: id, Kind: "transcript", Locator: fmt.Sprintf("%s bytes %d:%d", filepath.Base(path), cursor, batch.Cursor), Text: text}
			e = s.Store.Transaction(ctx, func(t *store.Tx) error {
				current, err := t.Session(ctx, id)
				if err != nil && !errors.Is(err, store.ErrNotFound) {
					return err
				}
				if current.Cursor != session.Cursor || current.Prefix != session.Prefix {
					return nil
				}
				if err := t.PutSession(ctx, ss); err != nil {
					return err
				}
				if text != "" {
					if err := t.PutEvidence(ctx, evidence); err != nil {
						return err
					}
					if err := t.Enqueue(ctx, "extract", evidence.ID, map[string]string{"evidence": evidence.ID}, 0); err != nil {
						return err
					}
				}
				return t.Enqueue(ctx, "scan", evidence.ID, map[string]string{"project": p.ID}, 0)
			})
			if e != nil {
				return e
			}
		}
	}
	return nil
}

func (s *Service) Scan(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	owner := domain.ID("scan", fmt.Sprint(os.Getpid()), fmt.Sprint(time.Now().UnixNano()))
	if err := s.Store.AcquireLease(ctx, "repository-scan", owner, 10*time.Minute); err != nil {
		return err
	}
	defer func() {
		release, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = s.Store.ReleaseLease(release, "repository-scan", owner)
	}()
	projects, err := s.Store.Projects(ctx)
	if err != nil {
		return err
	}
	snaps := map[string]domain.Snapshot{}
	previous := map[string]domain.Snapshot{}
	skipped := map[string]bool{}
	scanErrors := []error{}
	// Analyze all projects before associating identities, including newly added
	// providers. A failed parse leaves the previous graph and snapshot intact.
	for i, p := range projects {
		old, e := s.Store.Snapshot(ctx, p.ID)
		if e != nil && !errors.Is(e, store.ErrNotFound) {
			return e
		}
		previous[p.ID] = old
		next, e := repo.Build(ctx, p.Path, s.Filter)
		if e != nil {
			scanErrors = append(scanErrors, fmt.Errorf("scan %s: %w", p.Name, e))
			skipped[p.ID] = true
			snaps[p.ID] = old
			continue
		}
		next.Diff = repo.DiffSince(ctx, p.Path, old, next, s.Filter)
		snaps[p.ID] = next
		if p.Module != next.Module {
			p.Module = next.Module
			projects[i] = p
			if e = s.Store.PutProject(ctx, p); e != nil {
				return e
			}
		}
	}
	existing, err := s.Store.Relationships(ctx, false)
	if err != nil {
		return err
	}
	desired := map[string]domain.Relationship{}
	for _, p := range projects {
		if skipped[p.ID] {
			for _, r := range existing {
				if r.From == p.ID && r.Source == "static_analysis" {
					desired[r.ID] = r
				}
			}
			continue
		}
		snap := snaps[p.ID]
		merged := map[string]domain.Relationship{}
		for _, dep := range snap.Dependencies {
			matches := []domain.Project{}
			for _, target := range projects {
				if target.ID == p.ID {
					continue
				}
				other := snaps[target.ID]
				if (dep.Path != "" && dep.Path == target.Path) || (dep.Path == "" && ((dep.Kind == "go" && target.Module != "" && target.Module == dep.Name) || (dep.Kind == "npm" && other.PackageName != "" && other.PackageName == dep.Name))) {
					matches = append(matches, target)
				}
			}
			if len(matches) != 1 {
				continue
			}
			target := matches[0]
			r := merged[target.ID]
			r.From = p.ID
			r.To = target.ID
			r.Type = "DEPENDS_ON"
			r.Source = "static_analysis"
			r.Confidence = 1
			r.Evidence = appendUnique(r.Evidence, dep.Evidence)
			r.Revision = snap.Revision
			r.Version = dep.Version
			r.Binding = "declared"
			if dep.Path == target.Path {
				r.Binding = "local"
			}
			r.APIs = append(r.APIs, dep.APIs...)
			merged[target.ID] = r
		}
		for _, r := range merged {
			sort.Strings(r.APIs)
			r.APIs = unique(r.APIs)
			r.ID = domain.ID("static-edge", r.From, r.To, r.Evidence, r.Version, r.Binding, domain.JSON(r.APIs), snap.Fingerprint)
			r.ValidFrom = domain.Now()
			desired[r.ID] = r
		}
	}
	for _, r := range existing {
		if _, ok := desired[r.ID]; ok {
			desired[r.ID] = r
		}
	}
	// Compare against the *old* graph: removing a consumer's usage in the same
	// scan does not erase the evidence on which its earlier work was based.
	graph := []domain.Relationship{}
	for _, r := range existing {
		if (r.Type == "DEPENDS_ON" || r.Type == "ASSUMES") && (r.Source == "static_analysis" || r.Source == "explicit") {
			graph = append(graph, r)
		}
	}
	err = s.Store.Transaction(ctx, func(t *store.Tx) error {
		for _, p := range projects {
			next := snaps[p.ID]
			old := previous[p.ID]
			if err := t.PutEntity(ctx, domain.Entity{ID: p.ID, Project: p.ID, Name: p.Name, Kind: "project"}); err != nil {
				return err
			}
			if old.Fingerprint == next.Fingerprint {
				continue
			}
			if err := t.PutSnapshot(ctx, p.ID, next); err != nil {
				return err
			}
			changes := repo.Changes(old, next)
			if old.Fingerprint == "" {
				continue
			}
			e := domain.Evidence{ID: domain.ID("repo", p.ID, next.Fingerprint), Project: p.ID, Kind: "git_change", Locator: "git:" + next.Revision, Revision: next.Revision, Text: s.Filter.Text(domain.JSON(map[string]any{"changes": changes, "diff": next.Diff, "files": next.Files}))}
			if err := t.PutEvidence(ctx, e); err != nil {
				return err
			}
			if len(changes) > 0 {
				if err := s.propagate(ctx, t, p.ID, next.Fingerprint, changes, e, graph); err != nil {
					return err
				}
			}
			if next.Diff != "" {
				if err := t.Enqueue(ctx, "extract", e.ID, map[string]string{"evidence": e.ID}, 0); err != nil {
					return err
				}
			}
		}
		for _, r := range existing {
			if r.Source == "static_analysis" {
				if _, ok := desired[r.ID]; !ok {
					r.ValidTo = domain.Now()
					if err := t.PutRelationship(ctx, r); err != nil {
						return err
					}
				}
			}
		}
		for _, r := range desired {
			if err := t.PutRelationship(ctx, r); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return errors.Join(scanErrors...)
}
func appendUnique(old, next string) string {
	if old == "" {
		return next
	}
	if old == next {
		return old
	}
	return old + "; " + next
}
func unique(v []string) []string {
	out := []string{}
	for _, s := range v {
		if len(out) == 0 || out[len(out)-1] != s {
			out = append(out, s)
		}
	}
	return out
}

func (s *Service) propagate(ctx context.Context, t *store.Tx, changed, key string, changes []domain.Change, e domain.Evidence, graph []domain.Relationship) error {
	type item struct {
		id     string
		path   []string
		direct bool
	}
	queue := []item{{id: changed, path: []string{changed}, direct: true}}
	seen := map[string]bool{changed: true}
	count := 0
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if len(current.path) > 32 {
			return errors.New("impact traversal exceeds 32 levels")
		}
		for _, edge := range graph {
			if !((edge.Type == "DEPENDS_ON" && (edge.Source == "static_analysis" || edge.Source == "explicit")) || (edge.Type == "ASSUMES" && edge.Source == "explicit")) {
				continue
			}
			if edge.To != current.id || seen[edge.From] {
				continue
			}
			relevant := changes
			if current.direct && edge.Type == "DEPENDS_ON" {
				relevant = repo.Relevant(edge, changes)
				if len(relevant) == 0 {
					continue
				}
			}
			seen[edge.From] = true
			count++
			if count > 1000 {
				return errors.New("impact traversal exceeds 1000 projects")
			}
			path := append(append([]string{}, current.path...), edge.From)
			severity := "possible"
			if current.direct {
				for _, c := range relevant {
					if c.Kind == "removed" && edge.Binding == "local" {
						for _, api := range edge.APIs {
							if c.API == api {
								severity = "confirmed"
							}
						}
					}
				}
			}
			reason := fmt.Sprintf("Dependency changed: %s. %s", domain.JSON(relevant), edge.Evidence)
			if len(path) > 2 {
				reason = "Transitive dependency exposure. " + reason
				severity = "possible"
			}
			v := domain.Impact{ID: domain.ID("impact", changed, edge.From, key), Changed: e.Project, Affected: edge.From, ChangeKey: key, Severity: severity, Status: "open", Reason: s.Filter.Text(reason), Evidence: e.ID, Path: path, Verification: "Run the affected project's tests against the changed dependency; verify the consumed API and version before changing code.", Created: domain.Now()}
			if err := s.putImpact(ctx, t, v); err != nil {
				return err
			}
			queue = append(queue, item{id: edge.From, path: path})
		}
	}
	return nil
}
func (s *Service) putImpact(ctx context.Context, t *store.Tx, v domain.Impact) error {
	inserted, err := t.PutImpact(ctx, v)
	if err != nil || !inserted {
		return err
	}
	if err = t.Enqueue(ctx, "notify", v.ID, map[string]string{"impact": v.ID}, 0); err != nil {
		return err
	}
	if v.Severity != "confirmed" && s.Config.AI.Provider != "disabled" {
		if err = t.Enqueue(ctx, "assess", v.ID, map[string]string{"impact": v.ID}, 0); err != nil {
			return err
		}
	}
	if s.Config.Automation.Mode == "automatic" && v.Severity == "confirmed" {
		return t.Enqueue(ctx, "remediate", v.ID, map[string]string{"impact": v.ID}, 0)
	}
	return nil
}

func (s *Service) Extract(ctx context.Context, evidenceID string) error {
	e, err := s.Store.Evidence(ctx, evidenceID)
	if err != nil {
		return err
	}
	p, err := s.Store.Project(ctx, e.Project)
	if err != nil {
		return err
	}
	provider := s.AI
	if !p.CloudAllowed {
		provider = ai.Disabled{}
	}
	projects, err := s.Store.Projects(ctx)
	if err != nil {
		return err
	}
	known := map[string]string{}
	for _, p := range projects {
		if p.CloudAllowed {
			known[p.ID] = p.Name
		}
	}
	current, err := s.Store.Claims(ctx, p.ID, false)
	if err != nil {
		return err
	}
	active := map[string]domain.Claim{}
	for _, c := range current {
		if c.Status == "active" {
			active[c.Key] = c
		}
	}
	out, err := provider.Extract(ctx, e.Text+"\nKnown projects (identity only, not evidence):\n"+domain.JSON(known)+"\nActive claim keys (reference only; reuse a key only for the same assertion):\n"+domain.JSON(active))
	if err != nil {
		return err
	}
	text := s.Filter.Text(out.Summary)
	if strings.TrimSpace(text) == "" {
		return errors.New("extraction returned an empty summary")
	}
	vec, err := provider.Embed(ctx, text)
	if err != nil {
		return err
	}
	m := domain.Memory{ID: domain.ID("memory", e.ID, provider.EmbeddingModel()), Project: p.ID, Session: e.Session, Text: text, Embedding: vec, Model: provider.EmbeddingModel(), Created: domain.Now()}
	// Similarity proposes associations only. It never becomes a verified edge.
	candidates := []domain.Match{}
	if len(vec) > 0 {
		candidates, err = s.Store.Search(ctx, "", text, provider.EmbeddingModel(), vec, 8)
		if err != nil {
			return err
		}
	}
	graph, err := s.Store.Relationships(ctx, false)
	if err != nil {
		return err
	}
	hasDependents := map[string]bool{}
	for _, r := range graph {
		if r.Source == "explicit" && r.Type == "ASSUMES" {
			hasDependents[r.To] = true
		}
	}
	return s.Store.Transaction(ctx, func(t *store.Tx) error {
		if err := t.PutMemory(ctx, m); err != nil {
			return err
		}
		for _, c := range out.Claims {
			// Exact evidence quotations are mandatory, even with strict JSON output.
			if strings.TrimSpace(c.Quote) == "" || !strings.Contains(e.Text, c.Quote) || c.Key == "" || c.Text == "" {
				continue
			}
			entity := domain.ID("claim-entity", p.ID, c.Key)
			claim := domain.Claim{ID: domain.ID("extracted-claim", e.ID, c.Key, c.Text), Project: p.ID, Entity: entity, Key: c.Key, Text: s.Filter.Text(c.Text), Kind: c.Kind, Status: "proposed", Evidence: e.ID, Revision: e.Revision, Created: domain.Now(), Classification: "addition"}
			if old, ok := active[c.Key]; ok {
				if old.Text == claim.Text {
					continue
				}
				claim.Previous = old.ID
				claim.Classification = "proposed_revision"
			}
			// An inferred claim never silently supersedes an explicit assertion.
			if err := t.PutEntity(ctx, domain.Entity{ID: entity, Project: p.ID, Name: c.Key, Kind: c.Kind}); err != nil {
				return err
			}
			if err := t.PutClaim(ctx, claim); err != nil {
				return err
			}
			if claim.Previous != "" && hasDependents[entity] && p.CloudAllowed {
				if err := t.Enqueue(ctx, "review_claim", claim.ID, map[string]string{"claim": claim.ID}, 0); err != nil {
					return err
				}
			}
		}
		for _, link := range out.Links {
			if _, ok := known[link.Target]; !ok || link.Target == p.ID || link.Quote == "" || !strings.Contains(e.Text, link.Quote) || link.Confidence < 0 || link.Confidence > 1 {
				continue
			}
			r := domain.Relationship{ID: domain.ID("candidate", e.ID, link.Target, link.Type), From: p.ID, To: link.Target, Type: link.Type, Source: "llm_candidate", Confidence: link.Confidence, Evidence: e.ID, Revision: e.Revision, ValidFrom: domain.Now(), APIs: []string{}}
			if err := t.PutRelationship(ctx, r); err != nil {
				return err
			}
		}
		for _, candidate := range candidates {
			if candidate.Memory.Project == p.ID || candidate.Memory.Model != provider.EmbeddingModel() || candidate.Score < 0.82 {
				continue
			}
			r := domain.Relationship{ID: domain.ID("embedding-association", m.ID, candidate.Memory.ID), From: p.ID, To: candidate.Memory.Project, Type: "RELATED_TO", Source: "embedding_candidate", Confidence: candidate.Score, Evidence: e.ID + "; memory:" + candidate.Memory.ID, ValidFrom: domain.Now(), APIs: []string{}}
			if err := t.PutRelationship(ctx, r); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Service) ReviewClaim(ctx context.Context, id string) error {
	next, err := s.Store.Claim(ctx, id)
	if err != nil {
		return err
	}
	if next.Previous == "" {
		return nil
	}
	old, err := s.Store.Claim(ctx, next.Previous)
	if err != nil {
		return err
	}
	if old.Status != "active" {
		return nil
	}
	p, err := s.Store.Project(ctx, next.Project)
	if err != nil {
		return err
	}
	if !p.CloudAllowed {
		return nil
	}
	e, err := s.Store.Evidence(ctx, next.Evidence)
	if err != nil {
		return err
	}
	a, err := s.AI.Assess(ctx, "Assess whether the new evidence directly contradicts the old assumption. A refinement or paraphrase is unaffected; an uncertain contradiction is possible.\n"+domain.JSON(map[string]any{"old": old, "proposed": next, "new_evidence": e.Text}))
	if err != nil {
		return err
	}
	next.Classification = "uncertain_revision"
	if a.Verdict == "unaffected" {
		next.Classification = "refinement"
	}
	if a.Verdict == "confirmed" {
		next.Classification = "contradiction"
	}
	graph, err := s.Store.Relationships(ctx, false)
	if err != nil {
		return err
	}
	return s.Store.Transaction(ctx, func(t *store.Tx) error {
		if err := t.PutClaim(ctx, next); err != nil {
			return err
		}
		if next.Classification != "contradiction" {
			return nil
		}
		return s.propagate(ctx, t, old.Entity, next.ID, []domain.Change{{API: old.Entity, Before: old.Text, After: next.Text, Kind: "evidence_backed_claim_conflict"}}, e, graph)
	})
}

// Record is the explicit assumption ledger. Versioning and invalidation happen
// in the same transaction, retaining every previous assertion and dependency.
func (s *Service) Record(ctx context.Context, project, key, text, kind, evidence, previous string) (domain.Claim, error) {
	p, err := s.Store.Project(ctx, project)
	if err != nil {
		return domain.Claim{}, err
	}
	if strings.TrimSpace(key) == "" || strings.TrimSpace(text) == "" || strings.TrimSpace(evidence) == "" {
		return domain.Claim{}, errors.New("key, text and evidence are required")
	}
	if kind != "assumption" && kind != "decision" && kind != "constraint" && kind != "observation" {
		return domain.Claim{}, errors.New("invalid claim kind")
	}
	claims, err := s.Store.Claims(ctx, p.ID, false)
	if err != nil {
		return domain.Claim{}, err
	}
	var old domain.Claim
	for _, c := range claims {
		if c.Key == key && c.Status == "active" {
			old = c
		}
	}
	if old.ID != "" && old.Text == s.Filter.Text(text) {
		return old, nil
	}
	if old.ID != "" && old.ID != previous {
		return domain.Claim{}, errors.New("claim changed; provide the current claim ID as previous to supersede it")
	}
	if previous != "" && old.ID == "" {
		return domain.Claim{}, errors.New("previous does not match an active claim")
	}
	entity := domain.ID("claim-entity", p.ID, key)
	ev := domain.Evidence{ID: domain.ID("explicit-evidence", p.ID, evidence), Project: p.ID, Kind: "explicit", Locator: "context_record", Text: s.Filter.Text(evidence)}
	v := domain.Claim{ID: domain.ID("claim", entity, text, previous), Project: p.ID, Entity: entity, Key: key, Text: s.Filter.Text(text), Kind: kind, Status: "active", Evidence: ev.ID, Previous: previous, Created: domain.Now()}
	v.Classification = "addition"
	if previous != "" {
		v.Classification = "supersession"
	}
	graph, err := s.Store.Relationships(ctx, false)
	if err != nil {
		return v, err
	}
	err = s.Store.Transaction(ctx, func(t *store.Tx) error {
		if err := t.PutEvidence(ctx, ev); err != nil {
			return err
		}
		if err := t.PutEntity(ctx, domain.Entity{ID: entity, Project: p.ID, Name: key, Kind: kind}); err != nil {
			return err
		}
		// Optimistic concurrency prevents two writers superseding the same version.
		var count int
		if err := t.Row(ctx, `SELECT COUNT(*) FROM claims WHERE project=? AND claim_key=? AND status='active'`, p.ID, key).Scan(&count); err != nil {
			return err
		}
		if old.ID != "" {
			var status string
			if err := t.Row(ctx, `SELECT status FROM claims WHERE id=?`, old.ID).Scan(&status); err != nil {
				return err
			}
			if status != "active" {
				return errors.New("claim was concurrently superseded")
			}
			old.Status = "superseded"
			if err := t.PutClaim(ctx, old); err != nil {
				return err
			}
		} else if count != 0 {
			return errors.New("claim was concurrently created")
		}
		if err := t.PutClaim(ctx, v); err != nil {
			return err
		}
		if old.ID != "" {
			changes := []domain.Change{{API: entity, Before: old.Text, After: v.Text, Kind: "assumption_superseded"}}
			return s.propagate(ctx, t, entity, v.ID, changes, ev, graph)
		}
		return nil
	})
	return v, err
}
func (s *Service) DependOnClaim(ctx context.Context, project, claimID, evidence string) error {
	p, err := s.Store.Project(ctx, project)
	if err != nil {
		return err
	}
	claims, err := s.Store.Claims(ctx, "", true)
	if err != nil {
		return err
	}
	var claim domain.Claim
	for _, c := range claims {
		if c.ID == claimID {
			claim = c
			break
		}
	}
	if claim.ID == "" {
		return store.ErrNotFound
	}
	if claim.Status != "active" {
		return errors.New("cannot rely on an inactive claim")
	}
	if evidence == "" {
		return errors.New("dependency evidence is required")
	}
	r := domain.Relationship{ID: domain.ID("assumes", p.ID, claimID), From: p.ID, To: claim.Entity, Type: "ASSUMES", Source: "explicit", Confidence: 1, Evidence: s.Filter.Text(evidence) + "; claim:" + claimID, Revision: claimID, APIs: []string{}, ValidFrom: domain.Now()}
	edges, err := s.Store.Relationships(ctx, false)
	if err != nil {
		return err
	}
	return s.Store.Transaction(ctx, func(t *store.Tx) error {
		for _, old := range edges {
			if old.From == p.ID && old.To == claim.Entity && old.Source == "explicit" && old.ID != r.ID {
				old.ValidTo = domain.Now()
				if err := t.PutRelationship(ctx, old); err != nil {
					return err
				}
			}
		}
		return t.PutRelationship(ctx, r)
	})
}
func (s *Service) Assess(ctx context.Context, id string) error {
	v, err := s.Store.Impact(ctx, id)
	if err != nil {
		return err
	}
	if v.Status != "open" || v.Severity == "confirmed" {
		return nil
	}
	affected, err := s.Store.Project(ctx, v.Affected)
	if err != nil {
		return err
	}
	if !affected.CloudAllowed {
		return nil
	}
	e, err := s.Store.Evidence(ctx, v.Evidence)
	if err != nil {
		return err
	}
	origin, err := s.Store.Project(ctx, e.Project)
	if err != nil {
		return err
	}
	if !origin.CloudAllowed {
		return nil
	}
	a, err := s.AI.Assess(ctx, domain.JSON(v)+"\nEvidence:\n"+e.Text)
	if err != nil {
		return err
	}
	// LLM assessments cannot confirm breakage or dismiss a deterministic alert.
	// Store the advisory with the original evidence and preserve explicit status.
	return s.Store.Transaction(ctx, func(t *store.Tx) error {
		var body string
		if err := t.Row(ctx, `SELECT body FROM impacts WHERE id=?`, id).Scan(&body); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(body), &v); err != nil {
			return err
		}
		v.Reason += "\nModel assessment (" + a.Verdict + "): " + s.Filter.Text(a.Reason)
		v.Verification = s.Filter.Text(a.Verification)
		_, err := t.Exec(ctx, `UPDATE impacts SET body=? WHERE id=?`, domain.JSON(v), id)
		return err
	})
}

type Context struct {
	Project      domain.Project        `json:"project"`
	Claims       []domain.Claim        `json:"claims"`
	Dependencies []domain.Relationship `json:"dependencies"`
	Impacts      []domain.Impact       `json:"impacts"`
	Related      []domain.Match        `json:"related"`
}

func (s *Service) GetContext(ctx context.Context, project, task string) (Context, error) {
	var out Context
	p, err := s.Store.Project(ctx, project)
	if err != nil {
		return out, err
	}
	out.Project = p
	out.Claims, err = s.Store.Claims(ctx, p.ID, false)
	if err != nil {
		return out, err
	}
	edges, err := s.Store.Relationships(ctx, false)
	if err != nil {
		return out, err
	}
	out.Dependencies = []domain.Relationship{}
	for _, e := range edges {
		if e.From == p.ID || e.To == p.ID {
			out.Dependencies = append(out.Dependencies, e)
		}
	}
	out.Impacts, err = s.Store.Impacts(ctx, p.ID, "open")
	if err != nil {
		return out, err
	}
	var vector []float64
	if task != "" && p.CloudAllowed {
		vector, err = s.AI.Embed(ctx, task)
		if err != nil {
			s.Log.Warn("context uses lexical search", "error", err)
		}
	}
	out.Related, err = s.Store.Search(ctx, p.ID, task, s.AI.EmbeddingModel(), vector, 10)
	return out, err
}

func (s *Service) ProcessJob(ctx context.Context, j domain.Job) error {
	var p map[string]string
	if err := json.Unmarshal([]byte(j.Payload), &p); err != nil {
		return err
	}
	switch j.Kind {
	case "capture":
		return s.Poll(ctx, true)
	case "scan":
		return s.Scan(ctx)
	case "extract":
		return s.Extract(ctx, p["evidence"])
	case "assess":
		return s.Assess(ctx, p["impact"])
	case "review_claim":
		return s.ReviewClaim(ctx, p["claim"])
	}
	if h := s.Handlers[j.Kind]; h != nil {
		return h(ctx, j)
	}
	return fmt.Errorf("no handler registered for job kind %s", j.Kind)
}
func (s *Service) Drain(ctx context.Context, maxJobs int) error {
	owner := domain.ID("worker", fmt.Sprint(os.Getpid()), fmt.Sprint(time.Now().UnixNano()))
	var first error
	for i := 0; i < maxJobs; i++ {
		j, err := s.Store.ClaimJob(ctx, owner, 2*time.Minute)
		if errors.Is(err, store.ErrNotFound) {
			return first
		}
		if err != nil {
			return err
		}
		workCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			defer close(done)
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-workCtx.Done():
					return
				case <-ticker.C:
					if err := s.Store.RenewJob(workCtx, j, 2*time.Minute); err != nil {
						cancel()
						return
					}
				}
			}
		}()
		err = s.ProcessJob(workCtx, j)
		cancel()
		<-done
		if err != nil {
			s.Log.Warn("job failed", "kind", j.Kind, "id", j.ID, "error", err)
			if first == nil {
				first = err
			}
		}
		finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		e := s.Store.FinishJob(finishCtx, j, err)
		finishCancel()
		if e != nil {
			return e
		}
	}
	return first
}
func (s *Service) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.Config.PollInterval())
	defer ticker.Stop()
	done := make(chan struct{})
	defer func() { <-done }()
	go func() {
		defer close(done)
		jobs := time.NewTicker(time.Second)
		defer jobs.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-jobs.C:
				if err := s.Drain(ctx, 1); err != nil {
					s.Log.Warn("worker cycle", "error", err)
				}
			}
		}
	}()
	cycle := func() {
		if err := s.Poll(ctx, false); err != nil {
			s.Log.Error("capture cycle", "error", err)
		}
		if err := s.Scan(ctx); err != nil {
			s.Log.Error("repository scan", "error", err)
		}
	}
	cycle()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			cycle()
		}
	}
}
