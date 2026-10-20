package install

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jakecyr/stalemate/internal/config"
)

func TestStatusRequiresMatchingGenerationAndAuthentication(t *testing.T) {
	c := config.Default()
	c.DataDir = t.TempDir()
	c.HTTP.TokenEnv = "FIXTURE_STALEMATE_TOKEN"
	t.Setenv(c.HTTP.TokenEnv, "private")
	instance := "new-generation"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer private" {
			w.WriteHeader(401)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"process_pid": 123, "process_instance": instance, "version": "fixture"})
	}))
	defer server.Close()
	s := State{PID: 123, Instance: "old-generation", URL: server.URL}
	if got := status(context.Background(), c, s); got.Running {
		t.Fatal("reused PID recognized as owned service")
	}
	s.Instance = instance
	if got := status(context.Background(), c, s); !got.Running || got.Version != "fixture" {
		t.Fatal("owned instance not recognized", got)
	}
	// An unrelated service is never signalled or its state erased.
	s.Instance = "old-generation"
	if err := save(statePath(c), s); err != nil {
		t.Fatal(err)
	}
	if err := Stop(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(statePath(c)); err != nil {
		t.Fatal("unmatched state removed", err)
	}
}

func TestSecretUpdatePreservesOtherSettingsAndRestrictsPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "environment")
	os.WriteFile(path, []byte("# deployment\nOTHER=value\nexport KEY=old\nKEY=duplicate\n"), 0644)
	if err := SetSecret(path, "KEY", "new-secret"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "OTHER=value") || strings.Count(string(b), "KEY=") != 1 || !strings.Contains(string(b), "KEY=new-secret") {
		t.Fatal("settings not preserved")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("secret readable by other users")
	}
	for _, value := range []string{"", "bad\nINJECTED=value", "bad\x00value"} {
		if SetSecret(path, "KEY", value) == nil {
			t.Fatal("invalid secret accepted")
		}
	}
}
