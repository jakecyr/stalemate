package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigUsesFileRelativePathsAndEnvironmentSecrets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"data_dir":"data","capture":{"codex_home":"codex"},"database":{"dsn_env":"FIXTURE_DATABASE_URL"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FIXTURE_DATABASE_URL", "db/fixture.sqlite")
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.DataDir != filepath.Join(dir, "data") || c.Database.DSN != filepath.Join(dir, "db", "fixture.sqlite") || c.Capture.CodexHome != filepath.Join(dir, "codex") {
		t.Fatal("relative paths depended on invoking cwd", c)
	}
}
func TestNonLoopbackRequiresTokenAndCleartextProviderIsLocalOnly(t *testing.T) {
	c := Default()
	c.HTTP.Listen = "0.0.0.0:7433"
	c.HTTP.TokenEnv = "FIXTURE_API_TOKEN"
	t.Setenv(c.HTTP.TokenEnv, "")
	if c.Validate() == nil {
		t.Fatal("public listener without auth")
	}
	t.Setenv(c.HTTP.TokenEnv, "fixture-token")
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.AI.BaseURL = "http://remote.example/v1"
	if c.Validate() == nil {
		t.Fatal("remote plaintext accepted")
	}
	c.AI.BaseURL = "http://127.0.0.1:8080/v1"
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}
func TestLiteralEnvFileDoesNotExecuteAndExistingEnvironmentWins(t *testing.T) {
	t.Setenv("FIXTURE_EXISTING", "keep")
	dir := t.TempDir()
	path := filepath.Join(dir, "env")
	if err := os.WriteFile(path, []byte("FIXTURE_EXISTING=replace\nFIXTURE_LITERAL='$(touch should-never-exist)'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Unsetenv("FIXTURE_LITERAL") })
	if err := LoadEnv(path); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("FIXTURE_EXISTING") != "keep" || os.Getenv("FIXTURE_LITERAL") != "$(touch should-never-exist)" {
		t.Fatal("env changed or evaluated")
	}
}
func TestUnknownFieldsAndTrailingObjectsAreRejected(t *testing.T) {
	for _, text := range []string{`{"unknown":true}`, `{} {}`} {
		p := filepath.Join(t.TempDir(), "bad.json")
		if err := os.WriteFile(p, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(p); err == nil {
			t.Fatal("accepted invalid config", text)
		}
	}
}
