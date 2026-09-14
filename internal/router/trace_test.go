package router

import (
	"context"
	"testing"
	"time"

	"github.com/zealish/zealish-router/internal/provider"
	"github.com/zealish/zealish-router/internal/storage"
	"github.com/zealish/zealish-router/pkg/openai"
)

// tracedContext carries a fixed request id, which is what turns tracing on.
func tracedContext(id string) context.Context {
	return WithRequestID(context.Background(), id)
}

// traceOf reads back the single trace the engine should have written.
func traceOf(t *testing.T, store storage.TraceStore, id string) storage.RequestTrace {
	t.Helper()

	trace, err := store.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get trace %s: %v", id, err)
	}
	return trace
}

func TestTraceRecordsSuccessfulRequest(t *testing.T) {
	store := storage.NewMemory()
	e := newTestEngine(t, newRecorder(), &fakeProvider{name: "openai"},
		&fakeProvider{name: "openrouter"}, &fakeProvider{name: "ollama"})
	e.SetTraceStore(store.Traces())

	if _, err := e.ChatCompletion(tracedContext("req-ok"), &openai.ChatCompletionRequest{Model: "gpt-5"}); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	trace := traceOf(t, store.Traces(), "req-ok")
	if trace.Model != "gpt-5" {
		t.Errorf("Model = %q, want gpt-5", trace.Model)
	}
	if trace.FinalStatus != "ok" {
		t.Errorf("FinalStatus = %q, want ok", trace.FinalStatus)
	}
	if trace.FinalProvider != "openai" {
		t.Errorf("FinalProvider = %q, want openai", trace.FinalProvider)
	}
	if len(trace.Attempts) != 1 {
		t.Fatalf("len(Attempts) = %d, want 1", len(trace.Attempts))
	}

	a := trace.Attempts[0]
	if a.Seq != 1 {
		t.Errorf("Seq = %d, want 1", a.Seq)
	}
	if a.Model != "gpt-5-upstream" {
		t.Errorf("upstream model = %q, want gpt-5-upstream", a.Model)
	}
	if a.Retry || a.Fallback {
		t.Errorf("first attempt marked retry=%t fallback=%t, want both false", a.Retry, a.Fallback)
	}
	if a.Error != "" {
		t.Errorf("Error = %q, want empty on success", a.Error)
	}
}

// A request that retries the same provider and then falls back to the next one
// must still produce exactly one trace, with every try on its timeline.
func TestTraceRetryAndFallbackShareOneTrace(t *testing.T) {
	store := storage.NewMemory()
	primary := &fakeProvider{name: "openai", results: []error{
		upstreamErr(500, provider.ErrUpstream5xx),
		upstreamErr(500, provider.ErrUpstream5xx),
	}}
	secondary := &fakeProvider{name: "openrouter"}

	e := newTestEngine(t, newRecorder(), primary, secondary, &fakeProvider{name: "ollama"})
	e.SetTraceStore(store.Traces())
	// Two tries per provider, so the primary is retried before the chain moves.
	e.retry = Retry{Attempts: 2}

	resp, err := e.ChatCompletion(tracedContext("req-retry"), &openai.ChatCompletionRequest{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if resp.ID != "openrouter" {
		t.Fatalf("served by %q, want openrouter", resp.ID)
	}

	traces, total, err := store.Traces().List(context.Background(), storage.TraceFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 1 || len(traces) != 1 {
		t.Fatalf("total=%d len=%d, want exactly one trace for one request", total, len(traces))
	}

	trace := traceOf(t, store.Traces(), "req-retry")
	if len(trace.Attempts) != 3 {
		t.Fatalf("len(Attempts) = %d, want 3 (two tries on openai, one on openrouter)", len(trace.Attempts))
	}

	want := []struct {
		provider string
		status   string
		retry    bool
		fallback bool
	}{
		{"openai", "upstream_5xx", false, false},
		{"openai", "upstream_5xx", true, false},
		{"openrouter", "ok", false, true},
	}
	for i, w := range want {
		a := trace.Attempts[i]
		if a.Seq != i+1 {
			t.Errorf("attempt %d Seq = %d, want %d", i, a.Seq, i+1)
		}
		if a.Provider != w.provider {
			t.Errorf("attempt %d Provider = %q, want %q", i, a.Provider, w.provider)
		}
		if a.Status != w.status {
			t.Errorf("attempt %d Status = %q, want %q", i, a.Status, w.status)
		}
		if a.Retry != w.retry {
			t.Errorf("attempt %d Retry = %t, want %t", i, a.Retry, w.retry)
		}
		if a.Fallback != w.fallback {
			t.Errorf("attempt %d Fallback = %t, want %t", i, a.Fallback, w.fallback)
		}
	}

	if trace.Attempts[0].Error == "" {
		t.Error("failed attempt kept no error message")
	}
	if trace.FinalProvider != "openrouter" || trace.FinalStatus != "ok" {
		t.Errorf("final = %s/%s, want openrouter/ok", trace.FinalProvider, trace.FinalStatus)
	}
	if trace.AttemptCount != 3 {
		t.Errorf("AttemptCount = %d, want 3", trace.AttemptCount)
	}
}

// A route the chain skips without calling it — an open circuit here — is on
// the timeline, but the first route is never a fallback: nothing preceded it.
func TestTraceSkippedRouteKeepsChainPosition(t *testing.T) {
	store := storage.NewMemory()
	fail := func(name string) *fakeProvider {
		return &fakeProvider{name: name, results: []error{upstreamErr(503, provider.ErrUpstream5xx)}}
	}

	e := newTestEngine(t, newRecorder(), fail("openai"), fail("openrouter"), fail("ollama"))
	e.SetTraceStore(store.Traces())
	e.setBreakerPolicy(BreakerPolicy{FailureThreshold: 1, Cooldown: time.Minute})

	// The first request trips every provider; the second finds them all open.
	if _, err := e.ChatCompletion(tracedContext("req-warm"), &openai.ChatCompletionRequest{Model: "gpt-5"}); err == nil {
		t.Fatal("expected the warm-up request to fail")
	}
	if _, err := e.ChatCompletion(tracedContext("req-open"), &openai.ChatCompletionRequest{Model: "gpt-5"}); err == nil {
		t.Fatal("expected the second request to fail with every circuit open")
	}

	trace := traceOf(t, store.Traces(), "req-open")
	if len(trace.Attempts) != 3 {
		t.Fatalf("len(Attempts) = %d, want one skipped route each", len(trace.Attempts))
	}
	for _, a := range trace.Attempts {
		if a.Status != "circuit_open" {
			t.Errorf("attempt on %s status = %q, want circuit_open", a.Provider, a.Status)
		}
		if a.Latency != 0 {
			t.Errorf("skipped route on %s reported latency %v, want 0", a.Provider, a.Latency)
		}
	}
	if trace.Attempts[0].Fallback {
		t.Error("the first route was marked a fallback; nothing preceded it")
	}
	for _, a := range trace.Attempts[1:] {
		if !a.Fallback {
			t.Errorf("route on %s was reached by falling back but is not marked", a.Provider)
		}
	}
}

// Every route failing must still leave one trace, carrying the failure class
// of the last attempt.
func TestTraceExhaustedChainRecordsFailure(t *testing.T) {
	store := storage.NewMemory()
	fail := func(name string) *fakeProvider {
		return &fakeProvider{name: name, results: []error{upstreamErr(500, provider.ErrUpstream5xx)}}
	}

	e := newTestEngine(t, newRecorder(), fail("openai"), fail("openrouter"), fail("ollama"))
	e.SetTraceStore(store.Traces())

	if _, err := e.ChatCompletion(tracedContext("req-dead"), &openai.ChatCompletionRequest{Model: "gpt-5"}); err == nil {
		t.Fatal("ChatCompletion succeeded, want the chain to be exhausted")
	}

	trace := traceOf(t, store.Traces(), "req-dead")
	if trace.FinalStatus != "upstream_5xx" {
		t.Errorf("FinalStatus = %q, want upstream_5xx", trace.FinalStatus)
	}
	if len(trace.Attempts) != 3 {
		t.Fatalf("len(Attempts) = %d, want one per route", len(trace.Attempts))
	}
	if trace.Attempts[0].Fallback {
		t.Error("first attempt must not be marked as a fallback")
	}
	for _, a := range trace.Attempts[1:] {
		if !a.Fallback {
			t.Errorf("attempt on %s reached by fallback but not marked", a.Provider)
		}
	}
}

// A streamed request commits its trace when the relay drains, not when the
// channel is handed back.
func TestTraceStreamRecordsAfterRelayDrains(t *testing.T) {
	store := storage.NewMemory()
	e := newTestEngine(t, newRecorder(), &fakeProvider{name: "openai"},
		&fakeProvider{name: "openrouter"}, &fakeProvider{name: "ollama"})
	e.SetTraceStore(store.Traces())

	chunks, err := e.ChatCompletionStream(tracedContext("req-stream"),
		&openai.ChatCompletionRequest{Model: "gpt-5", Stream: true})
	if err != nil {
		t.Fatalf("ChatCompletionStream: %v", err)
	}
	var relayed int
	for range chunks {
		relayed++
	}
	if relayed == 0 {
		t.Fatal("stream relayed no chunks")
	}

	// The metering goroutine finishes just after the channel closes.
	var trace storage.RequestTrace
	for range 100 {
		if trace, err = store.Traces().Get(context.Background(), "req-stream"); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("stream trace never landed: %v", err)
	}
	if !trace.Streamed {
		t.Error("Streamed = false, want true")
	}
	if trace.FinalStatus != "ok" {
		t.Errorf("FinalStatus = %q, want ok", trace.FinalStatus)
	}
	if len(trace.Attempts) != 1 {
		t.Errorf("len(Attempts) = %d, want 1", len(trace.Attempts))
	}
}

// Without a request id there is nothing to key a trace by, so the engine must
// not write one.
func TestTraceSkippedWithoutRequestID(t *testing.T) {
	store := storage.NewMemory()
	e := newTestEngine(t, newRecorder(), &fakeProvider{name: "openai"},
		&fakeProvider{name: "openrouter"}, &fakeProvider{name: "ollama"})
	e.SetTraceStore(store.Traces())

	if _, err := e.ChatCompletion(context.Background(), &openai.ChatCompletionRequest{Model: "gpt-5"}); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	_, total, err := store.Traces().List(context.Background(), storage.TraceFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 0 {
		t.Errorf("total = %d, want 0 traces for an untraced request", total)
	}
}

// A combo request reports the member it actually routed to, so the trace
// distinguishes the requested name from the alias that served it.
func TestTraceComboRecordsChosenMember(t *testing.T) {
	store := storage.NewMemory()
	primary := &fakeProvider{name: "openai", results: []error{upstreamErr(500, provider.ErrUpstream5xx)}}
	secondary := &fakeProvider{name: "openrouter"}

	e := newTestEngine(t, newRecorder(), primary, secondary, &fakeProvider{name: "ollama"})
	e.SetTraceStore(store.Traces())
	e.Reload(
		[]storage.ModelAlias{
			{Alias: "gpt-5", Provider: "openai", Model: "gpt-5-upstream"},
			{Alias: "fast", Provider: "openrouter", Model: "gpt-5-mini"},
		},
		[]storage.Combo{{
			Name:     "smart",
			Strategy: storage.ComboFallback,
			Members:  []string{"gpt-5", "fast"},
			Enabled:  true,
		}},
		provider.NewRegistry(primary, secondary),
	)

	if _, err := e.ChatCompletion(tracedContext("req-combo"), &openai.ChatCompletionRequest{Model: "smart"}); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	trace := traceOf(t, store.Traces(), "req-combo")
	if trace.Model != "smart" {
		t.Errorf("Model = %q, want the requested combo name smart", trace.Model)
	}
	if trace.FinalAlias != "fast" {
		t.Errorf("FinalAlias = %q, want the member that served it", trace.FinalAlias)
	}
	if len(trace.Attempts) != 2 {
		t.Fatalf("len(Attempts) = %d, want 2", len(trace.Attempts))
	}
	if trace.Attempts[0].Alias != "gpt-5" || trace.Attempts[1].Alias != "fast" {
		t.Errorf("aliases = %s,%s, want gpt-5,fast",
			trace.Attempts[0].Alias, trace.Attempts[1].Alias)
	}
}
