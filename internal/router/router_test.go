package router

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zealish/zealish-router/internal/provider"
	"github.com/zealish/zealish-router/internal/storage"
	"github.com/zealish/zealish-router/pkg/openai"
)

// fakeProvider is a scripted Provider: each call pops the next outcome.
type fakeProvider struct {
	name     string
	mu       sync.Mutex
	results  []error
	calls    int
	streamed int
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) next() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if len(f.results) == 0 {
		return nil
	}
	err := f.results[0]
	if len(f.results) > 1 {
		f.results = f.results[1:]
	}
	return err
}

func (f *fakeProvider) ChatCompletion(_ context.Context, req *openai.ChatCompletionRequest) (*openai.ChatCompletionResponse, error) {
	if err := f.next(); err != nil {
		return nil, err
	}
	return &openai.ChatCompletionResponse{ID: f.name, Model: req.Model}, nil
}

func (f *fakeProvider) ChatCompletionStream(_ context.Context, req *openai.ChatCompletionRequest) (<-chan openai.StreamChunk, error) {
	if err := f.next(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.streamed++
	f.mu.Unlock()

	ch := make(chan openai.StreamChunk, 1)
	ch <- openai.StreamChunk{ID: f.name, Model: req.Model}
	close(ch)
	return ch, nil
}

func (f *fakeProvider) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// countingRecorder captures routing telemetry.
type countingRecorder struct {
	mu       sync.Mutex
	counts   map[string]int
	tokens   map[string]int
	circuits map[string]string
}

func newRecorder() *countingRecorder {
	return &countingRecorder{
		counts:   map[string]int{},
		tokens:   map[string]int{},
		circuits: map[string]string{},
	}
}

func (c *countingRecorder) RecordProviderError(providerName, reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts[providerName+"/"+reason]++
}

func (c *countingRecorder) RecordTokens(providerName, model string, prompt, completion int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tokens[providerName+"/"+model+"/prompt"] += prompt
	c.tokens[providerName+"/"+model+"/completion"] += completion
}

func (c *countingRecorder) RecordCircuitState(providerName, state string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.circuits[providerName] = state
}

func (c *countingRecorder) get(key string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts[key]
}

func (c *countingRecorder) circuit(providerName string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.circuits[providerName]
}

func (c *countingRecorder) token(key string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tokens[key]
}

func upstreamErr(status int, kind error) error {
	return &provider.Error{Provider: "test", Status: status, Kind: kind, Message: "boom"}
}

// newTestEngine builds an engine over the standard three-alias chain
// gpt-5 -> fast -> local, with retries disabled unless overridden.
func newTestEngine(t *testing.T, rec Recorder, providers ...provider.Provider) *Engine {
	t.Helper()

	aliases := []storage.ModelAlias{
		{Alias: "gpt-5", Provider: "openai", Model: "gpt-5-upstream", Fallback: []string{"fast", "local"}},
		{Alias: "fast", Provider: "openrouter", Model: "gpt-5-mini"},
		{Alias: "local", Provider: "ollama", Model: "qwen3:32b"},
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	e := NewEngine(logger, rec)
	e.Reload(aliases, nil, provider.NewRegistry(providers...))
	e.retry = Retry{Attempts: 1}
	return e
}

func TestChatCompletionPrimaryFailsFallbackSucceeds(t *testing.T) {
	primary := &fakeProvider{name: "openai", results: []error{upstreamErr(500, provider.ErrUpstream5xx)}}
	secondary := &fakeProvider{name: "openrouter"}
	tertiary := &fakeProvider{name: "ollama"}

	rec := newRecorder()
	e := newTestEngine(t, rec, primary, secondary, tertiary)

	resp, err := e.ChatCompletion(context.Background(), &openai.ChatCompletionRequest{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if resp.ID != "openrouter" {
		t.Fatalf("served by %q, want openrouter", resp.ID)
	}
	if resp.Model != "gpt-5-mini" {
		t.Errorf("upstream model = %q, want gpt-5-mini", resp.Model)
	}
	if tertiary.callCount() != 0 {
		t.Error("tertiary must not be reached once the fallback succeeds")
	}
	if got := rec.get("openai/upstream_5xx"); got != 1 {
		t.Errorf("openai/upstream_5xx = %d, want 1", got)
	}
}

func TestChatCompletionClientErrorNoFallback(t *testing.T) {
	primary := &fakeProvider{name: "openai", results: []error{upstreamErr(400, nil)}}
	secondary := &fakeProvider{name: "openrouter"}

	rec := newRecorder()
	e := newTestEngine(t, rec, primary, secondary, &fakeProvider{name: "ollama"})

	_, err := e.ChatCompletion(context.Background(), &openai.ChatCompletionRequest{Model: "gpt-5"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if errors.Is(err, ErrChainExhausted) {
		t.Error("400 must be surfaced directly, not as chain exhaustion")
	}

	var perr *provider.Error
	if !errors.As(err, &perr) || perr.Status != http.StatusBadRequest {
		t.Fatalf("err = %v, want upstream 400", err)
	}
	if secondary.callCount() != 0 {
		t.Error("fallback must not run after a 4xx")
	}
	if got := rec.get("openai/client_error"); got != 1 {
		t.Errorf("openai/client_error = %d, want 1", got)
	}
}

func TestChatCompletionChainExhausted(t *testing.T) {
	primary := &fakeProvider{name: "openai", results: []error{upstreamErr(503, provider.ErrUpstream5xx)}}
	secondary := &fakeProvider{name: "openrouter", results: []error{upstreamErr(429, provider.ErrRateLimited)}}
	tertiary := &fakeProvider{name: "ollama", results: []error{upstreamErr(0, provider.ErrConnection)}}

	rec := newRecorder()
	e := newTestEngine(t, rec, primary, secondary, tertiary)

	_, err := e.ChatCompletion(context.Background(), &openai.ChatCompletionRequest{Model: "gpt-5"})
	if !errors.Is(err, ErrChainExhausted) {
		t.Fatalf("err = %v, want ErrChainExhausted", err)
	}
	if !errors.Is(err, provider.ErrConnection) {
		t.Errorf("exhausted error must wrap the last failure, got %v", err)
	}
	for _, p := range []*fakeProvider{primary, secondary, tertiary} {
		if p.callCount() != 1 {
			t.Errorf("%s called %d times, want 1", p.name, p.callCount())
		}
	}
	if rec.get("openai/upstream_5xx") != 1 ||
		rec.get("openrouter/rate_limited") != 1 ||
		rec.get("ollama/connection") != 1 {
		t.Errorf("unexpected error counts: %v", rec.counts)
	}
}

func TestChainDeterministic(t *testing.T) {
	e := newTestEngine(t, nil, &fakeProvider{name: "openai"})

	want := []string{"gpt-5", "fast", "local"}
	for i := range 50 {
		got := e.Chain("gpt-5")
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("run %d: chain = %v, want %v", i, got, want)
		}
	}
	if got := e.Chain("fast"); len(got) != 1 || got[0] != "fast" {
		t.Errorf("chain without fallbacks = %v, want [fast]", got)
	}
}

func TestChainDeduplicatesAndCaps(t *testing.T) {
	e := newTestEngine(t, nil, &fakeProvider{name: "openai"})
	e.Reload([]storage.ModelAlias{
		{Alias: "gpt-5", Provider: "openai", Model: "m", Fallback: []string{"fast", "gpt-5", "fast", "local"}},
	}, nil, provider.NewRegistry(&fakeProvider{name: "openai"}))

	got := e.Chain("gpt-5")
	want := []string{"gpt-5", "fast", "local"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("chain = %v, want %v", got, want)
	}
}

func TestUnknownModelIsTerminal(t *testing.T) {
	e := newTestEngine(t, nil, &fakeProvider{name: "openai"})

	_, err := e.ChatCompletion(context.Background(), &openai.ChatCompletionRequest{Model: "nope"})
	if !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("err = %v, want ErrUnknownModel", err)
	}
}

func TestRetryPerProviderBeforeFallback(t *testing.T) {
	// Primary fails twice (exhausting its retries), fallback answers.
	primary := &fakeProvider{name: "openai", results: []error{
		upstreamErr(503, provider.ErrUpstream5xx),
		upstreamErr(503, provider.ErrUpstream5xx),
	}}
	secondary := &fakeProvider{name: "openrouter"}

	rec := newRecorder()
	e := newTestEngine(t, rec, primary, secondary, &fakeProvider{name: "ollama"})
	e.retry = Retry{Attempts: 2, BaseDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond}

	resp, err := e.ChatCompletion(context.Background(), &openai.ChatCompletionRequest{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if resp.ID != "openrouter" {
		t.Fatalf("served by %q, want openrouter", resp.ID)
	}
	if primary.callCount() != 2 {
		t.Errorf("primary called %d times, want 2", primary.callCount())
	}
	if got := rec.get("openai/upstream_5xx"); got != 2 {
		t.Errorf("openai/upstream_5xx = %d, want 2", got)
	}
}

func TestNoRetryOnClientError(t *testing.T) {
	primary := &fakeProvider{name: "openai", results: []error{upstreamErr(401, nil)}}

	e := newTestEngine(t, nil, primary, &fakeProvider{name: "openrouter"}, &fakeProvider{name: "ollama"})
	e.retry = Retry{Attempts: 3, BaseDelay: time.Millisecond}

	if _, err := e.ChatCompletion(context.Background(), &openai.ChatCompletionRequest{Model: "gpt-5"}); err == nil {
		t.Fatal("expected an error")
	}
	if primary.callCount() != 1 {
		t.Errorf("primary called %d times, want 1 (4xx must not retry)", primary.callCount())
	}
}

func TestStreamFallbackBeforeFirstChunk(t *testing.T) {
	primary := &fakeProvider{name: "openai", results: []error{upstreamErr(500, provider.ErrUpstream5xx)}}
	secondary := &fakeProvider{name: "openrouter"}

	e := newTestEngine(t, newRecorder(), primary, secondary, &fakeProvider{name: "ollama"})

	ch, err := e.ChatCompletionStream(context.Background(), &openai.ChatCompletionRequest{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("ChatCompletionStream: %v", err)
	}
	chunk, ok := <-ch
	if !ok {
		t.Fatal("stream closed without a chunk")
	}
	if chunk.ID != "openrouter" {
		t.Fatalf("streamed by %q, want openrouter", chunk.ID)
	}
	if _, open := <-ch; open {
		t.Error("channel should be closed after the single chunk")
	}
}

func TestContextCancellationStopsChain(t *testing.T) {
	primary := &fakeProvider{name: "openai", results: []error{upstreamErr(503, provider.ErrUpstream5xx)}}
	secondary := &fakeProvider{name: "openrouter"}

	e := newTestEngine(t, nil, primary, secondary, &fakeProvider{name: "ollama"})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := e.ChatCompletion(ctx, &openai.ChatCompletionRequest{Model: "gpt-5"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if secondary.callCount() != 0 {
		t.Error("chain must stop once the client context is cancelled")
	}
}

func TestBackoffWithinBounds(t *testing.T) {
	e := newTestEngine(t, nil, &fakeProvider{name: "openai"})
	e.retry = Retry{Attempts: 5, BaseDelay: 100 * time.Millisecond, MaxDelay: 400 * time.Millisecond}

	for n := 1; n <= 5; n++ {
		for range 20 {
			d := e.backoff(n)
			if d <= 0 || d > e.retry.MaxDelay {
				t.Fatalf("backoff(%d) = %v, out of (0, %v]", n, d, e.retry.MaxDelay)
			}
		}
	}
}
