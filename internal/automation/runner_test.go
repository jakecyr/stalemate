package automation

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jakecyr/stalemate/internal/ai"
	"github.com/jakecyr/stalemate/internal/config"
	"github.com/jakecyr/stalemate/internal/domain"
	"github.com/jakecyr/stalemate/internal/privacy"
	"github.com/jakecyr/stalemate/internal/process"
	"github.com/jakecyr/stalemate/internal/repo"
	"github.com/jakecyr/stalemate/internal/service"
	"github.com/jakecyr/stalemate/internal/store"
)

func TestRelocatedWorktreeKeepsRelativeReplacementsWithoutManifestEdits(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	lib := filepath.Join(root, "lib")
	app := filepath.Join(root, "app")
	wt := filepath.Join(root, "elsewhere", "worktree")
	c := config.Default()
	db, err := store.Open(ctx, "sqlite", filepath.Join(root, "data", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := service.New(c, db, ai.Disabled{}, privacy.New(c.Capture.Exclude))
	write := func(root, name, text string) {
		t.Helper()
		if err := os.MkdirAll(root, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(lib, "go.mod", "module example.com/lib\n\ngo 1.25.3\n")
	write(lib, "lib.go", "package lib\nfunc Value() string{return \"ok\"}\n")
	manifest := "module example.com/app\n\ngo 1.25.3\nrequire example.com/lib v0.0.0\nreplace example.com/lib => ../lib\n"
	code := "package app\nimport \"example.com/lib\"\nfunc Value()string{return lib.Value()}\n"
	for _, p := range []string{app, wt} {
		write(p, "go.mod", manifest)
		write(p, "app.go", code)
	}
	if _, err = repo.Git(ctx, app, "init"); err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	env, err := verificationEnvironment(ctx, app, wt, tmp, s)
	if err != nil {
		t.Fatal(err)
	}
	output, err := process.RunEnv(ctx, wt, []string{"go", "test", "./..."}, nil, nil, env)
	if err != nil {
		t.Fatalf("relocated local dependency failed: %v\n%s", err, output)
	}
	b, err := os.ReadFile(filepath.Join(wt, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != manifest {
		t.Fatal("verification mutated go.mod")
	}
	found := false
	for _, e := range env {
		if strings.HasPrefix(e, "GOWORK=") {
			found = true
		}
	}
	if !found {
		t.Fatal("missing workspace overlay")
	}
}
func TestRemediationIsDisabledByDefault(t *testing.T) {
	s := &service.Service{Config: config.Default()}
	if _, err := (&Runner{Service: s}).Remediate(context.Background(), "unused"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatal(err)
	}
}

func TestPublishRequiresPassingTestsAndPushesOnlyIsolatedBranch(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	app := filepath.Join(root, "consumer")
	bin := filepath.Join(root, "tools")
	for _, p := range []string{app, bin} {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, text string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, []byte(text), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(app, "go.mod"), "module example.com/consumer\n\ngo 1.25.3\n", 0600)
	original := "package consumer\nfunc Value() int{return 1}\n"
	write(filepath.Join(app, "consumer.go"), original, 0600)
	write(filepath.Join(app, "consumer_test.go"), "package consumer\nimport \"testing\"\nfunc TestValue(t *testing.T){if Value()!=2{t.Fatal(\"outdated dependency behavior\")}}\n", 0600)
	git := func(path string, args ...string) string {
		t.Helper()
		v, err := repo.Git(ctx, path, args...)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	git(app, "init", "-b", "master")
	git(app, "config", "user.name", "Fixture")
	git(app, "config", "user.email", "fixture@localhost")
	git(app, "add", ".")
	git(app, "commit", "-m", "fixture")
	remote := filepath.Join(root, "remote.git")
	if _, err := repo.Git(ctx, root, "init", "--bare", remote); err != nil {
		t.Fatal(err)
	}
	git(app, "remote", "add", "origin", remote)
	marker := filepath.Join(root, "gh-calls")
	write(filepath.Join(bin, "gh"), "#!/bin/sh\ncase \"$*\" in *--draft*) ;; *) exit 9;; esac\nprintf 'called\\n' >> '"+marker+"'\nprintf 'https://github.com/fixture/stalemate/pull/1\\n'\n", 0700)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	agent := filepath.Join(bin, "codex-fixture")
	writeAgent := func(value string) {
		write(agent, "#!/bin/sh\nprintf 'package consumer\\nfunc Value() int{return "+value+"}\\n' > consumer.go\nprintf 'agent completed\\n'\n", 0700)
	}
	writeAgent("2")
	c := config.Default()
	c.DataDir = filepath.Join(root, "data")
	c.Automation.Mode = "manual"
	c.Automation.CodexBinary = agent
	c.Automation.PublishPR = true
	db, err := store.Open(ctx, "sqlite", filepath.Join(c.DataDir, "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := service.New(c, db, ai.Disabled{}, privacy.New(c.Capture.Exclude))
	p, err := s.Register(ctx, app, "consumer", true)
	if err != nil {
		t.Fatal(err)
	}
	impact := func(id string) {
		t.Helper()
		if err := db.Transaction(ctx, func(tx *store.Tx) error {
			if err := tx.PutEvidence(ctx, domain.Evidence{ID: "evidence-" + id, Project: p.ID, Text: "A dependency changed", Kind: "test"}); err != nil {
				return err
			}
			_, err := tx.PutImpact(ctx, domain.Impact{ID: id, Affected: p.ID, Changed: p.ID, Status: "open", Severity: "confirmed", Evidence: "evidence-" + id})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	impact("impact-success-123456789")
	r := &Runner{Service: s}
	run, err := r.Remediate(ctx, "impact-success-123456789")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "published" || run.PR != "https://github.com/fixture/stalemate/pull/1" {
		t.Fatal(run)
	}
	git(root, "--git-dir="+remote, "show-ref", "--verify", "refs/heads/"+run.Branch)
	b, err := os.ReadFile(filepath.Join(app, "consumer.go"))
	if err != nil || string(b) != original {
		t.Fatal("publisher modified original project")
	}
	// Repeating the request must return the same run, not invoke another agent.
	if repeated, err := r.Remediate(ctx, "impact-success-123456789"); err != nil || repeated.ID != run.ID {
		t.Fatal("remediation not idempotent", err)
	}
	writeAgent("3")
	impact("impact-failure-123456789")
	failed, err := r.Remediate(ctx, "impact-failure-123456789")
	if err == nil || failed.Status != "failed" {
		t.Fatal("failed verification was published", err)
	}
	calls, err := os.ReadFile(marker)
	if err != nil || string(calls) != "called\n" {
		t.Fatal("verification failure invoked PR creation")
	}
	if _, err = repo.Git(ctx, root, "--git-dir="+remote, "show-ref", "--verify", "refs/heads/"+failed.Branch); err == nil {
		t.Fatal("failed verification pushed a branch")
	}
}
