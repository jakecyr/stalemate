// Package notify delivers idempotently queued local notifications and optional
// signed webhooks. Slack can consume the same webhook payload through a bridge.
package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/jakecyr/stalemate/internal/domain"
	"github.com/jakecyr/stalemate/internal/process"
	"github.com/jakecyr/stalemate/internal/service"
)

type Notifier struct {
	Service *service.Service
	Client  *http.Client
}

func (n *Notifier) Handle(ctx context.Context, j domain.Job) error {
	var p map[string]string
	if err := json.Unmarshal([]byte(j.Payload), &p); err != nil {
		return err
	}
	s := n.Service
	v, err := s.Store.Impact(ctx, p["impact"])
	if err != nil {
		return err
	}
	if v.Status != "open" {
		return nil
	}
	project, err := s.Store.Project(ctx, v.Affected)
	if err != nil {
		return err
	}
	s.Log.Info("dependency impact", "id", v.ID, "project", project.Name, "severity", v.Severity)
	if s.Config.Notifications.Desktop {
		message := "Stalemate: " + project.Name + " needs dependency revalidation"
		switch runtime.GOOS {
		case "darwin":
			_, err = process.Run(ctx, "", []string{"osascript", "-e", `on run argv
display notification (item 1 of argv) with title "Stalemate"
end run`, message}, nil, nil)
		case "linux":
			_, err = process.Run(ctx, "", []string{"notify-send", "Stalemate", message}, nil, nil)
		default:
			err = errors.New("desktop notifications are supported on macOS and Linux")
		}
		if err != nil {
			return err
		}
	}
	if s.Config.Notifications.WebhookURL == "" {
		return nil
	}
	// Outbound disclosure requires both workspace opt-ins, even for a webhook.
	if !project.CloudAllowed {
		return errors.New("webhook disclosure requires cloud consent for the affected project")
	}
	e, err := s.Store.Evidence(ctx, v.Evidence)
	if err != nil {
		return err
	}
	origin, err := s.Store.Project(ctx, e.Project)
	if err != nil {
		return err
	}
	if !origin.CloudAllowed {
		return errors.New("webhook disclosure requires cloud consent for the originating project")
	}
	body := []byte(s.Filter.Text(domain.JSON(map[string]any{"type": "impact.created", "id": v.ID, "project": project.Name, "impact": v})))
	req, err := http.NewRequestWithContext(ctx, "POST", s.Config.Notifications.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", v.ID)
	if key := os.Getenv(s.Config.Notifications.SecretEnv); key != "" {
		h := hmac.New(sha256.New, []byte(key))
		h.Write(body)
		req.Header.Set("X-Stalemate-Signature", "sha256="+hex.EncodeToString(h.Sum(nil)))
	}
	client := n.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("webhook redirects disabled") }}
	}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("webhook request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook HTTP %d", resp.StatusCode)
	}
	return nil
}

func Summary(v []domain.Impact) string {
	var b strings.Builder
	for _, i := range v {
		fmt.Fprintf(&b, "[%s] %s: %s\nVerify: %s\n", i.Severity, i.ID, i.Reason, i.Verification)
	}
	return b.String()
}
