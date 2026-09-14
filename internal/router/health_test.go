package router

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/zealish/zealish-router/internal/provider"
	"github.com/zealish/zealish-router/pkg/openai"
)

// pingableProvider is a fakeProvider whose Ping result is scripted.
type pingableProvider struct {
	fakeProvider
	mu      sync.Mutex
	pingErr error
	pings   int
}

func (p *pingableProvider) Ping(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pings++
	return p.pingErr
}

func (p *pingableProvider) setPingErr(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pingErr = err
}

func (p *pingableProvider) pingCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pings
}

func newTestChecker(e *Engine) *HealthChecker {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewHealthChecker(e, HealthCheckPolicy{}, logger)
}

func TestHealthCheckMarksFailingProviderDown(t *testing.T) {
	primary := &pingableProvider{fakeProvider: fakeProvider{name: "openai"}}
	primary.setPingErr(upstreamErr(503, provider.ErrUpstream5xx))
	secondary := &fakeProvider{name: "openrouter"}
	tertiary := &fakeProvider{name: "ollama"}

	rec := newRecorder()
	e := newTestEngine(t, rec, primary, secondary, tertiary)
	newTestChecker(e).CheckAll(context.Background())

	resp, err := e.ChatCompletion(context.Background(), &openai.ChatCompletionRequest{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if resp.ID != "openrouter" {
		t.Errorf("served by %q, want fallback %q", resp.ID, "openrouter")
	}
	if got := primary.callCount(); got != 0 {
		t.Errorf("unhealthy provider was called %d times, want 0", got)
	}
	if got := rec.get("openai/unhealthy"); got != 1 {
		t.Errorf("recorded openai/unhealthy = %d, want 1", got)
	}
}

func TestHealthCheckRecoveryRestoresProvider(t *testing.T) {
	primary := &pingableProvider{fakeProvider: fakeProvider{name: "openai"}}
	primary.setPingErr(upstreamErr(503, provider.ErrUpstream5xx))
	secondary := &fakeProvider{name: "openrouter"}
	tertiary := &fakeProvider{name: "ollama"}

	e := newTestEngine(t, newRecorder(), primary, secondary, tertiary)
	checker := newTestChecker(e)
	checker.CheckAll(context.Background())

	primary.setPingErr(nil)
	checker.CheckAll(context.Background())

	resp, err := e.ChatCompletion(context.Background(), &openai.ChatCompletionRequest{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if resp.ID != "openai" {
		t.Errorf("served by %q, want recovered primary %q", resp.ID, "openai")
	}
}

func TestHealthCheckAllProvidersDownExhaustsChain(t *testing.T) {
	providers := make([]provider.Provider, 0, 3)
	for _, name := range []string{"openai", "openrouter", "ollama"} {
		p := &pingableProvider{fakeProvider: fakeProvider{name: name}}
		p.setPingErr(upstreamErr(503, provider.ErrUpstream5xx))
		providers = append(providers, p)
	}

	e := newTestEngine(t, newRecorder(), providers...)
	newTestChecker(e).CheckAll(context.Background())

	_, err := e.ChatCompletion(context.Background(), &openai.ChatCompletionRequest{Model: "gpt-5"})
	if !errors.Is(err, ErrChainExhausted) {
		t.Fatalf("err = %v, want ErrChainExhausted", err)
	}
	if !errors.Is(err, ErrProviderUnhealthy) {
		t.Fatalf("err = %v, want wrapped ErrProviderUnhealthy", err)
	}
}

func TestHealthCheckSkipsNonPingerProviders(t *testing.T) {
	primary := &fakeProvider{name: "openai"}

	e := newTestEngine(t, newRecorder(), primary)
	newTestChecker(e).CheckAll(context.Background())

	resp, err := e.ChatCompletion(context.Background(), &openai.ChatCompletionRequest{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if resp.ID != "openai" {
		t.Errorf("served by %q, want %q", resp.ID, "openai")
	}
}

func TestHealthCheckCancelledContextLeavesHealthUntouched(t *testing.T) {
	primary := &pingableProvider{fakeProvider: fakeProvider{name: "openai"}}
	primary.setPingErr(upstreamErr(503, provider.ErrUpstream5xx))

	e := newTestEngine(t, newRecorder(), primary)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	newTestChecker(e).CheckAll(ctx)

	if !e.probes.healthy("openai") {
		t.Error("shutdown-time probe must not mark providers down")
	}
}

func TestHealthCheckSurfacesInHealthSnapshot(t *testing.T) {
	primary := &pingableProvider{fakeProvider: fakeProvider{name: "openai"}}
	primary.setPingErr(upstreamErr(503, provider.ErrUpstream5xx))

	e := newTestEngine(t, newRecorder(), primary)
	newTestChecker(e).CheckAll(context.Background())

	health := e.Health()
	h, ok := health["openai"]
	if !ok {
		t.Fatal("openai absent from health snapshot")
	}
	if h.State != CircuitOpen {
		t.Errorf("state = %q, want %q", h.State, CircuitOpen)
	}
	if h.LastProbeError == "" {
		t.Error("LastProbeError is empty, want probe failure message")
	}
}

func TestHealthCheckProbeCountsPerCheck(t *testing.T) {
	primary := &pingableProvider{fakeProvider: fakeProvider{name: "openai"}}

	e := newTestEngine(t, newRecorder(), primary)
	checker := newTestChecker(e)
	checker.CheckAll(context.Background())
	checker.CheckAll(context.Background())

	if got := primary.pingCount(); got != 2 {
		t.Errorf("pings = %d, want 2", got)
	}
}
