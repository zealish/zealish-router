package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/zealish/zealish-router/internal/auth"
	"github.com/zealish/zealish-router/internal/config"
	"github.com/zealish/zealish-router/internal/metrics"
	"github.com/zealish/zealish-router/internal/provider"
	"github.com/zealish/zealish-router/internal/router"
	"github.com/zealish/zealish-router/internal/storage"
	"github.com/zealish/zealish-router/pkg/openai"
)

// stubProvider returns canned results without any network I/O.
type stubProvider struct {
	name   string
	err    error
	chunks []openai.StreamChunk
	// block, when non-nil, holds the stream open until it is closed.
	block chan struct{}
}

func (s *stubProvider) Name() string { return s.name }

func (s *stubProvider) ChatCompletion(_ context.Context, req *openai.ChatCompletionRequest) (*openai.ChatCompletionResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &openai.ChatCompletionResponse{
		ID:      "cmpl-1",
		Object:  "chat.completion",
		Model:   req.Model,
		Choices: []openai.Choice{{Index: 0, Message: &openai.Message{Role: "assistant", Content: json.RawMessage(`"pong"`)}}},
	}, nil
}

func (s *stubProvider) ChatCompletionStream(_ context.Context, _ *openai.ChatCompletionRequest) (<-chan openai.StreamChunk, error) {
	if s.err != nil {
		return nil, s.err
	}
	ch := make(chan openai.StreamChunk, len(s.chunks))
	for _, c := range s.chunks {
		ch <- c
	}
	if s.block == nil {
		close(ch)
		return ch, nil
	}
	go func() {
		<-s.block
		close(ch)
	}()
	return ch, nil
}

func (s *stubProvider) Embeddings(_ context.Context, req *openai.EmbeddingRequest) (*openai.EmbeddingResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &openai.EmbeddingResponse{
		Object: "list",
		Model:  req.Model,
		Data:   []openai.Embedding{{Object: "embedding", Index: 0, Embedding: json.RawMessage(`[0.1,0.2,0.3]`)}},
		Usage:  &openai.Usage{PromptTokens: 4, TotalTokens: 4},
	}, nil
}

// newTestServer wires a full routing table around p, with auth disabled.
func newTestServer(t *testing.T, p provider.Provider) http.Handler {
	return newTestServerWithConfig(t, p, nil)
}

// newTestServerWithConfig wires the same routing table, letting a test tweak
// the configuration before the routes are built.
func newTestServerWithConfig(t *testing.T, p provider.Provider, tweak func(*config.Config)) http.Handler {
	t.Helper()

	cfg := config.Default()
	cfg.Auth.Enabled = false
	cfg.Admin.Enabled = false
	if tweak != nil {
		tweak(cfg)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	collector := metrics.New()
	engine := router.NewEngine(logger, collector)
	engine.Reload([]storage.ModelAlias{
		{Alias: "gpt-5", Provider: "openai", Model: "gpt-5-upstream"},
		{Alias: "fast", Provider: "openai", Model: "gpt-5-mini"},
	}, nil, provider.NewRegistry(p))

	deps := Dependencies{
		Config:  cfg,
		Engine:  engine,
		Store:   storage.NewMemory(),
		Auth:    auth.NewService(false, nil, nil, logger),
		Metrics: collector,
		Logger:  logger,
	}
	return newRoutes(deps, newHandler(deps))
}

func post(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) openai.ErrorResponse {
	t.Helper()
	var out openai.ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode error envelope: %v (body=%q)", err, rec.Body.String())
	}
	return out
}

func TestChatCompletionsMalformedJSON(t *testing.T) {
	h := newTestServer(t, &stubProvider{name: "openai"})

	rec := post(t, h, `{"model": "gpt-5",`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if got := decodeError(t, rec).Error.Type; got != "invalid_request_error" {
		t.Errorf("error type = %q", got)
	}
}

func TestChatCompletionsMissingModel(t *testing.T) {
	h := newTestServer(t, &stubProvider{name: "openai"})

	rec := post(t, h, `{"messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if msg := decodeError(t, rec).Error.Message; !strings.Contains(msg, "model") {
		t.Errorf("message = %q, want it to mention 'model'", msg)
	}
}

func TestChatCompletionsUnknownAlias(t *testing.T) {
	h := newTestServer(t, &stubProvider{name: "openai"})

	rec := post(t, h, `{"model":"does-not-exist","messages":[]}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestChatCompletionsHappyPath(t *testing.T) {
	h := newTestServer(t, &stubProvider{name: "openai"})

	rec := post(t, h, `{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}

	var resp openai.ChatCompletionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Model != "gpt-5-upstream" {
		t.Errorf("model = %q, want the resolved upstream name", resp.Model)
	}
}

func TestChatCompletionsUpstreamFailureIsBadGateway(t *testing.T) {
	p := &stubProvider{name: "openai", err: &provider.Error{
		Provider: "openai", Status: 503, Kind: provider.ErrUpstream5xx, Message: "down",
	}}
	h := newTestServer(t, p)

	rec := post(t, h, `{"model":"gpt-5","messages":[]}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if msg := decodeError(t, rec).Error.Message; strings.Contains(msg, "down") {
		t.Error("internal upstream detail leaked to the client")
	}
}

func TestChatCompletionsUpstreamClientErrorPassThrough(t *testing.T) {
	p := &stubProvider{name: "openai", err: &provider.Error{
		Provider: "openai", Status: http.StatusBadRequest, Message: "unsupported parameter",
	}}
	h := newTestServer(t, p)

	rec := post(t, h, `{"model":"gpt-5","messages":[]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if msg := decodeError(t, rec).Error.Message; msg != "unsupported parameter" {
		t.Errorf("message = %q, want the upstream message", msg)
	}
}

func TestStreamingFramesAndSentinel(t *testing.T) {
	p := &stubProvider{name: "openai", chunks: []openai.StreamChunk{
		{ID: "c1", Object: "chat.completion.chunk", Model: "gpt-5-upstream"},
		{ID: "c2", Object: "chat.completion.chunk", Model: "gpt-5-upstream"},
	}}
	h := newTestServer(t, p)

	rec := post(t, h, `{"model":"gpt-5","stream":true,"messages":[]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := rec.Header().Get("X-Accel-Buffering"); got != "no" {
		t.Errorf("X-Accel-Buffering = %q, want no", got)
	}

	body := rec.Body.String()
	if !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Fatalf("stream not terminated with the sentinel: %q", body)
	}

	var ids []string
	for _, frame := range strings.Split(strings.TrimSpace(body), "\n\n") {
		payload := strings.TrimPrefix(frame, "data: ")
		if payload == "[DONE]" {
			continue
		}
		var chunk openai.StreamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			t.Fatalf("frame %q is not valid JSON: %v", payload, err)
		}
		ids = append(ids, chunk.ID)
	}
	if len(ids) != 2 || ids[0] != "c1" || ids[1] != "c2" {
		t.Fatalf("chunk ids = %v, want [c1 c2]", ids)
	}
}

func TestStreamingErrorBeforeHeadersUsesStatusCode(t *testing.T) {
	p := &stubProvider{name: "openai", err: &provider.Error{
		Provider: "openai", Status: 503, Kind: provider.ErrUpstream5xx, Message: "down",
	}}
	h := newTestServer(t, p)

	rec := post(t, h, `{"model":"gpt-5","stream":true,"messages":[]}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("Content-Type = %q, want JSON before any frame is written", got)
	}
}

func TestStreamingClientDisconnect(t *testing.T) {
	block := make(chan struct{})
	p := &stubProvider{
		name:   "openai",
		chunks: []openai.StreamChunk{{ID: "c1", Model: "gpt-5-upstream"}},
		block:  block,
	}
	h := newTestServer(t, p)
	t.Cleanup(func() { close(block) })

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-5","stream":true,"messages":[]}`)).WithContext(ctx)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(rec, req)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not return after client disconnect")
	}
	if strings.Contains(rec.Body.String(), "[DONE]") {
		t.Error("disconnected client must not be sent the sentinel")
	}
}

func TestChatCompletionsBodyTooLarge(t *testing.T) {
	h := newTestServerWithConfig(t, &stubProvider{name: "openai"}, func(c *config.Config) {
		c.Server.MaxBodyBytes = 256
	})

	body := `{"model":"gpt-5","messages":[{"role":"user","content":"` + strings.Repeat("a", 1024) + `"}]}`
	rec := post(t, h, body)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	if got := decodeError(t, rec).Error.Type; got != "invalid_request_error" {
		t.Errorf("error type = %q", got)
	}
}

func TestChatCompletionsBodyLimitDisabled(t *testing.T) {
	h := newTestServerWithConfig(t, &stubProvider{name: "openai"}, func(c *config.Config) {
		c.Server.MaxBodyBytes = 0
	})

	body := `{"model":"gpt-5","messages":[{"role":"user","content":"` + strings.Repeat("a", 1<<16) + `"}]}`
	if rec := post(t, h, body); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestRecovererReturnsGenericError(t *testing.T) {
	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logs, nil))

	r := chi.NewRouter()
	r.Use(recoverer(logger))
	r.Get("/boom", func(http.ResponseWriter, *http.Request) {
		panic("database password hunter2")
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, "hunter2") || strings.Contains(body, "handler_test.go") {
		t.Errorf("panic detail leaked to client: %q", body)
	}
	if !strings.Contains(logs.String(), "hunter2") {
		t.Error("panic value was not logged")
	}
}

func postTo(t *testing.T, h http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestEmbeddingsHappyPath(t *testing.T) {
	h := newTestServer(t, &stubProvider{name: "openai"})

	rec := postTo(t, h, "/v1/embeddings", `{"model":"gpt-5","input":"hello"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}

	var resp openai.EmbeddingResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Model != "gpt-5-upstream" {
		t.Errorf("model = %q, want the resolved upstream name", resp.Model)
	}
	if len(resp.Data) != 1 || string(resp.Data[0].Embedding) != "[0.1,0.2,0.3]" {
		t.Errorf("data = %+v, want the upstream vector passed through", resp.Data)
	}
}

func TestEmbeddingsMissingInput(t *testing.T) {
	h := newTestServer(t, &stubProvider{name: "openai"})

	rec := postTo(t, h, "/v1/embeddings", `{"model":"gpt-5"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if msg := decodeError(t, rec).Error.Message; !strings.Contains(msg, "input") {
		t.Errorf("message = %q, want it to mention 'input'", msg)
	}
}

func TestEmbeddingsMissingModel(t *testing.T) {
	h := newTestServer(t, &stubProvider{name: "openai"})

	rec := postTo(t, h, "/v1/embeddings", `{"input":"hello"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestEmbeddingsUnknownAlias(t *testing.T) {
	h := newTestServer(t, &stubProvider{name: "openai"})

	rec := postTo(t, h, "/v1/embeddings", `{"model":"does-not-exist","input":"hi"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestEmbeddingsUpstreamFailureIsBadGateway(t *testing.T) {
	p := &stubProvider{name: "openai", err: &provider.Error{
		Provider: "openai", Status: 503, Kind: provider.ErrUpstream5xx, Message: "down",
	}}
	h := newTestServer(t, p)

	rec := postTo(t, h, "/v1/embeddings", `{"model":"gpt-5","input":"hi"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if msg := decodeError(t, rec).Error.Message; strings.Contains(msg, "down") {
		t.Error("internal upstream detail leaked to the client")
	}
}

func TestEmbeddingsUnsupportedDialectIsBadRequest(t *testing.T) {
	p := &stubProvider{name: "openai", err: &provider.Error{
		Provider: "openai", Kind: provider.ErrUnsupported, Message: "no embeddings endpoint",
	}}
	h := newTestServer(t, p)

	rec := postTo(t, h, "/v1/embeddings", `{"model":"gpt-5","input":"hi"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
}
