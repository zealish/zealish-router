package router

import (
	"errors"
	"sync"
	"time"
)

// ErrCircuitOpen is returned when a route is skipped because its provider's
// circuit is open. It is never retryable: the chain advances immediately
// instead of paying the provider's full timeout again.
var ErrCircuitOpen = errors.New("router: circuit open")

// CircuitState is the phase of one provider's breaker.
type CircuitState string

const (
	// CircuitClosed passes every request through; the provider is healthy.
	CircuitClosed CircuitState = "closed"
	// CircuitOpen rejects every request until the cooldown elapses.
	CircuitOpen CircuitState = "open"
	// CircuitHalfOpen admits a single probe to test recovery.
	CircuitHalfOpen CircuitState = "half_open"
)

// BreakerPolicy bounds how quickly a provider is taken out of rotation and how
// soon it is offered a probe.
type BreakerPolicy struct {
	// FailureThreshold is the number of consecutive retryable failures that
	// trips the circuit. Zero or less disables the breaker entirely.
	FailureThreshold int
	// Cooldown is how long the circuit stays open before a probe is admitted.
	Cooldown time.Duration
}

// DefaultBreaker is the policy used when none is injected. Five consecutive
// failures is past any plausible transient blip, and thirty seconds is short
// enough that a recovered provider returns to rotation quickly.
var DefaultBreaker = BreakerPolicy{FailureThreshold: 5, Cooldown: 30 * time.Second}

// breaker is one provider's state. Transitions are:
//
//	closed --threshold consecutive failures--> open
//	open --cooldown elapsed--> half-open (one probe admitted)
//	half-open --probe succeeds--> closed
//	half-open --probe fails--> open (cooldown restarts)
type breaker struct {
	failures int
	state    CircuitState
	// openedAt is when the circuit last tripped; the cooldown runs from it.
	openedAt time.Time
	// probing is true while a half-open probe is in flight, so concurrent
	// requests do not all pile onto a provider that is still down.
	probing bool
}

// breakers tracks provider health across reloads. Health is a property of the
// upstream, not of the routing table, so it deliberately survives a Reload.
type breakers struct {
	// now is injectable so tests can drive the cooldown without sleeping.
	now func() time.Time
	// onTransition, when set, is called after every state change. It runs
	// under the lock, so it must not call back into the breaker.
	onTransition func(provider string, state CircuitState)

	mu sync.Mutex
	// policy is the default applied to any provider without an override.
	policy BreakerPolicy
	// overrides are per-provider policies, republished on every Reload.
	overrides map[string]BreakerPolicy
	state     map[string]*breaker
}

func newBreakers(policy BreakerPolicy) *breakers {
	return &breakers{
		now:       time.Now,
		policy:    policy,
		overrides: make(map[string]BreakerPolicy),
		state:     make(map[string]*breaker),
	}
}

// setOverrides republishes the per-provider policies. Health is preserved: a
// provider that is currently open stays open under its new policy, because the
// upstream did not become healthy just because its threshold was retuned.
func (b *breakers) setOverrides(overrides map[string]BreakerPolicy) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.overrides = overrides
}

// policyFor resolves the policy governing a provider. The caller must hold
// b.mu.
func (b *breakers) policyFor(name string) BreakerPolicy {
	if p, ok := b.overrides[name]; ok {
		return p
	}
	return b.policy
}

// transition moves a breaker to a new state and reports it. The caller must
// hold b.mu. Repeating the current state is not reported.
func (b *breakers) transition(name string, br *breaker, state CircuitState) {
	if br.state == state {
		return
	}
	br.state = state
	if b.onTransition != nil {
		b.onTransition(name, state)
	}
}

// setBreakerPolicy rebuilds the engine's breakers under a new default policy,
// keeping the transition hook wired. Health is reset: the policy that produced
// the old counts no longer applies.
func (e *Engine) setBreakerPolicy(policy BreakerPolicy) *breakers {
	b := newBreakers(policy)
	b.onTransition = e.onCircuitChange
	e.breakers = b
	return b
}

// SetBreakerPolicy installs the default circuit breaker policy, applied to
// every provider without an override. Existing health is preserved.
func (e *Engine) SetBreakerPolicy(policy BreakerPolicy) {
	e.breakers.mu.Lock()
	defer e.breakers.mu.Unlock()
	e.breakers.policy = policy
}

// BreakerPolicy returns the default circuit breaker policy.
func (e *Engine) BreakerPolicy() BreakerPolicy {
	e.breakers.mu.Lock()
	defer e.breakers.mu.Unlock()
	return e.breakers.policy
}

// SetBreakerOverrides republishes the per-provider policies. The loader calls
// it on every reload, so a provider retuned through the admin API takes effect
// without a restart.
func (e *Engine) SetBreakerOverrides(overrides map[string]BreakerPolicy) {
	e.breakers.setOverrides(overrides)
}

// get returns the breaker for a provider, creating a closed one on first use.
// The caller must hold b.mu.
func (b *breakers) get(name string) *breaker {
	br, ok := b.state[name]
	if !ok {
		br = &breaker{state: CircuitClosed}
		b.state[name] = br
	}
	return br
}

// allow reports whether a request may be sent to the provider. An open circuit
// whose cooldown has elapsed transitions to half-open and admits exactly one
// probe; further callers are rejected until that probe reports back.
func (b *breakers) allow(name string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	policy := b.policyFor(name)
	if policy.FailureThreshold <= 0 {
		return true
	}

	br := b.get(name)
	switch br.state {
	case CircuitOpen:
		if b.now().Sub(br.openedAt) < policy.Cooldown {
			return false
		}
		b.transition(name, br, CircuitHalfOpen)
		br.probing = true
		return true
	case CircuitHalfOpen:
		// A probe is already in flight; everyone else keeps failing fast.
		if br.probing {
			return false
		}
		br.probing = true
		return true
	default:
		return true
	}
}

// success reports a completed call. It closes a half-open circuit and clears
// any accumulated failures.
func (b *breakers) success(name string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.policyFor(name).FailureThreshold <= 0 {
		return
	}

	br := b.get(name)
	br.failures = 0
	br.probing = false
	b.transition(name, br, CircuitClosed)
}

// failure reports a retryable upstream failure. It trips the circuit once the
// threshold is reached, and re-opens it when a half-open probe fails.
func (b *breakers) failure(name string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	policy := b.policyFor(name)
	if policy.FailureThreshold <= 0 {
		return
	}

	br := b.get(name)
	br.failures++

	// A failed probe means the provider is still down: reopen without waiting
	// for the threshold to be reached all over again.
	if br.state == CircuitHalfOpen || br.failures >= policy.FailureThreshold {
		br.openedAt = b.now()
		br.probing = false
		// A failed probe re-opens a circuit that is already open in the
		// caller's eyes, so force the report rather than relying on a state
		// change: the cooldown restarting is itself news.
		br.state = CircuitOpen
		if b.onTransition != nil {
			b.onTransition(name, CircuitOpen)
		}
	}
}

// ProviderHealth is a point-in-time read of one provider's breaker, for the
// admin API and the dashboard.
type ProviderHealth struct {
	Provider string       `json:"provider"`
	State    CircuitState `json:"state"`
	Failures int          `json:"failures"`
	// OpenedAt is when the circuit last tripped; zero while it has never
	// tripped or has since closed.
	OpenedAt time.Time `json:"opened_at,omitempty"`
	// RetryAt is when the next probe is admitted. Zero unless open.
	RetryAt time.Time `json:"retry_at,omitempty"`
}

// snapshot reports the state of every provider the breaker has observed.
func (b *breakers) snapshot() map[string]ProviderHealth {
	b.mu.Lock()
	defer b.mu.Unlock()

	out := make(map[string]ProviderHealth, len(b.state))
	for name, br := range b.state {
		health := ProviderHealth{Provider: name, State: br.state, Failures: br.failures}
		if br.state == CircuitOpen {
			health.OpenedAt = br.openedAt
			health.RetryAt = br.openedAt.Add(b.policyFor(name).Cooldown)
		}
		out[name] = health
	}
	return out
}
