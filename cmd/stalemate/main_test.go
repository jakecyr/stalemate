package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitKeepsSecretsOutOfConfigurationAndRefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "stalemate.json")
	env := filepath.Join(dir, "env")
	if err := os.WriteFile(env, nil, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STALEMATE_DATA_DIR", filepath.Join(dir, "data"))
	t.Setenv("OPENAI_API_KEY", "fixture-secret-do-not-store")
	var out bytes.Buffer
	args := []string{"--config", cfg, "--env-file", env, "init"}
	if err := run(context.Background(), args, &out); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "fixture-secret") {
		t.Fatal("secret written to config")
	}
	var c map[string]any
	if err = json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	if c["ai"].(map[string]any)["provider"] != "openai" {
		t.Fatal("key-backed init not enabled")
	}
	if err = run(context.Background(), args, &out); err == nil {
		t.Fatal("init overwrote user configuration")
	}
	after, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b, after) {
		t.Fatal("existing config was modified")
	}
	// A key-configured install can inspect an empty ledger and persist hooks
	// without performing any hosted request or requiring a running daemon.
	out.Reset()
	if err = run(context.Background(), []string{"--config", cfg, "--env-file", env, "project", "list"}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "[]" {
		t.Fatal(out.String())
	}
}
func TestHelpAndVersionDoNotExposeConfiguration(t *testing.T) {
	for _, args := range [][]string{nil, {"--help"}, {"version"}} {
		var out bytes.Buffer
		if err := run(context.Background(), args, &out); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.ToLower(out.String()), "stalemate") {
			t.Fatal(out.String())
		}
	}
}
