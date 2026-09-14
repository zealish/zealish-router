package router

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zealish/zealish-router/internal/provider"
	"github.com/zealish/zealish-router/pkg/openai"
)

// fakeClock drives breaker cooldowns without sleeping.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestBreakers(policy BreakerPolicy) (*breakers, *fakeClock) {
	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	b := newBreakers(policy)
	b.now = clock.now
	return b, clock
}

func TestBreakerTripsAfterThreshold(t *testing.T) {
	b, _ := newTestBreakers(BreakerPolicy{FailureThreshold: 3, Cooldown: time.Minute})

	for i := range 2 {
		b.failure("openai")
		if !b.allow("openai") {
			t.Fatalf("circuit opened after %d failures, want 3", i+1)
		}
	}

	b.failure("openai")
	if b.allow("openai") {
		t.Fatal("circuit still closed after reaching the threshold")
	}
	if got := b.snapshot()["openai"].State; got != CircuitOpen {
		t.Errorf("state = %q, want %q", got, CircuitOpen)
	}
}

func TestBreakerSuccessResetsFailures(t *testing.T) {
	b, _ := newTestBreakers(BreakerPolicy{FailureThreshold: 3, Cooldown: time.Minute})

	b.failure("openai")
	b.failure("openai")
	b.success("openai")
	b.failure("openai")
	b.failure("openai")

	if !b.allow("openai") {
		t.Fatal("non-consecutive failures tripped the circuit")
	}
}

func TestBreakerHalfOpenAdmitsOneProbe(t *testing.T) {
	b, clock := newTestBreakers(BreakerPolicy{FailureThreshold: 1, Cooldown: 30 * time.Second})

	b.failure("openai")
	if b.allow("openai") {
		t.Fatal("open circuit admitted a request")
	}

	clock.advance(29 * time.Second)
	if b.allow("openai") {
		t.Fatal("circuit admitted a probe before the cooldown elapsed")
	}

	clock.advance(2 * time.Second)
	if !b.allow("openai") {
		t.Fatal("circuit refused the probe after the cooldown elapsed")
	}
	if b.allow("openai") {
		t.Fatal("circuit admitted a second concurrent probe")
	}
	if got := b.snapshot()["openai"].State; got != CircuitHalfOpen {
		t.Errorf("state = %q, want %q", got, CircuitHalfOpen)
	}
}

func TestBreakerProbeSuccessCloses(t *testing.T) {
	b, clock := newTestBreakers(BreakerPolicy{FailureThreshold: 1, Cooldown: time.Second})

	b.failure("openai")
	clock.advance(2 * time.Second)
	if !b.allow("openai") {
		t.Fatal("probe not admitted")
	}

	b.success("openai")
	health := b.snapshot()["openai"]
	if health.State != CircuitClosed {
		t.Errorf("state = %q, want %q", health.State, CircuitClosed)
	}
	if health.Failures != 0 {
		t.Errorf("failures = %d, want 0", health.Failures)
	}
	if !b.allow("openai") || !b.allow("openai") {
		t.Error("closed circuit rejected a request")
	}
}

func TestBreakerProbeFailureReopensImmediately(t *testing.T) {
	b, clock := newTestBreakers(BreakerPolicy{FailureThreshold: 5, Cooldown: time.Second})

	for range 5 {
		b.failure("openai")
	}
	clock.advance(2 * time.Second)
	if !b.allow("openai") {
		t.Fatal("probe not admitted")
	}

	// One failed probe is enough: the threshold does not have to be met again.
	b.failure("openai")
	if b.allow("openai") {
		t.Fatal("circuit did not reopen after a failed probe")
	}

	// And the cooldown restarts from the failed probe.
	clock.advance(2 * time.Second)
	if !b.allow("openai") {
		t.Error("cooldown did not restart from the failed probe")
	}
}

func TestBreakerDisabledByNonPositiveThreshold(t *testing.T) {
	b, _ := newTestBreakers(BreakerPolicy{FailureThreshold: 0, Cooldown: time.Minute})

	for range 100 {
		b.failure("openai")
	}
	if !b.allow("openai") {
		t.Error("disabled breaker blocked a request")
	}
}

func TestBreakerIsolatesProviders(t *testing.T) {
	b, _ := newTestBreakers(BreakerPolicy{FailureThreshold: 2, Cooldown: time.Minute})

	b.failure("openai")
	b.failure("openai")

	if b.allow("openai") {
		t.Error("openai circuit did not open")
	}
	if !b.allow("openrouter") {
		t.Error("a healthy provider was taken down with a failing one")
	}
}

func TestBreakerSnapshotReportsRetryAt(t *testing.T) {
	b, clock := newTestBreakers(BreakerPolicy{FailureThreshold: 1, Cooldown: 30 * time.Second})

	b.failure("openai")
	health := b.snapshot()["openai"]

	if want := clock.now(); !health.OpenedAt.Equal(want) {
		t.Errorf("OpenedAt = %v, want %v", health.OpenedAt, want)
	}
	if want := clock.now().Add(30 * time.Second); !health.RetryAt.Equal(want) {
		t.Errorf("RetryAt = %v, want %v", health.RetryAt, want)
	}
}

// --- engine integration ---

func TestDispatchSkipsOpenCircuitWithoutCallingProvider(t *testing.T) {
	primary := &fakeProvider{name: "openai", results: []error{upstreamErr(500, provider.ErrUpstream5xx)}}
	secondary := &fakeProvider{name: "openrouter"}

	e := newTestEngine(t, nil, primary, secondary, &fakeProvider{name: "ollama"})
	e.setBreakerPolicy(BreakerPolicy{FailureThreshold: 1, Cooldown: time.Minute})

	// First request trips openai and falls back to openrouter.
	if _, err := e.ChatCompletion(context.Background(), &openai.ChatCompletionRequest{Model: "gpt-5"}); err != nil {
		t.Fatalf("first request: %v", err)
	}
	tripped := primary.callCount()

	// The second request must not touch openai at all.
	if _, err := e.ChatCompletion(context.Background(), &openai.ChatCompletionRequest{Model: "gpt-5"}); err != nil {
		t.Fatalf("second request: %v", err)
	}
	if got := primary.callCount(); got != tripped {
		t.Errorf("open circuit still called the provider: %d calls, want %d", got, tripped)
	}
	if got := e.Health()["openai"].State; got != CircuitOpen {
		t.Errorf("state = %q, want %q", got, CircuitOpen)
	}
}

func TestDispatchReportsCircuitOpenWhenEveryRouteIsDown(t *testing.T) {
	fail := func(name string) *fakeProvider {
		return &fakeProvider{name: name, results: []error{upstreamErr(503, provider.ErrUpstream5xx)}}
	}
	e := newTestEngine(t, nil, fail("openai"), fail("openrouter"), fail("ollama"))
	e.setBreakerPolicy(BreakerPolicy{FailureThreshold: 1, Cooldown: time.Minute})

	// Trip every provider in the chain.
	if _, err := e.ChatCompletion(context.Background(), &openai.ChatCompletionRequest{Model: "gpt-5"}); err == nil {
		t.Fatal("expected the chain to fail")
	}

	_, err := e.ChatCompletion(context.Background(), &openai.ChatCompletionRequest{Model: "gpt-5"})
	if err == nil {
		t.Fatal("expected an error with every circuit open")
	}
	if !errors.Is(err, ErrCircuitOpen) {
		t.Errorf("error = %v, want it to wrap ErrCircuitOpen", err)
	}
	if !errors.Is(err, ErrChainExhausted) {
		t.Errorf("error = %v, want it to wrap ErrChainExhausted", err)
	}
}

func TestDispatchClientErrorDoesNotTripCircuit(t *testing.T) {
	// A 400 is the caller's fault; it must never take a provider out.
	primary := &fakeProvider{name: "openai", results: []error{upstreamErr(400, nil)}}
	e := newTestEngine(t, nil, primary, &fakeProvider{name: "openrouter"}, &fakeProvider{name: "ollama"})
	e.setBreakerPolicy(BreakerPolicy{FailureThreshold: 1, Cooldown: time.Minute})

	if _, err := e.ChatCompletion(context.Background(), &openai.ChatCompletionRequest{Model: "gpt-5"}); err == nil {
		t.Fatal("expected the terminal 4xx to be returned")
	}
	if got := e.Health()["openai"].State; got == CircuitOpen {
		t.Error("a client error tripped the circuit")
	}
}

func TestBreakerSurvivesReload(t *testing.T) {
	primary := &fakeProvider{name: "openai", results: []error{upstreamErr(500, provider.ErrUpstream5xx)}}
	e := newTestEngine(t, nil, primary, &fakeProvider{name: "openrouter"}, &fakeProvider{name: "ollama"})
	e.setBreakerPolicy(BreakerPolicy{FailureThreshold: 1, Cooldown: time.Minute})

	if _, err := e.ChatCompletion(context.Background(), &openai.ChatCompletionRequest{Model: "gpt-5"}); err != nil {
		t.Fatalf("first request: %v", err)
	}

	// Provider health is a property of the upstream, not of the routing table.
	e.Reload(nil, nil, provider.NewRegistry(primary))
	if got := e.Health()["openai"].State; got != CircuitOpen {
		t.Errorf("state after reload = %q, want %q", got, CircuitOpen)
	}
}

func TestBreakerReportsTransitionsToRecorder(t *testing.T) {
	rec := newRecorder()
	primary := &fakeProvider{name: "openai", results: []error{upstreamErr(500, provider.ErrUpstream5xx)}}
	e := newTestEngine(t, rec, primary, &fakeProvider{name: "openrouter"}, &fakeProvider{name: "ollama"})
	b := e.setBreakerPolicy(BreakerPolicy{FailureThreshold: 1, Cooldown: time.Second})
	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	b.now = clock.now

	if _, err := e.ChatCompletion(context.Background(), &openai.ChatCompletionRequest{Model: "gpt-5"}); err != nil {
		t.Fatalf("first request: %v", err)
	}
	if got := rec.circuit("openai"); got != string(CircuitOpen) {
		t.Errorf("reported state = %q, want %q", got, CircuitOpen)
	}

	// After the cooldown the probe succeeds and the circuit closes again.
	primary.results = []error{nil}
	clock.advance(2 * time.Second)
	if _, err := e.ChatCompletion(context.Background(), &openai.ChatCompletionRequest{Model: "gpt-5"}); err != nil {
		t.Fatalf("probe request: %v", err)
	}
	if got := rec.circuit("openai"); got != string(CircuitClosed) {
		t.Errorf("reported state = %q, want %q", got, CircuitClosed)
	}
}

func TestBreakerPerProviderOverride(t *testing.T) {
	b, _ := newTestBreakers(BreakerPolicy{FailureThreshold: 5, Cooldown: time.Minute})
	b.setOverrides(map[string]BreakerPolicy{
		"fragile": {FailureThreshold: 1, Cooldown: time.Second},
	})

	// The override trips after a single failure.
	b.failure("fragile")
	if b.allow("fragile") {
		t.Error("overridden provider did not trip at its own threshold")
	}

	// Everyone else still follows the default policy.
	b.failure("openai")
	if !b.allow("openai") {
		t.Error("default provider tripped at the override's threshold")
	}
}

func TestBreakerOverrideCanDisablePerProvider(t *testing.T) {
	b, _ := newTestBreakers(BreakerPolicy{FailureThreshold: 1, Cooldown: time.Minute})
	b.setOverrides(map[string]BreakerPolicy{"always-on": {FailureThreshold: 0}})

	for range 50 {
		b.failure("always-on")
	}
	if !b.allow("always-on") {
		t.Error("a provider with the breaker disabled was taken out of rotation")
	}
}

func TestBreakerOverrideCooldownUsedForRetryAt(t *testing.T) {
	b, clock := newTestBreakers(BreakerPolicy{FailureThreshold: 1, Cooldown: time.Hour})
	b.setOverrides(map[string]BreakerPolicy{
		"quick": {FailureThreshold: 1, Cooldown: 5 * time.Second},
	})

	b.failure("quick")
	if want := clock.now().Add(5 * time.Second); !b.snapshot()["quick"].RetryAt.Equal(want) {
		t.Errorf("RetryAt = %v, want %v", b.snapshot()["quick"].RetryAt, want)
	}

	// The override's shorter cooldown governs when the probe is admitted.
	clock.advance(6 * time.Second)
	if !b.allow("quick") {
		t.Error("probe not admitted after the override's cooldown")
	}
}

func TestBreakerOverridesPreserveHealthAcrossReload(t *testing.T) {
	b, _ := newTestBreakers(BreakerPolicy{FailureThreshold: 1, Cooldown: time.Minute})

	b.failure("openai")
	if b.allow("openai") {
		t.Fatal("circuit did not open")
	}

	// Retuning a provider does not make its upstream healthy again.
	b.setOverrides(map[string]BreakerPolicy{"openai": {FailureThreshold: 9, Cooldown: time.Minute}})
	if b.allow("openai") {
		t.Error("republishing overrides reset an open circuit")
	}
}
