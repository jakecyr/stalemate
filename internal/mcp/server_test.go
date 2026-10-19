package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jakecyr/stalemate/internal/ai"
	"github.com/jakecyr/stalemate/internal/config"
	"github.com/jakecyr/stalemate/internal/privacy"
	"github.com/jakecyr/stalemate/internal/service"
	"github.com/jakecyr/stalemate/internal/store"
)

func TestStdioNegotiationNotificationsToolsAndErrors(t *testing.T) {
	c := config.Default()
	db, err := store.Open(context.Background(), "sqlite", filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &Server{Service: service.New(c, db, ai.Disabled{}, privacy.New(nil))}
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"context_get","arguments":{"project":"missing"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"bogus"}`,
		`not json`,
	}, "\n") + "\n"
	var out bytes.Buffer
	if err = s.Serve(context.Background(), strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 5 {
		t.Fatalf("notifications must not emit a response: %s", out.String())
	}
	for _, l := range lines {
		if !json.Valid([]byte(l)) {
			t.Fatal("non-JSON stdout")
		}
	}
	if !strings.Contains(lines[0], `"2025-03-26"`) || !strings.Contains(lines[1], "context_record") || !strings.Contains(lines[2], `"isError":true`) || !strings.Contains(lines[3], "-32601") || !strings.Contains(lines[4], "-32700") {
		t.Fatal(out.String())
	}
}
