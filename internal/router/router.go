// Package router is the routing engine: it resolves model aliases to a
// provider, applies the configured fallback chain, and dispatches requests.
// Its routing table comes from storage, never from HTTP.
package router

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync/atomic"
	"time"

	"github.com/zealish/zealish-router/internal/provider"
	"github.com/zealish/zealish-router/internal/storage"
	"github.com/zealish/zealish-router/pkg/openai"
)

// ErrUnknownModel is returned when a model alias has no configured route.
var ErrUnknownModel = errors.New("router: unknown model")

// ErrChainExhausted is returned when every route in a fallback chain failed.
// It wraps the last upstream error.
var ErrChainExhausted = errors.New("router: fallback chain exhausted")

// Retry bounds a single provider's attempts before the chain advances.
type Retry struct {
	// Attempts is the total number of tries per provider, including the first.
	Attempts int
	// BaseDelay is the first backoff interval; it doubles per retry.
	BaseDelay time.Duration
	// MaxDelay caps the backoff interval.
	MaxDelay time.Duration
}

// DefaultRetry is the policy used when none is injected.
var DefaultRetry = Retry{Attempts: 2, BaseDelay: 200 * time.Millisecond, MaxDelay: 2 * time.Second}

// maxChainAttempts caps how many routes a single request may try, independent
// of configuration, as a last line of defence against pathological chains.
const maxChainAttempts = 8

// Recorder receives routing telemetry. The metrics package implements it.
type Recorder interface {
	RecordProviderError(provider, reason string)
	RecordTokens(provider, model string, prompt, completion int)
}

// Route is a resolved alias target.
type Route struct {
	Alias    string
	Provider string
	Model    string
}

// routes is the hot-swappable half of the engine: everything derived from
// stored routing records. It is replaced wholesale on reload, never mutated
// in place.
type routes struct {
	models   map[string]storage.ModelAlias
	combos   map[string]storage.Combo
	cursors  map[string]*atomic.Uint64
	registry *provider.Registry
}

// Engine dispatches chat completions to providers using alias and fallback
// configuration. Its routing table may be swapped at runtime; the request path
// takes a single atomic load and never a lock.
type Engine struct {
	routes   atomic.Pointer[routes]
	logger   *slog.Logger
	recorder Recorder
	usage    storage.UsageStore
	retry    Retry
}

// usageWriteTimeout bounds a single usage-log insert. It runs off the request's
// context so a finished stream still records after the client disconnects.
const usageWriteTimeout = 5 * time.Second

// NewEngine constructs a routing engine with an empty routing table. A nil
// recorder disables telemetry; Reload installs the first table.
func NewEngine(logger *slog.Logger, rec Recorder) *Engine {
	e := &Engine{
		logger:   logger,
		recorder: rec,
		retry:    DefaultRetry,
	}
	e.Reload(nil, nil, provider.NewRegistry())
	return e
}

// SetUsageStore installs the durable usage log. A nil store disables usage
// persistence, which is what tests and the metrics-only path want.
func (e *Engine) SetUsageStore(s storage.UsageStore) {
	e.usage = s
}

// Reload swaps the routing table for one built from the given aliases, combos
// and provider registry. In-flight requests keep the snapshot they started
// with; round-robin cursors start over.
func (e *Engine) Reload(aliases []storage.ModelAlias, combos []storage.Combo, registry *provider.Registry) {
	models := make(map[string]storage.ModelAlias, len(aliases))
	for _, m := range aliases {
		models[m.Alias] = m
	}

	pools := make(map[string]storage.Combo, len(combos))
	cursors := make(map[string]*atomic.Uint64, len(combos))
	for _, c := range combos {
		if !c.Enabled || len(c.Members) == 0 {
			continue
		}
		// An alias of the same name wins: it is the concrete route, and a
		// combo shadowing it would make the alias unreachable.
		if _, clash := models[c.Name]; clash {
			continue
		}
		pools[c.Name] = c
		cursors[c.Name] = &atomic.Uint64{}
	}
	e.routes.Store(&routes{models: models, combos: pools, cursors: cursors, registry: registry})
}

// Aliases returns every configured model alias.
func (e *Engine) Aliases() []string {
	rt := e.routes.Load()
	aliases := make([]string, 0, len(rt.models))
	for alias := range rt.models {
		aliases = append(aliases, alias)
	}
	return aliases
}

// Combos returns every routable combo name.
func (e *Engine) Combos() []string {
	rt := e.routes.Load()
	names := make([]string, 0, len(rt.combos))
	for name := range rt.combos {
		names = append(names, name)
	}
	return names
}

// Resolve maps a model name onto its provider and upstream model name. For a
// combo it reports the route its chain would try first.
func (e *Engine) Resolve(name string) (Route, error) {
	return e.routes.Load().resolve(name)
}

func (rt *routes) resolve(name string) (Route, error) {
	m, ok := rt.models[name]
	if !ok {
		// A combo is not a route of its own; it reports its first member, so
		// callers that only want a concrete target get one.
		if combo, isCombo := rt.combos[name]; isCombo {
			m, ok = rt.models[combo.Members[0]]
			name = combo.Members[0]
		}
		if !ok {
			return Route{}, fmt.Errorf("%w: %s", ErrUnknownModel, name)
		}
	}
	return Route{Alias: name, Provider: m.Provider, Model: m.Model}, nil
}

// Chain returns the deterministic attempt order for a model name: for an alias
// the alias itself followed by its configured fallbacks, for a combo its
// members in strategy order, each followed by its own fallbacks. Duplicates
// are removed and the total length capped.
func (e *Engine) Chain(name string) []string {
	return e.routes.Load().chain(name)
}

func (rt *routes) chain(name string) []string {
	chain := make([]string, 0, maxChainAttempts)
	seen := make(map[string]struct{}, maxChainAttempts)

	appendCandidate := func(candidate string) bool {
		if _, dup := seen[candidate]; dup {
			return true
		}
		seen[candidate] = struct{}{}
		chain = append(chain, candidate)
		return len(chain) < maxChainAttempts
	}

	for _, member := range rt.entrypoints(name) {
		if !appendCandidate(member) {
			return chain
		}
		for _, fallback := range rt.models[member].Fallback {
			if !appendCandidate(fallback) {
				return chain
			}
		}
	}
	return chain
}

// entrypoints lists the routes a name starts from: one for a plain alias, the
// combo pool in strategy order for a combo.
func (rt *routes) entrypoints(name string) []string {
	combo, ok := rt.combos[name]
	if !ok {
		return []string{name}
	}
	if combo.Strategy != storage.ComboRoundRobin {
		return combo.Members
	}

	// Round-robin only moves the starting point: the rest of the pool still
	// follows in order, so a busy member is skipped rather than retried.
	n := len(combo.Members)
	start := int(rt.cursors[name].Add(1)-1) % n
	ordered := make([]string, 0, n)
	for i := range n {
		ordered = append(ordered, combo.Members[(start+i)%n])
	}
	return ordered
}

// ChatCompletion routes a non-streaming completion request, advancing through
// the fallback chain on retryable upstream failures.
func (e *Engine) ChatCompletion(ctx context.Context, req *openai.ChatCompletionRequest) (*openai.ChatCompletionResponse, error) {
	meta := metaFrom(ctx, time.Now())
	return dispatch(ctx, e, req, false, meta, func(p provider.Provider, route Route, upstream *openai.ChatCompletionRequest) (*openai.ChatCompletionResponse, error) {
		resp, err := p.ChatCompletion(ctx, upstream)
		if err != nil {
			return nil, err
		}
		e.recordUsage(route, usageOf(resp.Usage, upstream, resp.Choices), false, meta)
		return resp, nil
	})
}

// ChatCompletionStream routes a streaming completion request. Fallback applies
// only while establishing the stream: once the channel is handed back the first
// chunk may already be in flight, so the response is committed.
func (e *Engine) ChatCompletionStream(ctx context.Context, req *openai.ChatCompletionRequest) (<-chan openai.StreamChunk, error) {
	meta := metaFrom(ctx, time.Now())
	return dispatch(ctx, e, req, true, meta, func(p provider.Provider, route Route, upstream *openai.ChatCompletionRequest) (<-chan openai.StreamChunk, error) {
		chunks, err := p.ChatCompletionStream(ctx, upstream)
		if err != nil {
			return nil, err
		}
		return e.meterStream(ctx, route, upstream, chunks, meta), nil
	})
}

// dispatch walks the fallback chain for req.Model, retrying each provider
// according to the engine policy, and returns the first successful result.
// A request that fails after reaching at least one provider is appended to the
// usage log with its failure class so totals count every hit on a model.
func dispatch[T any](ctx context.Context, e *Engine, req *openai.ChatCompletionRequest, streamed bool, meta callMeta, call func(provider.Provider, Route, *openai.ChatCompletionRequest) (T, error)) (T, error) {
	var zero T

	// Pin one snapshot for the whole walk: a reload mid-chain must not move
	// the aliases out from under this request.
	rt := e.routes.Load()
	chain := rt.chain(req.Model)
	var lastErr error
	var lastRoute Route
	attempted := false

	for _, alias := range chain {
		p, route, err := rt.pick(alias)
		if err != nil {
			if alias == req.Model {
				// The requested alias itself is unroutable: terminal. A combo
				// never matches here, so its members are all skippable.
				return zero, err
			}
			e.logger.Warn("skipping unroutable route",
				slog.String("requested", req.Model),
				slog.String("alias", alias),
				slog.Any("error", err))
			lastErr = err
			continue
		}

		upstream := *req
		upstream.Model = route.Model

		lastRoute, attempted = route, true

		result, err := attempt(ctx, e, route, func() (T, error) { return call(p, route, &upstream) })
		if err == nil {
			return result, nil
		}
		lastErr = err

		if !provider.Retryable(err) {
			e.recordFailure(route, streamed, classify(err), meta)
			return zero, err
		}
		if ctx.Err() != nil {
			e.recordFailure(route, streamed, classify(ctx.Err()), meta)
			return zero, ctx.Err()
		}
	}

	if lastErr == nil {
		return zero, fmt.Errorf("%w: %s", ErrUnknownModel, req.Model)
	}
	if attempted {
		e.recordFailure(lastRoute, streamed, classify(lastErr), meta)
	}
	return zero, fmt.Errorf("%w: %w", ErrChainExhausted, lastErr)
}

// attempt runs call against one route with bounded retries and exponential
// backoff plus jitter. It records and logs every failed try.
func attempt[T any](ctx context.Context, e *Engine, route Route, call func() (T, error)) (T, error) {
	var zero T

	attempts := max(e.retry.Attempts, 1)
	var lastErr error

	for i := 1; i <= attempts; i++ {
		result, err := call()
		if err == nil {
			return result, nil
		}
		lastErr = err

		reason := classify(err)
		e.record(route.Provider, reason)
		e.logger.Warn("provider attempt failed",
			slog.String("alias", route.Alias),
			slog.String("provider", route.Provider),
			slog.String("model", route.Model),
			slog.Int("attempt", i),
			slog.String("reason", reason),
			slog.Any("error", err))

		if !provider.Retryable(err) || i == attempts {
			return zero, err
		}
		if err := sleep(ctx, e.backoff(i)); err != nil {
			return zero, err
		}
	}
	return zero, lastErr
}

// backoff returns the delay before retry number n (1-based), doubling from
// BaseDelay up to MaxDelay. Jitter spreads the result over [delay/2, delay] so
// the cap is never exceeded.
func (e *Engine) backoff(n int) time.Duration {
	delay := e.retry.BaseDelay
	if delay <= 0 {
		return 0
	}
	for range n - 1 {
		delay *= 2
		if e.retry.MaxDelay > 0 && delay >= e.retry.MaxDelay {
			delay = e.retry.MaxDelay
			break
		}
	}
	half := int64(delay) / 2
	if half <= 0 {
		return delay
	}
	return time.Duration(half + rand.Int64N(half+1))
}

func (e *Engine) record(providerName, reason string) {
	if e.recorder == nil {
		return
	}
	e.recorder.RecordProviderError(providerName, reason)
}

func (rt *routes) pick(alias string) (provider.Provider, Route, error) {
	route, err := rt.resolve(alias)
	if err != nil {
		return nil, Route{}, err
	}
	p, err := rt.registry.Get(route.Provider)
	if err != nil {
		return nil, Route{}, err
	}
	return p, route, nil
}

// classify maps an upstream error onto a stable metric label.
func classify(err error) string {
	switch {
	case errors.Is(err, provider.ErrTimeout):
		return "timeout"
	case errors.Is(err, provider.ErrRateLimited):
		return "rate_limited"
	case errors.Is(err, provider.ErrUpstream5xx):
		return "upstream_5xx"
	case errors.Is(err, provider.ErrConnection):
		return "connection"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "client_error"
	}
}

// sleep waits for d, aborting early if ctx is done.
func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
