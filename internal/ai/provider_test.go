package ai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jakecyr/stalemate/internal/config"
	"github.com/jakecyr/stalemate/internal/privacy"
	"github.com/jakecyr/stalemate/internal/store"
)

func provider(t *testing.T, handler http.HandlerFunc) *OpenAI {
	t.Helper()
	t.Setenv("OPENAI_API_KEY", "fixture-key-never-print")
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	s, err := store.Open(context.Background(), "sqlite", filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	c := config.Default().AI
	c.Provider = "openai"
	c.BaseURL = server.URL
	c.Dimensions = 3
	p, err := New(c, s, privacy.New(nil))
	if err != nil {
		t.Fatal(err)
	}
	return p.(*OpenAI)
}
func TestStructuredOutputCacheAndRedaction(t *testing.T) {
	var calls atomic.Int32
	p := provider(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer fixture-key-never-print" {
			t.Error("missing auth")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if strings.Contains(body["input"].(string), "fixture-key-never-print") {
			t.Error("credential sent as evidence")
		}
		if body["store"] != false {
			t.Error("Responses storage must be disabled")
		}
		text, _ := json.Marshal(Extraction{Summary: "Uses a stable API", Claims: []ExtractedClaim{}, Links: []Link{}})
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "completed", "usage": map[string]int{"input_tokens": 100, "output_tokens": 20}, "output": []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": string(text)}}}}})
	})
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		x, err := p.Extract(ctx, "Evidence fixture-key-never-print")
		if err != nil {
			t.Fatal(err)
		}
		if x.Summary != "Uses a stable API" {
			t.Fatal(x)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("cache made %d requests", calls.Load())
	}
	u, err := p.Store.Usage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if u.Calls != 1 || u.Reserved != 0 || u.Input != 100 || u.Output != 20 {
		t.Fatal(u)
	}
}
func TestBudgetRefusesBeforeNetworkAndFailureKeepsReservation(t *testing.T) {
	var calls atomic.Int32
	p := provider(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(500)
		_, _ = w.Write([]byte("fixture-key-never-print"))
	})
	p.Config.BudgetUSD = 0.0000001
	_, err := p.Extract(context.Background(), "small evidence")
	if !errors.Is(err, store.ErrBudget) || calls.Load() != 0 {
		t.Fatalf("budget did not stop request: %v", err)
	}
	p.Config.BudgetUSD = 20
	_, err = p.Extract(context.Background(), "small evidence")
	if err == nil || strings.Contains(err.Error(), "fixture-key-never-print") {
		t.Fatal("HTTP error leaked provider response", err)
	}
	u, err := p.Store.Usage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if u.Reserved <= 0 {
		t.Fatal("uncertain HTTP call released reservation")
	}
}
func TestEmbeddingValidationAndUsage(t *testing.T) {
	p := provider(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"index": 0, "embedding": []float64{1, 0, 0}}}, "usage": map[string]int{"prompt_tokens": 7, "total_tokens": 7}})
	})
	v, err := p.Embed(context.Background(), "stable API")
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 3 {
		t.Fatal(v)
	}
	p.Config.Dimensions = 4
	if _, err = p.Embed(context.Background(), "stable API"); err == nil {
		t.Fatal("accepted incompatible vector")
	}
}
func TestRefusalAndIncompleteNeverBecomeKnowledge(t *testing.T) {
	for _, status := range []string{"incomplete", "failed"} {
		t.Run(status, func(t *testing.T) {
			p := provider(t, func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "usage": map[string]int{"input_tokens": 2, "output_tokens": 1}, "output": []any{}})
			})
			if _, err := p.Extract(context.Background(), "evidence"); err == nil {
				t.Fatal("accepted incomplete output")
			}
		})
	}
}
