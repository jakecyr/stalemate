// Package api exposes the same ledger through a local dashboard and JSON API.
package api

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jakecyr/stalemate/internal/domain"
	"github.com/jakecyr/stalemate/internal/service"
	"github.com/jakecyr/stalemate/internal/store"
)

//go:embed dashboard.html
var assets embed.FS

type Server struct{ Service *service.Service }

func (s *Server) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		b, _ := assets.ReadFile("dashboard.html")
		w.Write(b)
	})
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := s.Service.Store.DB.PingContext(r.Context()); err != nil {
			write(w, 503, map[string]string{"status": "unavailable"})
			return
		}
		write(w, 200, map[string]string{"status": "ok"})
	})
	m.Handle("/api/", s.authenticate(http.HandlerFunc(s.route)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		listenHost, _, _ := net.SplitHostPort(s.Service.Config.HTTP.Listen)
		ip := net.ParseIP(listenHost)
		if ip != nil && ip.IsLoopback() {
			candidate := net.ParseIP(host)
			if host != "localhost" && (candidate == nil || !candidate.IsLoopback()) {
				write(w, 403, map[string]string{"error": "untrusted Host header"})
				return
			}
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, e := url.Parse(origin)
			if e != nil || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") {
				write(w, 403, map[string]string{"error": "cross-origin requests are forbidden"})
				return
			}
		}
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			write(w, 403, map[string]string{"error": "cross-site requests are forbidden"})
			return
		}
		m.ServeHTTP(w, r)
	})
}
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := os.Getenv(s.Service.Config.HTTP.TokenEnv)
		if token != "" {
			given := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(given), []byte(token)) != 1 {
				write(w, 401, map[string]string{"error": "API token required"})
				return
			}
		}
		media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if r.Method != "GET" && media != "application/json" {
			write(w, 415, map[string]string{"error": "Content-Type must be application/json"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("expected one JSON object")
	}
	return nil
}
func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	svc := s.Service
	path := strings.TrimPrefix(r.URL.Path, "/api/")
	var result any
	var err error
	status := 200
	switch {
	case path == "status" && r.Method == "GET":
		var usage store.Usage
		usage, err = svc.Store.Usage(ctx)
		result = map[string]any{"usage": usage, "budget_usd": svc.Config.AI.BudgetUSD, "provider": svc.Config.AI.Provider, "embedding_model": svc.AI.EmbeddingModel(), "extraction_model": svc.Config.AI.ExtractionModel, "automation": svc.Config.Automation.Mode, "publish_pr": svc.Config.Automation.PublishPR}
	case path == "projects" && r.Method == "GET":
		result, err = svc.Store.Projects(ctx)
	case path == "projects" && r.Method == "POST":
		var p struct {
			Path  string `json:"path"`
			Name  string `json:"name"`
			Cloud bool   `json:"cloud_allowed"`
		}
		if err = decode(w, r, &p); err == nil {
			result, err = svc.Register(ctx, p.Path, p.Name, p.Cloud)
			status = 201
		}
	case path == "context" && r.Method == "GET":
		result, err = svc.GetContext(ctx, r.URL.Query().Get("project"), r.URL.Query().Get("task"))
	case path == "relationships" && r.Method == "GET":
		result, err = svc.Store.Relationships(ctx, r.URL.Query().Get("history") == "true")
	case path == "claims" && r.Method == "GET":
		result, err = svc.Store.Claims(ctx, r.URL.Query().Get("project"), r.URL.Query().Get("history") == "true")
	case path == "claims" && r.Method == "POST":
		var p struct {
			Project  string `json:"project"`
			Key      string `json:"key"`
			Text     string `json:"text"`
			Kind     string `json:"kind"`
			Evidence string `json:"evidence"`
			Previous string `json:"previous"`
		}
		if err = decode(w, r, &p); err == nil {
			result, err = svc.Record(ctx, p.Project, p.Key, p.Text, p.Kind, p.Evidence, p.Previous)
			status = 201
		}
	case path == "assumptions/depend" && r.Method == "POST":
		var p struct {
			Project  string `json:"project"`
			Claim    string `json:"claim"`
			Evidence string `json:"evidence"`
		}
		if err = decode(w, r, &p); err == nil {
			err = svc.DependOnClaim(ctx, p.Project, p.Claim, p.Evidence)
			result = map[string]bool{"ok": err == nil}
		}
	case path == "impacts" && r.Method == "GET":
		result, err = svc.Store.Impacts(ctx, r.URL.Query().Get("project"), r.URL.Query().Get("status"))
	case strings.HasPrefix(path, "impacts/") && r.Method == "POST":
		id := strings.TrimPrefix(path, "impacts/")
		var p struct {
			Status string `json:"status"`
		}
		if err = decode(w, r, &p); err == nil {
			err = svc.Store.SetImpactStatus(ctx, id, p.Status)
			if err == nil {
				result, err = svc.Store.Impact(ctx, id)
			}
		}
	case path == "search" && r.Method == "GET":
		project := r.URL.Query().Get("project")
		query := r.URL.Query().Get("q")
		if query == "" {
			err = errors.New("q is required")
			break
		}
		var vector []float64
		// Cross-project search remains local. A project query can be embedded only
		// after that project's disclosure setting has been checked.
		if project != "" {
			var p domain.Project
			p, err = svc.Store.Project(ctx, project)
			if err != nil {
				break
			}
			project = p.ID
			if p.CloudAllowed {
				vector, err = svc.AI.Embed(ctx, query)
				if err != nil {
					break
				}
			}
		}
		result, err = svc.Store.Search(ctx, project, query, svc.AI.EmbeddingModel(), vector, 20)
	case path == "jobs" && r.Method == "GET":
		result, err = svc.Store.Jobs(ctx)
	case path == "jobs/retry" && r.Method == "POST":
		var p struct {
			ID string `json:"id"`
		}
		if err = decode(w, r, &p); err == nil {
			err = svc.Store.RetryJob(ctx, p.ID)
			result = map[string]bool{"ok": err == nil}
		}
	case path == "runs" && r.Method == "GET":
		result, err = svc.Store.Runs(ctx)
	case path == "remediate" && r.Method == "POST":
		var p struct {
			Impact string `json:"impact"`
		}
		if err = decode(w, r, &p); err == nil {
			if svc.Config.Automation.Mode == "off" {
				err = errors.New("automation is disabled")
				break
			}
			if _, err = svc.Store.Impact(ctx, p.Impact); err == nil {
				err = svc.Store.Enqueue(ctx, "remediate", p.Impact, map[string]string{"impact": p.Impact}, 0)
				status = 202
				result = map[string]string{"status": "queued"}
			}
		}
	case path == "scan" && r.Method == "POST":
		var p struct{}
		if err = decode(w, r, &p); err == nil {
			err = svc.Store.Enqueue(ctx, "scan", fmt.Sprint(domain.Now()), map[string]string{}, 0)
			status = 202
			result = map[string]string{"status": "queued"}
		}
	case strings.HasPrefix(path, "evidence/") && r.Method == "GET":
		result, err = svc.Store.Evidence(ctx, strings.TrimPrefix(path, "evidence/"))
	case path == "sessions" && r.Method == "GET":
		result, err = svc.Store.Sessions(ctx, r.URL.Query().Get("project"))
	default:
		write(w, 404, map[string]string{"error": "unknown endpoint or method"})
		return
	}
	if err != nil {
		status = 400
		if errors.Is(err, store.ErrNotFound) {
			status = 404
		}
		if errors.Is(err, store.ErrBudget) {
			status = 429
		}
		write(w, status, map[string]string{"error": svc.Filter.Text(err.Error())})
		return
	}
	write(w, status, result)
}
func (s *Server) Serve(ctx context.Context) error {
	h := &http.Server{Addr: s.Service.Config.HTTP.Listen, Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 120 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = h.Shutdown(shutdown)
		case <-done:
		}
	}()
	err := h.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
