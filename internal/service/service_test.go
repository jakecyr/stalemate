package service

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jakecyr/stalemate/internal/ai"
	"github.com/jakecyr/stalemate/internal/config"
	"github.com/jakecyr/stalemate/internal/domain"
	"github.com/jakecyr/stalemate/internal/privacy"
	"github.com/jakecyr/stalemate/internal/repo"
	"github.com/jakecyr/stalemate/internal/store"
)

func setup(t *testing.T) *Service {
	t.Helper()
	c := config.Default()
	c.DataDir = t.TempDir()
	c.Capture.CodexHome = filepath.Join(c.DataDir, "codex")
	db, err := store.Open(context.Background(), "sqlite", filepath.Join(c.DataDir, "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := New(c, db, ai.Disabled{}, privacy.New(c.Capture.Exclude))
	s.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	s.Handlers["notify"] = func(context.Context, domain.Job) error { return nil }
	return s
}
func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}
func project(t *testing.T, s *Service, name, mod, code string) domain.Project {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	writeFile(t, filepath.Join(path, "go.mod"), "module example.com/"+name+"\n\ngo 1.22\n"+mod)
	writeFile(t, filepath.Join(path, "main.go"), code)
	if _, err := repo.Git(context.Background(), path, "init", "-b", "master"); err != nil {
		t.Fatal(err)
	}
	p, err := s.Register(context.Background(), path, name, false)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func check(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func TestCrossProjectImpactPrecisionAndTransitiveRecall(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	lib := project(t, s, "lib", "", "package lib\nfunc Value() string { return \"v1\" }\nfunc Unused() string { return \"a\" }\n")
	mid := project(t, s, "mid", "require example.com/lib v0.0.0\nreplace example.com/lib => "+lib.Path+"\n", "package mid\nimport \"example.com/lib\"\nfunc Result() string { return lib.Value() }\n")
	app := project(t, s, "app", "require example.com/mid v0.0.0\nreplace example.com/mid => "+mid.Path+"\n", "package app\nimport \"example.com/mid\"\nfunc Render() string { return mid.Result() }\n")
	project(t, s, "unrelated", "", "package unrelated\nfunc Other() string { return \"a\" }\n")
	check(t, s.Scan(ctx))
	edges, err := s.Store.Relationships(ctx, false)
	check(t, err)
	if len(edges) != 2 {
		t.Fatalf("want 2 verified edges, got %#v", edges)
	}
	// A change to an unconsumed API must not emit a downstream warning.
	writeFile(t, filepath.Join(lib.Path, "main.go"), "package lib\nfunc Value() string { return \"v1\" }\nfunc Unused(n int) string { return \"b\" }\n")
	check(t, s.Scan(ctx))
	impacts, err := s.Store.Impacts(ctx, "", "")
	check(t, err)
	if len(impacts) != 0 {
		t.Fatalf("irrelevant API produced impacts: %#v", impacts)
	}
	// Removing an actually consumed API is confirmed for the direct consumer;
	// a transitive user is exposed, but never automatically declared broken.
	writeFile(t, filepath.Join(lib.Path, "main.go"), "package lib\nfunc Replacement() string { return \"v2\" }\nfunc Unused(n int) string { return \"b\" }\n")
	check(t, s.Scan(ctx))
	impacts, err = s.Store.Impacts(ctx, "", "")
	check(t, err)
	if len(impacts) != 2 {
		t.Fatalf("want direct + transitive, got %#v", impacts)
	}
	seen := map[string]domain.Impact{}
	for _, i := range impacts {
		seen[i.Affected] = i
	}
	if seen[mid.ID].Severity != "confirmed" {
		t.Fatal("removed consumed API must be confirmed")
	}
	if seen[app.ID].Severity != "possible" || len(seen[app.ID].Path) != 3 {
		t.Fatal("transitive exposure must remain possible with a full path")
	}
	check(t, s.Scan(ctx))
	again, err := s.Store.Impacts(ctx, "", "")
	check(t, err)
	if len(again) != 2 {
		t.Fatal("repeated scan duplicated impacts")
	}
	check(t, s.Store.SetImpactStatus(ctx, impacts[0].ID, "acknowledged"))
	c, err := s.GetContext(ctx, impacts[0].Affected, "")
	check(t, err)
	if len(c.Impacts) != 0 {
		t.Fatal("acknowledged impact should leave open context")
	}
}
func TestAssumptionLedgerSupersedesWithoutErasingHistory(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	a := project(t, s, "provider", "", "package provider\nfunc Ready() {}\n")
	b := project(t, s, "consumer", "", "package consumer\nfunc Ready() {}\n")
	check(t, s.Scan(ctx))
	old, err := s.Record(ctx, a.ID, "compatibility", "API remains compatible until v2", "assumption", "docs/compatibility.md:1", "")
	check(t, err)
	check(t, s.DependOnClaim(ctx, b.ID, old.ID, "Feature relies on v1 payloads"))
	_, err = s.Record(ctx, a.ID, "compatibility", "v1 is no longer supported", "assumption", "release v2", "")
	if err == nil {
		t.Fatal("superseding needs expected previous version")
	}
	next, err := s.Record(ctx, a.ID, "compatibility", "v1 is no longer supported", "assumption", "release v2", old.ID)
	check(t, err)
	if next.Previous != old.ID {
		t.Fatal("missing revision chain")
	}
	claims, err := s.Store.Claims(ctx, a.ID, true)
	check(t, err)
	if len(claims) != 2 {
		t.Fatalf("history: %#v", claims)
	}
	statuses := map[string]string{}
	for _, c := range claims {
		statuses[c.ID] = c.Status
	}
	if statuses[old.ID] != "superseded" || statuses[next.ID] != "active" {
		t.Fatal(statuses)
	}
	impacts, err := s.Store.Impacts(ctx, a.ID, "open")
	check(t, err)
	if len(impacts) != 1 || impacts[0].Affected != b.ID {
		t.Fatalf("missing assumption alert: %#v", impacts)
	}
	if err = s.DependOnClaim(ctx, b.ID, old.ID, "stale reference"); err == nil {
		t.Fatal("must reject stale claim dependencies")
	}
}
func TestCaptureCursorReplayPartialAndSecretBoundary(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	p := project(t, s, "capture-test", "", "package capturetest\nfunc Ready() {}\n")
	check(t, s.Scan(ctx))
	path := filepath.Join(s.Config.Capture.CodexHome, "sessions", "2026", "session.jsonl")
	header := domain.JSON(map[string]any{"type": "session_meta", "payload": map[string]string{"id": "session-1", "cwd": p.Path}}) + "\n"
	message := domain.JSON(map[string]any{"type": "response_item", "payload": map[string]any{"type": "message", "role": "user", "content": []map[string]string{{"type": "input_text", "text": "Use sk-testsecret1234567890123456789 and assume compatibility"}}}}) + "\n"
	writeFile(t, path, header+message+`{"type":"event_msg","payload":{"type":"task_complete"}}`)
	check(t, s.Poll(ctx, true))
	sessions, err := s.Store.Sessions(ctx, p.ID)
	check(t, err)
	if len(sessions) != 1 || sessions[0].Cursor != int64(len(header+message)) {
		t.Fatalf("cursor advanced over partial line: %#v", sessions)
	}
	check(t, s.Drain(ctx, 100))
	mem, err := s.Store.Memories(ctx, p.ID)
	check(t, err)
	if len(mem) != 1 || strings.Contains(mem[0].Text, "sk-testsecret") {
		t.Fatalf("secret persisted or memory missing: %#v", mem)
	}
	check(t, s.Poll(ctx, true))
	check(t, s.Drain(ctx, 100))
	again, err := s.Store.Memories(ctx, p.ID)
	check(t, err)
	if len(again) != 1 {
		t.Fatal("replay duplicated extraction")
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	check(t, err)
	_, err = f.WriteString("\n")
	check(t, err)
	check(t, f.Close())
	check(t, s.Poll(ctx, false))
	sessions, err = s.Store.Sessions(ctx, p.ID)
	check(t, err)
	if sessions[0].Status != "turn_completed" {
		t.Fatal("completion not recognized")
	}
	// Unregistered projects never create a session or a model job.
	other := filepath.Join(s.Config.Capture.CodexHome, "sessions", "other.jsonl")
	writeFile(t, other, domain.JSON(map[string]any{"type": "session_meta", "payload": map[string]string{"id": "other", "cwd": t.TempDir()}})+"\n"+message)
	check(t, s.Poll(ctx, true))
	var n int
	check(t, s.Store.Row(ctx, `SELECT COUNT(*) FROM sessions`).Scan(&n))
	if n != 1 {
		t.Fatal("unregistered transcript was captured")
	}
}

func TestMalformedProjectDoesNotBlockOthersAndKeepsGraph(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	lib := project(t, s, "provider", "", "package provider\nfunc Used() string { return \"v1\" }\n")
	consumer := project(t, s, "consumer", "require example.com/provider v0.0.0\nreplace example.com/provider => "+lib.Path+"\n", "package consumer\nimport \"example.com/provider\"\nfunc Feature() string { return provider.Used() }\n")
	broken := project(t, s, "broken", "", "package broken\nfunc Fine() {}\n")
	check(t, s.Scan(ctx))
	old, err := s.Store.Snapshot(ctx, broken.ID)
	check(t, err)
	writeFile(t, filepath.Join(broken.Path, "main.go"), "package broken\nfunc (")
	writeFile(t, filepath.Join(lib.Path, "main.go"), "package provider\nfunc New() string { return \"v2\" }\n")
	if err = s.Scan(ctx); err == nil {
		t.Fatal("malformed source was not reported")
	}
	unchanged, err := s.Store.Snapshot(ctx, broken.ID)
	check(t, err)
	if unchanged.Fingerprint != old.Fingerprint {
		t.Fatal("bad parse replaced baseline")
	}
	impacts, err := s.Store.Impacts(ctx, consumer.ID, "open")
	check(t, err)
	if len(impacts) != 1 {
		t.Fatal("unrelated parse failure blocked valid impact", impacts)
	}
}

type fakeProvider struct {
	extraction ai.Extraction
	assessment ai.Assessment
}

func (f *fakeProvider) Extract(context.Context, string) (ai.Extraction, error) {
	return f.extraction, nil
}
func (f *fakeProvider) Embed(context.Context, string) ([]float64, error) { return []float64{1, 0}, nil }
func (f *fakeProvider) Assess(context.Context, string) (ai.Assessment, error) {
	return f.assessment, nil
}
func (f *fakeProvider) EmbeddingModel() string { return "fixture:2" }

func TestInferredConflictKeepsActiveClaimAndDoesNotTraverseCandidates(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	a := project(t, s, "source", "", "package source\nfunc Fine() {}\n")
	b := project(t, s, "dependent", "", "package dependent\nfunc Fine() {}\n")
	unrelated := project(t, s, "unrelated", "", "package unrelated\nfunc Fine() {}\n")
	a.CloudAllowed = true
	check(t, s.Store.PutProject(ctx, a))
	check(t, s.Scan(ctx))
	old, err := s.Record(ctx, a.ID, "compatibility", "v1 remains supported", "assumption", "old release notes", "")
	check(t, err)
	check(t, s.DependOnClaim(ctx, b.ID, old.ID, "uses the v1 contract"))
	s.AI = &fakeProvider{extraction: ai.Extraction{Summary: "v1 is removed", Claims: []ai.ExtractedClaim{{Key: "compatibility", Text: "v1 is no longer supported", Kind: "assumption", Quote: "v1 is removed"}, {Key: "invented", Text: "Never happened", Kind: "decision", Quote: "no supporting evidence"}}}, assessment: ai.Assessment{Verdict: "confirmed", Reason: "Old and new claims conflict", Verification: "Test v2 migration"}}
	e := domain.Evidence{ID: "new-evidence", Project: a.ID, Kind: "transcript", Text: "Release note: v1 is removed"}
	check(t, s.Store.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.PutEvidence(ctx, e); err != nil {
			return err
		}
		return tx.PutRelationship(ctx, domain.Relationship{ID: "candidate", From: unrelated.ID, To: old.Entity, Type: "ASSUMES", Source: "llm_candidate", Confidence: .99, APIs: []string{}})
	}))
	check(t, s.Extract(ctx, e.ID))
	id := domain.ID("extracted-claim", e.ID, "compatibility", "v1 is no longer supported")
	proposed, err := s.Store.Claim(ctx, id)
	check(t, err)
	if proposed.Previous != old.ID || proposed.Status != "proposed" {
		t.Fatal(proposed)
	}
	check(t, s.ReviewClaim(ctx, id))
	active, err := s.Store.Claim(ctx, old.ID)
	check(t, err)
	if active.Status != "active" {
		t.Fatal("model overwrote explicit assertion")
	}
	proposed, err = s.Store.Claim(ctx, id)
	check(t, err)
	if proposed.Classification != "contradiction" {
		t.Fatal(proposed)
	}
	impacts, err := s.Store.Impacts(ctx, "", "open")
	check(t, err)
	if len(impacts) != 1 || impacts[0].Affected != b.ID || impacts[0].Severity != "possible" {
		t.Fatalf("candidate leaked into propagation or inferred conflict became confirmed: %#v", impacts)
	}
	claims, err := s.Store.Claims(ctx, a.ID, true)
	check(t, err)
	if len(claims) != 2 {
		t.Fatal("unsupported claim persisted", claims)
	}
}

func TestEmbeddingSimilarityCreatesCandidateWithoutDependencyIdentity(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	a := project(t, s, "first", "", "package first\nfunc Fine() {}\n")
	b := project(t, s, "second", "", "package second\nfunc Fine() {}\n")
	a.CloudAllowed = true
	check(t, s.Store.PutProject(ctx, a))
	s.AI = &fakeProvider{extraction: ai.Extraction{Summary: "Shared token contract"}}
	check(t, s.Store.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.PutMemory(ctx, domain.Memory{ID: "existing", Project: b.ID, Text: "Shared token contract", Model: "fixture:2", Embedding: []float64{1, 0}}); err != nil {
			return err
		}
		return tx.PutEvidence(ctx, domain.Evidence{ID: "new", Project: a.ID, Text: "Shared token contract", Kind: "transcript"})
	}))
	check(t, s.Extract(ctx, "new"))
	edges, err := s.Store.Relationships(ctx, false)
	check(t, err)
	if len(edges) != 1 || edges[0].Source != "embedding_candidate" || edges[0].Type != "RELATED_TO" {
		t.Fatalf("semantic match asserted a dependency: %#v", edges)
	}
}
