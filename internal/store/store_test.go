package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), "sqlite", filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func TestDurableJobDedupLeaseRecoveryAndOwnership(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := s.Enqueue(ctx, "extract", "same", map[string]string{"evidence": "e"}, 0); err != nil {
			t.Fatal(err)
		}
	}
	j, err := s.ClaimJob(ctx, "one", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimJob(ctx, "two", time.Minute); !errors.Is(err, ErrNotFound) {
		t.Fatal("leased job was stolen", err)
	}
	_, err = s.Exec(ctx, `UPDATE jobs SET lease=0 WHERE id=?`, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := s.ClaimJob(ctx, "two", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Attempts != 2 {
		t.Fatal(recovered)
	}
	if err = s.FinishJob(ctx, j, nil); err == nil {
		t.Fatal("old worker should not finish recovered job")
	}
	if err = s.FinishJob(ctx, recovered, nil); err != nil {
		t.Fatal(err)
	}
	jobs, err := s.Jobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].Status != "done" {
		t.Fatal(jobs)
	}
}
func TestBudgetReservationsAreAtomicAndSurviveReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.sqlite")
	s, err := Open(context.Background(), "sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted := 0
	for _, id := range []string{"a", "b", "c", "d"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			err := s.Reserve(ctx, id, "test", "model", 0.6, 1)
			if err == nil {
				mu.Lock()
				accepted++
				mu.Unlock()
			} else if !errors.Is(err, ErrBudget) {
				t.Error(err)
			}
		}(id)
	}
	wg.Wait()
	if accepted != 1 {
		t.Fatalf("accepted %d requests against a one-dollar budget", accepted)
	}
	s.Close()
	s, err = Open(ctx, "sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	usage, err := s.Usage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if usage.Reserved != 0.6 {
		t.Fatal(usage)
	}
	if err = s.Reserve(ctx, "e", "test", "model", 0.5, 1); !errors.Is(err, ErrBudget) {
		t.Fatal("restart released uncertain reservation", err)
	}
}
func TestCosineDoesNotMixDimensionsOrDivideByZero(t *testing.T) {
	for _, c := range []struct {
		a, b []float64
		want float64
	}{{[]float64{1, 0}, []float64{1, 0}, 1}, {[]float64{0, 0}, []float64{1, 0}, 0}, {[]float64{1}, []float64{1, 2}, 0}, {nil, nil, 0}} {
		if v := Cosine(c.a, c.b); v != c.want {
			t.Fatalf("got %v want %v", v, c.want)
		}
	}
}

// Run against a disposable database, never an existing production ledger.
func TestPostgresIntegration(t *testing.T) {
	dsn := os.Getenv("STALEMATE_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set STALEMATE_TEST_POSTGRES to test PostgreSQL")
	}
	s, err := Open(context.Background(), "postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err = s.Enqueue(ctx, "postgres-test", t.Name(), map[string]string{}, 0); err != nil {
		t.Fatal(err)
	}
	j, err := s.ClaimJob(ctx, "postgres", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.FinishJob(ctx, j, nil); err != nil {
		t.Fatal(err)
	}
	if err = s.Reserve(ctx, "postgres-reservation", "test", "model", 0.01, 10); err != nil {
		t.Fatal(err)
	}
	if err = s.Settle(ctx, "postgres-reservation", 10, 10, 0.001); err != nil {
		t.Fatal(err)
	}
}
