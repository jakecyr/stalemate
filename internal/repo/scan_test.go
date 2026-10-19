package repo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jakecyr/stalemate/internal/domain"
	"github.com/jakecyr/stalemate/internal/privacy"
)

func fixture(t *testing.T, code string) string {
	t.Helper()
	p := t.TempDir()
	if _, err := Git(context.Background(), p, "init", "-b", "master"); err != nil {
		t.Fatal(err)
	}
	put(t, p, "go.mod", "module example.com/library\n\ngo 1.25.3\n")
	put(t, p, "library.go", code)
	return p
}
func put(t *testing.T, root, path, text string) {
	t.Helper()
	full := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}
func snap(t *testing.T, path string) domain.Snapshot {
	t.Helper()
	s, err := Build(context.Background(), path, privacy.New([]string{".env*", "*.key"}))
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func TestLocalHelperChangesReachOnlyTheirExportedCallers(t *testing.T) {
	p := fixture(t, "package library\nfunc Public() string { return helper() }\nfunc Other() string { return \"stable\" }\nfunc helper() string { return nested() }\nfunc nested() string { return \"a\" }\nfunc unused() string { return \"x\" }\n")
	before := snap(t, p)
	put(t, p, "library.go", "package library\nfunc Public() string { return helper() }\nfunc Other() string { return \"stable\" }\nfunc helper() string { return nested() }\nfunc nested() string { return \"b\" }\nfunc unused() string { return \"x\" }\n")
	after := snap(t, p)
	changes := Changes(before, after)
	if len(changes) != 1 || changes[0].API != "example.com/library.Public" || changes[0].Kind != "behavior_changed" {
		t.Fatalf("private helper impact missed or broadened: %#v", changes)
	}
	put(t, p, "library.go", "package library\nfunc Public() string { return helper() }\nfunc Other() string { return \"stable\" }\nfunc helper() string { return nested() }\nfunc nested() string { return \"b\" }\nfunc unused() string { return \"new\" }\n")
	if c := Changes(after, snap(t, p)); len(c) != 0 {
		t.Fatal("unused helper produced exported change", c)
	}
}
func TestCommentsAndParameterNamesAreNotSignatureBreaks(t *testing.T) {
	p := fixture(t, "package library\nfunc Hello(name string) string { return name }\n")
	old := snap(t, p)
	put(t, p, "library.go", "package library\n// Updated documentation.\nfunc Hello(name string) string { /* comment */ return name }\n")
	if c := Changes(old, snap(t, p)); len(c) != 0 {
		t.Fatal("comment change", c)
	}
	if funcSignature(mustFunc(t, "func(a string, b int) (value string)")) != funcSignature(mustFunc(t, "func(x string, y int) (result string)")) {
		t.Fatal("parameter names changed signature")
	}
}
func TestMethodExposureIsConservativeAndEmptyVectorsAreSafe(t *testing.T) {
	e := domain.Relationship{APIs: []string{"example.com/library.NewClient"}}
	c := []domain.Change{{API: "example.com/library.Client.Send", Kind: "removed"}}
	if len(Relevant(e, c)) != 1 {
		t.Fatal("factory-created receiver method exposure missed")
	}
	if len(Relevant(e, []domain.Change{{API: "example.com/library.Unused", Kind: "removed"}})) != 0 {
		t.Fatal("unrelated function exposed")
	}
}
func TestNPMManifestDependenciesAndImplementationExposure(t *testing.T) {
	p := t.TempDir()
	if _, err := Git(context.Background(), p, "init"); err != nil {
		t.Fatal(err)
	}
	put(t, p, "package.json", `{"name":"@test/library","dependencies":{"@test/other":"file:../other"}}`)
	put(t, p, "index.js", `export function value() { return 1; }`)
	old := snap(t, p)
	put(t, p, "index.js", `export function value() { return 2; }`)
	next := snap(t, p)
	if len(next.Dependencies) != 1 || next.Dependencies[0].Kind != "npm" {
		t.Fatal(next.Dependencies)
	}
	changes := Changes(old, next)
	if len(changes) != 1 || changes[0].Kind != "implementation_changed" {
		t.Fatal(changes)
	}
}
func TestSymlinksAndSecretFilesNeverEnterSnapshot(t *testing.T) {
	p := fixture(t, "package library\nconst APIKey = \"sk-fixturekey123456789012345\"\n")
	put(t, p, ".env", "SECRET=never-read")
	secret := filepath.Join(t.TempDir(), "outside.go")
	if err := os.WriteFile(secret, []byte("not valid code"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(p, "linked.go")); err != nil {
		t.Fatal(err)
	}
	s := snap(t, p)
	for _, file := range s.Files {
		if file == ".env" || file == "linked.go" {
			t.Fatal("excluded file captured", file)
		}
	}
	if s.APIs["example.com/library.APIKey"] != "=\"[REDACTED]\"" {
		t.Fatal("exported credential not redacted", s.APIs)
	}
}

func TestManifestSymlinkCannotReadOutsideRegisteredRoot(t *testing.T) {
	p := t.TempDir()
	if _, err := Git(context.Background(), p, "init"); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("unknown-private-value"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(p, "go.mod")); err != nil {
		t.Fatal(err)
	}
	_, err := Build(context.Background(), p, privacy.New(nil))
	if err == nil || strings.Contains(err.Error(), "unknown-private-value") {
		t.Fatal("manifest symlink crossed disclosure boundary", err)
	}
}

func TestTrackedDirectoryReplacedBySymlinkDoesNotEscapeRoot(t *testing.T) {
	p := fixture(t, "package library\nfunc Fine() {}\n")
	put(t, p, "nested/source.go", "package nested\nfunc Existing() {}\n")
	if _, err := Git(context.Background(), p, "add", "."); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(p, "nested")); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	put(t, outside, "source.go", "this outside content must not be parsed")
	if err := os.Symlink(outside, filepath.Join(p, "nested")); err != nil {
		t.Fatal(err)
	}
	s := snap(t, p)
	for _, file := range s.Files {
		if strings.HasPrefix(file, "nested/") {
			t.Fatal("tracked parent symlink escaped root")
		}
	}
}
