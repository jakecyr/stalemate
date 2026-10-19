package privacy

import (
	"strings"
	"testing"
)

func TestSecretRedactionAndPathBoundary(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "private-fixture-secret")
	f := New([]string{".env*", "*.key", "*credentials*"})
	text := f.Text("key private-fixture-secret sk-testlongtoken1234567890 password=hunter2 alice@example.com https://user:pass@example.com")
	for _, secret := range []string{"private-fixture-secret", "sk-testlongtoken", "hunter2", "alice@example.com", "user:pass"} {
		if strings.Contains(text, secret) {
			t.Fatal("unredacted secret", secret)
		}
	}
	for _, path := range []string{".env", "nested/.env.production", "x/service.key", ".git/config", "node_modules/a.js", "secrets/credentials.json"} {
		if f.Allowed(path) {
			t.Fatal("allowed", path)
		}
	}
	if !f.Allowed("internal/service.go") {
		t.Fatal("excluded code")
	}
	if Within("/repo/app", "/repo/application") {
		t.Fatal("path prefix confused for containment")
	}
}
