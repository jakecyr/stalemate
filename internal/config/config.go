// Package config defines explicit local defaults and environment-based secrets.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	DataDir       string        `json:"data_dir"`
	Database      Database      `json:"database"`
	HTTP          HTTP          `json:"http"`
	Capture       Capture       `json:"capture"`
	AI            AI            `json:"ai"`
	Notifications Notifications `json:"notifications"`
	Automation    Automation    `json:"automation"`
}
type Database struct {
	Driver string `json:"driver"`
	DSN    string `json:"dsn"`
	DSNEnv string `json:"dsn_env"`
}
type HTTP struct {
	Listen   string `json:"listen"`
	TokenEnv string `json:"token_env"`
}
type Capture struct {
	CodexHome   string   `json:"codex_home"`
	PollSeconds int      `json:"poll_seconds"`
	IdleSeconds int      `json:"idle_seconds"`
	MaxBytes    int      `json:"max_bytes"`
	Exclude     []string `json:"exclude"`
}
type AI struct {
	Provider                     string  `json:"provider"`
	BaseURL                      string  `json:"base_url"`
	APIKeyEnv                    string  `json:"api_key_env"`
	ExtractionModel              string  `json:"extraction_model"`
	ReasoningModel               string  `json:"reasoning_model"`
	EmbeddingModel               string  `json:"embedding_model"`
	ReasoningEffort              string  `json:"reasoning_effort"`
	Dimensions                   int     `json:"dimensions"`
	BudgetUSD                    float64 `json:"budget_usd"`
	InputUSDPerMillion           float64 `json:"input_usd_per_million"`
	OutputUSDPerMillion          float64 `json:"output_usd_per_million"`
	ReasoningInputUSDPerMillion  float64 `json:"reasoning_input_usd_per_million"`
	ReasoningOutputUSDPerMillion float64 `json:"reasoning_output_usd_per_million"`
	EmbeddingUSDPerMillion       float64 `json:"embedding_usd_per_million"`
	MaxOutputTokens              int     `json:"max_output_tokens"`
	TimeoutSeconds               int     `json:"timeout_seconds"`
}
type Notifications struct {
	WebhookURL string `json:"webhook_url"`
	SecretEnv  string `json:"secret_env"`
	Desktop    bool   `json:"desktop"`
}
type Automation struct {
	Mode           string   `json:"mode"`
	CodexBinary    string   `json:"codex_binary"`
	Model          string   `json:"model"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	MaxRuns        int      `json:"max_runs"`
	PublishPR      bool     `json:"publish_pr"`
	TestCommand    []string `json:"test_command"`
}

func Default() Config {
	home, _ := os.UserHomeDir()
	ch := os.Getenv("CODEX_HOME")
	if ch == "" {
		ch = filepath.Join(home, ".codex")
	}
	return Config{
		DataDir:    filepath.Join(home, ".local", "share", "stalemate"),
		Database:   Database{Driver: "sqlite", DSNEnv: "STALEMATE_DATABASE_URL"},
		HTTP:       HTTP{Listen: "127.0.0.1:7433", TokenEnv: "STALEMATE_API_TOKEN"},
		Capture:    Capture{CodexHome: ch, PollSeconds: 15, IdleSeconds: 600, MaxBytes: 48000, Exclude: []string{".env*", ".meb", "*.pem", "*.key", "*credentials*", "*secrets*", "auth.json", ".npmrc", ".pypirc", ".netrc", "id_rsa*", "id_ed25519*", "*.p12", "*.pfx"}},
		AI:         AI{Provider: "disabled", BaseURL: "https://api.openai.com/v1", APIKeyEnv: "OPENAI_API_KEY", ExtractionModel: "gpt-6-luna", ReasoningModel: "gpt-6.1-sol", EmbeddingModel: "text-embedding-3-small", ReasoningEffort: "low", Dimensions: 512, BudgetUSD: 20, InputUSDPerMillion: 0.10, OutputUSDPerMillion: 0.50, ReasoningInputUSDPerMillion: 2, ReasoningOutputUSDPerMillion: 10, EmbeddingUSDPerMillion: 0.02, MaxOutputTokens: 3000, TimeoutSeconds: 90},
		Automation: Automation{Mode: "off", CodexBinary: "codex", Model: "gpt-6.1-sol", TimeoutSeconds: 600, MaxRuns: 3, TestCommand: []string{"go", "test", "./..."}},
	}
}
func Load(path string) (Config, error) {
	c := Default()
	base, err := os.Getwd()
	if err != nil {
		return c, err
	}
	if path != "" {
		abs, e := filepath.Abs(path)
		if e != nil {
			return c, e
		}
		base = filepath.Dir(abs)
		f, err := os.Open(path)
		if err != nil {
			return c, err
		}
		defer f.Close()
		d := json.NewDecoder(io.LimitReader(f, 1<<20))
		d.DisallowUnknownFields()
		if err = d.Decode(&c); err != nil {
			return c, fmt.Errorf("config: %w", err)
		}
		var extra any
		if err = d.Decode(&extra); err != io.EOF {
			return c, errors.New("config must contain one JSON object")
		}
	}
	if v := os.Getenv("STALEMATE_DATA_DIR"); v != "" {
		c.DataDir = v
	}
	for key, target := range map[string]*string{
		"STALEMATE_LISTEN": &c.HTTP.Listen, "STALEMATE_CODEX_HOME": &c.Capture.CodexHome,
		"STALEMATE_DATABASE_DRIVER": &c.Database.Driver, "STALEMATE_AI_PROVIDER": &c.AI.Provider,
		"STALEMATE_AI_BASE_URL": &c.AI.BaseURL, "STALEMATE_EXTRACTION_MODEL": &c.AI.ExtractionModel,
		"STALEMATE_REASONING_MODEL": &c.AI.ReasoningModel, "STALEMATE_EMBEDDING_MODEL": &c.AI.EmbeddingModel,
	} {
		if v := os.Getenv(key); v != "" {
			*target = v
		}
	}
	resolve := func(p string) string {
		if strings.HasPrefix(p, "~/") {
			home, _ := os.UserHomeDir()
			p = filepath.Join(home, strings.TrimPrefix(p, "~/"))
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(base, p)
		}
		return filepath.Clean(p)
	}
	if c.DataDir == "" || c.Capture.CodexHome == "" {
		return c, errors.New("data_dir and capture.codex_home must not be empty")
	}
	c.DataDir = resolve(c.DataDir)
	c.Capture.CodexHome = resolve(c.Capture.CodexHome)
	if v := os.Getenv(c.Database.DSNEnv); v != "" {
		c.Database.DSN = v
	}
	if c.Database.DSN == "" {
		if c.Database.Driver == "postgres" {
			return c, errors.New("PostgreSQL requires database.dsn or its configured environment variable")
		}
		c.Database.DSN = filepath.Join(c.DataDir, "stalemate.sqlite")
	}
	if c.Database.Driver == "sqlite" && c.Database.DSN != ":memory:" && !strings.HasPrefix(c.Database.DSN, "file:") {
		c.Database.DSN = resolve(c.Database.DSN)
	}
	if v := os.Getenv("STALEMATE_BUDGET_USD"); v != "" {
		n, e := strconv.ParseFloat(v, 64)
		if e != nil || math.IsNaN(n) || math.IsInf(n, 0) {
			return c, errors.New("invalid STALEMATE_BUDGET_USD")
		}
		c.AI.BudgetUSD = n
	}
	return c, c.Validate()
}
func (c Config) Validate() error {
	if c.Database.Driver != "sqlite" && c.Database.Driver != "postgres" {
		return errors.New("database.driver must be sqlite or postgres")
	}
	if c.AI.Provider != "disabled" && c.AI.Provider != "openai" {
		return errors.New("ai.provider must be disabled or openai (including compatible endpoints)")
	}
	if c.Capture.PollSeconds < 1 || c.Capture.IdleSeconds < 0 || c.Capture.MaxBytes < 1024 || c.Capture.MaxBytes > 1000000 {
		return errors.New("invalid capture bounds")
	}
	if c.AI.BudgetUSD < 0 || c.AI.MaxOutputTokens < 256 || c.AI.MaxOutputTokens > 16000 || c.AI.TimeoutSeconds < 1 || c.AI.Dimensions < 1 {
		return errors.New("invalid AI budget or request bounds")
	}
	if c.AI.InputUSDPerMillion < 0 || c.AI.OutputUSDPerMillion <= 0 || c.AI.ReasoningInputUSDPerMillion < 0 || c.AI.ReasoningOutputUSDPerMillion <= 0 || c.AI.EmbeddingUSDPerMillion <= 0 {
		return errors.New("AI price estimates must be positive")
	}
	if c.Automation.Mode != "off" && c.Automation.Mode != "manual" && c.Automation.Mode != "automatic" {
		return errors.New("automation.mode must be off, manual or automatic")
	}
	if c.Automation.TimeoutSeconds < 1 || c.Automation.MaxRuns < 1 {
		return errors.New("invalid automation bounds")
	}
	if c.Automation.PublishPR && len(c.Automation.TestCommand) == 0 {
		return errors.New("PR publishing requires a test command")
	}
	host, _, err := net.SplitHostPort(c.HTTP.Listen)
	if err != nil {
		return fmt.Errorf("http.listen: %w", err)
	}
	ip := net.ParseIP(host)
	if (ip == nil || !ip.IsLoopback()) && os.Getenv(c.HTTP.TokenEnv) == "" {
		return errors.New("non-loopback HTTP requires an API token")
	}
	for _, raw := range []string{c.AI.BaseURL, c.Notifications.WebhookURL} {
		if raw == "" {
			continue
		}
		u, e := url.Parse(raw)
		if e != nil || u.Host == "" || u.User != nil {
			return errors.New("invalid endpoint URL")
		}
		if u.Scheme != "https" && (u.Scheme != "http" || !isLoopback(u.Hostname())) {
			return errors.New("endpoints require HTTPS except loopback")
		}
	}
	return nil
}
func isLoopback(s string) bool {
	ip := net.ParseIP(s)
	return s == "localhost" || (ip != nil && ip.IsLoopback())
}
func (c Config) PollInterval() time.Duration {
	return time.Duration(c.Capture.PollSeconds) * time.Second
}

// LoadEnv reads literal values only; it never evaluates shell expressions.
func LoadEnv(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if strings.ContainsAny(k, " \t\r\n") || k == "" {
			return errors.New("invalid environment variable name")
		}
		if len(v) > 1 && ((v[0] == '\'' && v[len(v)-1] == '\'') || (v[0] == '"' && v[len(v)-1] == '"')) {
			v = v[1 : len(v)-1]
		}
		if _, ok := os.LookupEnv(k); !ok {
			if err = os.Setenv(k, v); err != nil {
				return err
			}
		}
	}
	return nil
}
