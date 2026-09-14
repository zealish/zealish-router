package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zealish/zealish-router/internal/auth"
	"github.com/zealish/zealish-router/internal/config"
	"github.com/zealish/zealish-router/internal/metrics"
	"github.com/zealish/zealish-router/internal/provider"
	"github.com/zealish/zealish-router/internal/router"
	"github.com/zealish/zealish-router/internal/storage"
)

// newQuotaServer wires a gateway whose single stored key carries the given
// quotas, and returns the raw key to authenticate with.
func newQuotaServer(t *testing.T, perMin int, budgetUSD float64) (http.Handler, string, storage.Store) {
	t.Helper()

	cfg := config.Default()
	cfg.Auth.Enabled = true
	cfg.Admin.Enabled = false

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	collector := metrics.New()
	store := storage.NewMemory()

	generated, err := auth.GenerateKey("test")
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	generated.Record.RateLimitPerMin = perMin
	generated.Record.MonthlyBudgetUSD = budgetUSD
	if err := store.APIKeys().Create(context.Background(), generated.Record); err != nil {
		t.Fatalf("create key: %v", err)
	}

	engine := router.NewEngine(logger, collector)
	engine.SetUsageStore(store.Usage())
	engine.Reload([]storage.ModelAlias{
		{Alias: "gpt-5", Provider: "openai", Model: "gpt-5-upstream"},
	}, nil, provider.NewRegistry(&stubProvider{name: "openai"}))

	deps := Dependencies{
		Config:  cfg,
		Engine:  engine,
		Store:   store,
		Auth:    auth.NewService(true, nil, store.APIKeys(), logger),
		Quota:   auth.NewQuota(store.Usage()),
		Metrics: collector,
		Logger:  logger,
	}
	return newRoutes(deps, newHandler(deps)), generated.Raw, store
}

func chat(t *testing.T, h http.Handler, rawKey string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestChatCompletionsRateLimitReturns429(t *testing.T) {
	h, rawKey, _ := newQuotaServer(t, 2, 0)

	for i := range 2 {
		rec := chat(t, h, rawKey)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d status = %d, want 200 (body=%q)", i, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("X-RateLimit-Limit"); got != "2" {
			t.Errorf("request %d X-RateLimit-Limit = %q, want \"2\"", i, got)
		}
	}

	rec := chat(t, h, rawKey)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("third request status = %d, want 429", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "60" {
		t.Errorf("Retry-After = %q, want \"60\"", got)
	}
	if got := rec.Header().Get("X-RateLimit-Remaining"); got != "0" {
		t.Errorf("X-RateLimit-Remaining = %q, want \"0\"", got)
	}
	if got := decodeError(t, rec).Error.Type; got != "rate_limit_error" {
		t.Errorf("error type = %q, want rate_limit_error", got)
	}
}

func TestChatCompletionsBudgetExhaustedReturns429(t *testing.T) {
	h, rawKey, store := newQuotaServer(t, 0, 1.0)
	ctx := context.Background()

	keys, err := store.APIKeys().List(ctx)
	if err != nil || len(keys) != 1 {
		t.Fatalf("list keys: %v (n=%d)", err, len(keys))
	}

	// Overspend the budget inside the current calendar month.
	if err := store.Usage().Record(ctx, storage.UsageEvent{
		CreatedAt: time.Now().UTC(), KeyID: keys[0].ID, Alias: "gpt-5",
		Provider: "openai", Model: "m", Status: "ok", CostUSD: 5,
	}); err != nil {
		t.Fatalf("record usage: %v", err)
	}

	rec := chat(t, h, rawKey)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 (body=%q)", rec.Code, rec.Body.String())
	}
	if got := decodeError(t, rec).Error.Type; got != "insufficient_quota" {
		t.Errorf("error type = %q, want insufficient_quota", got)
	}
	// The budget message must not leak the spend figure or the key id.
	if body := rec.Body.String(); strings.Contains(body, keys[0].ID) {
		t.Errorf("response leaked the key id: %s", body)
	}
}

func TestChatCompletionsUnlimitedKeySetsNoRateHeaders(t *testing.T) {
	h, rawKey, _ := newQuotaServer(t, 0, 0)

	rec := chat(t, h, rawKey)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%q)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-RateLimit-Limit"); got != "" {
		t.Errorf("X-RateLimit-Limit = %q, want no header for an unlimited key", got)
	}
}

func TestChatCompletionsRecordsKeyAttribution(t *testing.T) {
	h, rawKey, store := newQuotaServer(t, 0, 0)
	ctx := context.Background()

	if rec := chat(t, h, rawKey); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%q)", rec.Code, rec.Body.String())
	}

	keys, err := store.APIKeys().List(ctx)
	if err != nil || len(keys) != 1 {
		t.Fatalf("list keys: %v (n=%d)", err, len(keys))
	}

	events, err := store.Usage().Recent(ctx, 10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("recorded %d events, want 1", len(events))
	}
	if events[0].KeyID != keys[0].ID {
		t.Errorf("usage key_id = %q, want %q", events[0].KeyID, keys[0].ID)
	}
}
