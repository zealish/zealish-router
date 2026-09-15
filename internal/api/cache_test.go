package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zealish/zealish-router/internal/auth"
	"github.com/zealish/zealish-router/internal/cache"
	"github.com/zealish/zealish-router/internal/config"
	"github.com/zealish/zealish-router/internal/metrics"
	"github.com/zealish/zealish-router/internal/provider"
	"github.com/zealish/zealish-router/internal/router"
	"github.com/zealish/zealish-router/internal/storage"
	"github.com/zealish/zealish-router/pkg/openai"
)

// countingProvider records how many upstream calls the cache let through.
type countingProvider struct {
	stubProvider
	chat       atomic.Int64
	embeddings atomic.Int64
}

func (c *countingProvider) ChatCompletion(ctx context.Context, req *openai.ChatCompletionRequest) (*openai.ChatCompletionResponse, error) {
	c.chat.Add(1)
	return c.stubProvider.ChatCompletion(ctx, req)
}

func (c *countingProvider) ChatCompletionStream(ctx context.Context, req *openai.ChatCompletionRequest) (<-chan openai.StreamChunk, error) {
	c.chat.Add(1)
	return c.stubProvider.ChatCompletionStream(ctx, req)
}

func (c *countingProvider) Embeddings(ctx context.Context, req *openai.EmbeddingRequest) (*openai.EmbeddingResponse, error) {
	c.embeddings.Add(1)
	return c.stubProvider.Embeddings(ctx, req)
}

// newCachingServer wires the gateway with a response cache in front of a
// provider that counts its calls.
func newCachingServer(t *testing.T, responses *cache.Cache) (http.Handler, *countingProvider) {
	t.Helper()

	cfg := config.Default()
	cfg.Auth.Enabled = false
	cfg.Admin = config.Admin{Enabled: true, Token: adminToken}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	collector := metrics.New()
	upstream := &countingProvider{stubProvider: stubProvider{name: "openai"}}

	engine := router.NewEngine(logger, collector)
	engine.Reload([]storage.ModelAlias{
		{Alias: "gpt-5", Provider: "openai", Model: "gpt-5-upstream"},
		{Alias: "fast", Provider: "openai", Model: "gpt-5-mini"},
	}, nil, provider.NewRegistry(upstream))

	store := storage.NewMemory()
	deps := Dependencies{
		Config:    cfg,
		Engine:    engine,
		Loader:    router.NewLoader(store.Providers(), store.Models(), store.Combos(), store.Proxies(), engine),
		Store:     store,
		Auth:      auth.NewService(false, nil, nil, logger),
		AdminAuth: auth.NewAdminService(true, adminToken),
		Cache:     responses,
		Metrics:   collector,
		Logger:    logger,
	}
	return newRoutes(deps, newHandler(deps)), upstream
}

const chatBody = `{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`

func TestChatCompletionsCacheHitSkipsUpstream(t *testing.T) {
	h, upstream := newCachingServer(t, cache.New(time.Minute, 16))

	first := postTo(t, h, "/v1/chat/completions", chatBody)
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d (body=%s)", first.Code, first.Body.String())
	}
	if got := first.Header().Get(cacheHeader); got != headerMiss {
		t.Errorf("first %s = %q, want MISS", cacheHeader, got)
	}

	second := postTo(t, h, "/v1/chat/completions", chatBody)
	if second.Code != http.StatusOK {
		t.Fatalf("second status = %d", second.Code)
	}
	if got := second.Header().Get(cacheHeader); got != headerHit {
		t.Errorf("second %s = %q, want HIT", cacheHeader, got)
	}
	if first.Body.String() != second.Body.String() {
		t.Errorf("cached body differs:\n%s\n%s", first.Body.String(), second.Body.String())
	}
	if got := upstream.chat.Load(); got != 1 {
		t.Errorf("upstream calls = %d, want the hit to be served without one", got)
	}
	if ct := second.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content type = %q", ct)
	}
}

func TestCacheKeyedOnFullBody(t *testing.T) {
	h, upstream := newCachingServer(t, cache.New(time.Minute, 16))

	postTo(t, h, "/v1/chat/completions", chatBody)
	// Same model, different prompt: a different request, so a different entry.
	other := postTo(t, h, "/v1/chat/completions",
		`{"model":"gpt-5","messages":[{"role":"user","content":"bye"}]}`)
	if got := other.Header().Get(cacheHeader); got != headerMiss {
		t.Errorf("different prompt %s = %q, want MISS", cacheHeader, got)
	}
	// Same prompt, different model: also a different entry.
	model := postTo(t, h, "/v1/chat/completions",
		`{"model":"fast","messages":[{"role":"user","content":"hi"}]}`)
	if got := model.Header().Get(cacheHeader); got != headerMiss {
		t.Errorf("different model %s = %q, want MISS", cacheHeader, got)
	}
	if got := upstream.chat.Load(); got != 3 {
		t.Errorf("upstream calls = %d, want 3 distinct requests", got)
	}
}

// A stream is relayed chunk by chunk, so it can neither be served from nor
// admitted to the cache.
func TestStreamingBypassesCache(t *testing.T) {
	h, upstream := newCachingServer(t, cache.New(time.Minute, 16))
	body := `{"model":"gpt-5","stream":true,"messages":[{"role":"user","content":"hi"}]}`

	for i := range 2 {
		rec := postTo(t, h, "/v1/chat/completions", body)
		if rec.Code != http.StatusOK {
			t.Fatalf("stream %d status = %d", i, rec.Code)
		}
		if got := rec.Header().Get(cacheHeader); got != headerPass {
			t.Errorf("stream %d %s = %q, want BYPASS", i, cacheHeader, got)
		}
	}
	if got := upstream.chat.Load(); got != 2 {
		t.Errorf("upstream calls = %d, want every stream to reach upstream", got)
	}
}

func TestEmbeddingsCacheHitSkipsUpstream(t *testing.T) {
	h, upstream := newCachingServer(t, cache.New(time.Minute, 16))
	body := `{"model":"gpt-5","input":["hello"]}`

	if got := postTo(t, h, "/v1/embeddings", body).Header().Get(cacheHeader); got != headerMiss {
		t.Errorf("first %s = %q, want MISS", cacheHeader, got)
	}
	if got := postTo(t, h, "/v1/embeddings", body).Header().Get(cacheHeader); got != headerHit {
		t.Errorf("second %s = %q, want HIT", cacheHeader, got)
	}
	if got := upstream.embeddings.Load(); got != 1 {
		t.Errorf("upstream calls = %d, want 1", got)
	}
}

// Chat and embeddings bodies never collide even when they hash the same
// payload, because the endpoint is part of the key.
func TestCacheSeparatesEndpoints(t *testing.T) {
	h, _ := newCachingServer(t, cache.New(time.Minute, 16))
	body := `{"model":"gpt-5","input":["hello"],"messages":[{"role":"user","content":"hi"}]}`

	postTo(t, h, "/v1/chat/completions", body)
	if got := postTo(t, h, "/v1/embeddings", body).Header().Get(cacheHeader); got != headerMiss {
		t.Errorf("embeddings %s = %q, want MISS", cacheHeader, got)
	}
}

// A failed request must not be cached: the next identical call has to reach
// upstream again.
func TestUpstreamFailureIsNotCached(t *testing.T) {
	h, upstream := newCachingServer(t, cache.New(time.Minute, 16))
	upstream.err = &provider.Error{Provider: "openai", Status: http.StatusBadRequest, Message: "bad"}

	for i := range 2 {
		rec := postTo(t, h, "/v1/chat/completions", chatBody)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("attempt %d status = %d, want 400", i, rec.Code)
		}
	}
	if got := upstream.chat.Load(); got != 2 {
		t.Errorf("upstream calls = %d, want the failure not to be cached", got)
	}
}

func TestDisabledCacheBypasses(t *testing.T) {
	h, upstream := newCachingServer(t, nil)

	for range 2 {
		rec := postTo(t, h, "/v1/chat/completions", chatBody)
		if got := rec.Header().Get(cacheHeader); got != headerPass {
			t.Errorf("%s = %q, want BYPASS", cacheHeader, got)
		}
	}
	if got := upstream.chat.Load(); got != 2 {
		t.Errorf("upstream calls = %d, want no caching", got)
	}
}

func TestAdminCacheStatsAndPurge(t *testing.T) {
	h, upstream := newCachingServer(t, cache.New(time.Minute, 16))

	postTo(t, h, "/v1/chat/completions", chatBody)
	postTo(t, h, "/v1/chat/completions", chatBody)

	var stats cacheStatsResponse
	rec := adminRequest(t, h, http.MethodGet, "/api/v1/cache", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("stats status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &stats); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if !stats.Enabled || stats.Entries != 1 || stats.Hits != 1 || stats.Misses != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	if stats.HitRate != 0.5 {
		t.Errorf("hit rate = %v, want 0.5", stats.HitRate)
	}

	purge := adminRequest(t, h, http.MethodDelete, "/api/v1/cache", "")
	if purge.Code != http.StatusOK {
		t.Fatalf("purge status = %d", purge.Code)
	}
	var purged map[string]int
	if err := json.Unmarshal(purge.Body.Bytes(), &purged); err != nil {
		t.Fatalf("decode purge: %v", err)
	}
	if purged["purged"] != 1 {
		t.Errorf("purged = %d, want 1", purged["purged"])
	}

	if got := postTo(t, h, "/v1/chat/completions", chatBody).Header().Get(cacheHeader); got != headerMiss {
		t.Errorf("after purge %s = %q, want MISS", cacheHeader, got)
	}
	if got := upstream.chat.Load(); got != 2 {
		t.Errorf("upstream calls = %d, want the purge to force a refetch", got)
	}
}
