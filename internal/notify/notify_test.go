package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/jakecyr/stalemate/internal/ai"
	"github.com/jakecyr/stalemate/internal/config"
	"github.com/jakecyr/stalemate/internal/domain"
	"github.com/jakecyr/stalemate/internal/privacy"
	"github.com/jakecyr/stalemate/internal/service"
	"github.com/jakecyr/stalemate/internal/store"
)

func TestSignedWebhookIdempotencyAndDisclosureConsent(t *testing.T) {
	ctx := context.Background()
	c := config.Default()
	c.Notifications.SecretEnv = "TEST_WEBHOOK_SECRET"
	t.Setenv(c.Notifications.SecretEnv, "signing-secret")
	db, err := store.Open(ctx, "sqlite", filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		b, _ := io.ReadAll(r.Body)
		mac := hmac.New(sha256.New, []byte("signing-secret"))
		mac.Write(b)
		if r.Header.Get("X-Stalemate-Signature") != "sha256="+hex.EncodeToString(mac.Sum(nil)) || r.Header.Get("Idempotency-Key") != "impact" {
			t.Error("missing signature/idempotency key")
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	c.Notifications.WebhookURL = server.URL
	s := service.New(c, db, ai.Disabled{}, privacy.New(nil))
	p := domain.Project{ID: "p", Name: "private", Path: t.TempDir(), CloudAllowed: false}
	if err = db.PutProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err = db.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.PutEvidence(ctx, domain.Evidence{ID: "e", Project: "p", Kind: "test"}); err != nil {
			return err
		}
		_, err := tx.PutImpact(ctx, domain.Impact{ID: "impact", Affected: "p", Changed: "p", Evidence: "e", Status: "open"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	n := &Notifier{Service: s, Client: server.Client()}
	j := domain.Job{Payload: `{"impact":"impact"}`}
	if err = n.Handle(ctx, j); err == nil || calls != 0 {
		t.Fatal("private project disclosed", err)
	}
	p.CloudAllowed = true
	if err = db.PutProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err = n.Handle(ctx, j); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal(calls)
	}
}
