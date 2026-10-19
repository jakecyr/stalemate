package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jakecyr/stalemate/internal/ai"
	"github.com/jakecyr/stalemate/internal/config"
	"github.com/jakecyr/stalemate/internal/privacy"
	"github.com/jakecyr/stalemate/internal/service"
	"github.com/jakecyr/stalemate/internal/store"
)

func apiServer(t *testing.T) http.Handler {
	t.Helper()
	c := config.Default()
	db, err := store.Open(context.Background(), "sqlite", filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return (&Server{Service: service.New(c, db, ai.Disabled{}, privacy.New(c.Capture.Exclude))}).Handler()
}
func TestHTTPBoundaryAuthOriginHostAndJSON(t *testing.T) {
	t.Setenv("STALEMATE_API_TOKEN", "test-token")
	h := apiServer(t)
	for _, tc := range []struct {
		name, method, path, host, origin, token, content, body string
		want                                                   int
	}{
		{"dashboard", "GET", "/", "127.0.0.1:7433", "", "", "", "", 200},
		{"unauthenticated", "GET", "/api/status", "127.0.0.1:7433", "", "", "", "", 401},
		{"authenticated", "GET", "/api/status", "127.0.0.1:7433", "", "test-token", "", "", 200},
		{"dns rebinding", "GET", "/api/status", "evil.test:7433", "", "test-token", "", "", 403},
		{"cross origin", "POST", "/api/scan", "127.0.0.1:7433", "https://evil.test", "test-token", "application/json", "{}", 403},
		{"form csrf", "POST", "/api/scan", "127.0.0.1:7433", "", "test-token", "application/x-www-form-urlencoded", "", 415},
		{"valid queue", "POST", "/api/scan", "127.0.0.1:7433", "http://127.0.0.1:7433", "test-token", "application/json", "{}", 202},
		{"trailing json", "POST", "/api/scan", "127.0.0.1:7433", "", "test-token", "application/json", "{} {}", 400},
		{"unknown fields", "POST", "/api/scan", "127.0.0.1:7433", "", "test-token", "application/json", "{\"unsafe\":true}", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "http://127.0.0.1:7433"+tc.path, strings.NewReader(tc.body))
			r.Host = tc.host
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("Content-Type", tc.content)
			if tc.token != "" {
				r.Header.Set("Authorization", "Bearer "+tc.token)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("got %d want %d: %s", w.Code, tc.want, w.Body.String())
			}
			if tc.path != "/" && !json.Valid(w.Body.Bytes()) {
				t.Fatal("non-JSON API response")
			}
		})
	}
}
