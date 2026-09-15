package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zealish/zealish-router/internal/auth"
	"github.com/zealish/zealish-router/internal/config"
	"github.com/zealish/zealish-router/internal/metrics"
	"github.com/zealish/zealish-router/internal/provider"
	"github.com/zealish/zealish-router/internal/router"
	"github.com/zealish/zealish-router/internal/storage"
	"github.com/zealish/zealish-router/pkg/openai"
)

// newAllowlistServer wires a gateway serving two aliases behind a single key
// restricted to the models given.
func newAllowlistServer(t *testing.T, allowed ...string) (http.Handler, string) {
	t.Helper()

	cfg := config.Default()
	cfg.Auth.Enabled = true
	cfg.Admin.Enabled = false

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	collector := metrics.New()
	store := storage.NewMemory()

	generated, err := auth.GenerateKey("scoped")
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	generated.Record.AllowedModels = allowed
	if err := store.APIKeys().Create(context.Background(), generated.Record); err != nil {
		t.Fatalf("create key: %v", err)
	}

	engine := router.NewEngine(logger, collector)
	engine.Reload([]storage.ModelAlias{
		{Alias: "gpt-5", Provider: "openai", Model: "gpt-5-upstream"},
		{Alias: "claude", Provider: "openai", Model: "claude-upstream"},
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
	return newRoutes(deps, newHandler(deps)), generated.Raw
}

func callModel(t *testing.T, h http.Handler, rawKey, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestChatCompletionsAllowedModelPasses(t *testing.T) {
	h, rawKey := newAllowlistServer(t, "gpt-5")

	rec := callModel(t, h, rawKey, "/v1/chat/completions",
		`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%q)", rec.Code, rec.Body.String())
	}
}

func TestChatCompletionsDisallowedModelReturns403(t *testing.T) {
	h, rawKey := newAllowlistServer(t, "gpt-5")

	rec := callModel(t, h, rawKey, "/v1/chat/completions",
		`{"model":"claude","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body=%q)", rec.Code, rec.Body.String())
	}
	if got := decodeError(t, rec).Error.Type; got != "invalid_request_error" {
		t.Errorf("error type = %q, want invalid_request_error", got)
	}
}

// Streaming goes through the same handler, so the rejection must still be a
// status code rather than an SSE frame.
func TestChatCompletionsStreamDisallowedModelReturns403(t *testing.T) {
	h, rawKey := newAllowlistServer(t, "gpt-5")

	rec := callModel(t, h, rawKey, "/v1/chat/completions",
		`{"model":"claude","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body=%q)", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "data:") {
		t.Errorf("rejection was framed as SSE: %q", rec.Body.String())
	}
}

func TestEmbeddingsDisallowedModelReturns403(t *testing.T) {
	h, rawKey := newAllowlistServer(t, "gpt-5")

	rec := callModel(t, h, rawKey, "/v1/embeddings", `{"model":"claude","input":"hi"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body=%q)", rec.Code, rec.Body.String())
	}
}

func TestEmptyAllowlistReachesEveryModel(t *testing.T) {
	h, rawKey := newAllowlistServer(t)

	for _, model := range []string{"gpt-5", "claude"} {
		rec := callModel(t, h, rawKey, "/v1/chat/completions",
			`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`)
		if rec.Code != http.StatusOK {
			t.Errorf("model %s status = %d, want 200 (body=%q)", model, rec.Code, rec.Body.String())
		}
	}
}

func TestListModelsHidesDisallowedModels(t *testing.T) {
	h, rawKey := newAllowlistServer(t, "gpt-5")

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	list := decodeJSON[openai.ModelList](t, rec)
	if len(list.Data) != 1 || list.Data[0].ID != "gpt-5" {
		t.Errorf("models = %+v, want only gpt-5", list.Data)
	}
}
