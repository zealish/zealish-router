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
	RecordCircuitState(provider, state string)
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
	// engine backlinks to the live health and statistics the intelligent
	// strategy ranks on. Those outlive any single table, so they are read
	// through the engine rather than copied into the snapshot.
	engine *Engine
}

// Engine dispatches chat completions to providers using alias and fallback
// configuration. Its routing table may be swapped at runtime; the request path
// takes a single atomic load and never a lock.
type Engine struct {
	routes   atomic.Pointer[routes]
	logger   *slog.Logger
	recorder Recorder
	usage    storage.UsageStore
	traces   storage.TraceStore
	retry    Retry
	breakers *breakers
	probes   *probes
	stats    *Stats
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
		breakers: newBreakers(DefaultBreaker),
		probes:   newProbes(),
		stats:    NewStats(),
	}
	e.breakers.onTransition = e.onCircuitChange
	e.Reload(nil, nil, provider.NewRegistry())
	return e
}

// onCircuitChange logs and publishes a breaker transition. It runs under the
// breaker's lock, so it only touches the logger and the recorder.
func (e *Engine) onCircuitChange(providerName string, state CircuitState) {
	switch state {
	case CircuitOpen:
		e.logger.Error("provider circuit opened", slog.String("provider", providerName))
	case CircuitClosed:
		e.logger.Info("provider circuit closed", slog.String("provider", providerName))
	default:
		e.logger.Warn("provider circuit probing", slog.String("provider", providerName))
	}
	if e.recorder != nil {
		e.recorder.RecordCircuitState(providerName, string(state))
	}
}

// SetUsageStore installs the durable usage log. A nil store disables usage
// persistence, which is what tests and the metrics-only path want.
func (e *Engine) SetUsageStore(s storage.UsageStore) {
	e.usage = s
}

// SetTraceStore installs the durable request-trace log. A nil store disables
// tracing, which is what the metrics-only path and most tests want.
func (e *Engine) SetTraceStore(s storage.TraceStore) {
	e.traces = s
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
	e.routes.Store(&routes{models: models, combos: pools, cursors: cursors, registry: registry, engine: e})
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

// Health reports the circuit state of every provider the engine has observed.
// Providers that have never been called are absent: they are healthy by
// definition, and the caller knows the full provider list already. A provider
// that failed its last health probe is reported as open.
func (e *Engine) Health() map[string]ProviderHealth {
	out := e.breakers.snapshot()
	for name, err := range e.probes.snapshot() {
		h := out[name]
		h.Provider = name
		h.State = CircuitOpen
		h.LastProbeError = err.Error()
		out[name] = h
	}
	return out
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

// Capabilities reports what a routable name serves. An alias reports its own
// set; a combo reports the union of its members', since any member may take
// the request. An unknown or unclassified name reports nothing.
func (e *Engine) Capabilities(name string) []string {
	rt := e.routes.Load()
	if m, ok := rt.models[name]; ok {
		return provider.NormalizeCapabilities(m.Capabilities)
	}

	combo, ok := rt.combos[name]
	if !ok {
		return nil
	}
	union := make([]string, 0, len(combo.Members))
	for _, member := range combo.Members {
		union = append(union, rt.models[member].Capabilities...)
	}
	return provider.NormalizeCapabilities(union)
}

// Chain returns the deterministic attempt order for a model name.
func (e *Engine) Chain(name string) []string {
	return e.routes.Load().chain(name, RequestRequirements{})
}

func (rt *routes) chain(name string, req RequestRequirements) []string {
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

	entrypoints, err := rt.entrypoints(name, req)
	if err != nil {
		return nil
	}
	for _, member := range entrypoints {
		if !appendCandidate(member) {
			return chain
		}
		for _, fallback := range rt.models[member].Fallback {
			if combo, ok := rt.combos[name]; ok && combo.Strategy == storage.ComboIntelligent {
				m, exists := rt.models[fallback]
				if !exists || !candidateCompatible(m, req) {
					continue
				}
			}
			if !appendCandidate(fallback) {
				return chain
			}
		}
	}
	return chain
}

// entrypoints lists the routes a name starts from.
func (rt *routes) entrypoints(name string, req RequestRequirements) ([]string, error) {
	combo, ok := rt.combos[name]
	if !ok {
		return []string{name}, nil
	}
	switch combo.Strategy {
	case storage.ComboRoundRobin:
		return rotate(combo.Members, int(rt.cursors[name].Add(1)-1)%len(combo.Members)), nil
	case storage.ComboWeighted:
		return rotate(combo.Members, weightedPick(combo.Members, combo.Weights, rt.cursors[name].Add(1)-1)), nil
	case storage.ComboIntelligent:
		return rt.intelligentOrder(combo, req)
	default:
		return combo.Members, nil
	}
}

func rotate(members []string, start int) []string {
	n := len(members)
	ordered := make([]string, 0, n)
	for i := range n {
		ordered = append(ordered, members[(start+i)%n])
	}
	return ordered
}

func weightedPick(members []string, weights []int, tick uint64) int {
	total := 0
	for i := range members {
		total += weightAt(weights, i)
	}
	offset := int(tick % uint64(total))
	for i := range members {
		offset -= weightAt(weights, i)
		if offset < 0 {
			return i
		}
	}
	return 0
}

func weightAt(weights []int, i int) int {
	if i >= len(weights) || weights[i] < 1 {
		return 1
	}
	return weights[i]
}

// ChatCompletion routes a non-streaming completion request, advancing through
// the fallback chain on retryable upstream failures.
// ChatCompletion routes a non-streaming completion request, advancing through
// the fallback chain on retryable upstream failures.
func (e *Engine) ChatCompletion(ctx context.Context, req *openai.ChatCompletionRequest) (*openai.ChatCompletionResponse, error) {
	meta := beginRequest(ctx, req.Model, false)
	requirements := analyzeChatRequest(req)
	return dispatch(ctx, e, req.Model, false, requirements, meta, func(callCtx context.Context, _ measured, p provider.Provider, route Route) (*openai.ChatCompletionResponse, error) {
		upstream := *req
		upstream.Model = route.Model
		resp, err := p.ChatCompletion(callCtx, &upstream)
		if err != nil {
			return nil, err
		}
		e.recordUsage(route, usageOf(resp.Usage, &upstream, resp.Choices), false, meta)
		return resp, nil
	})

}

// ChatCompletionStream routes a streaming completion request. Fallback applies only while establishing the stream.
func (e *Engine) ChatCompletionStream(ctx context.Context, req *openai.ChatCompletionRequest) (<-chan openai.StreamChunk, error) {
	meta := beginRequest(ctx, req.Model, true)
	requirements := analyzeChatRequest(req)
	return dispatch(ctx, e, req.Model, true, requirements, meta, func(callCtx context.Context, m measured, p provider.Provider, route Route) (<-chan openai.StreamChunk, error) {
		upstream := *req
		upstream.Model = route.Model
		chunks, err := p.ChatCompletionStream(callCtx, &upstream)
		if err != nil {
			return nil, err
		}
		return e.meterLatency(ctx, route, m, e.meterStream(ctx, route, &upstream, chunks, meta)), nil
	})
}

// Embeddings routes an embeddings request through the same alias and fallback machinery.
func (e *Engine) Embeddings(ctx context.Context, req *openai.EmbeddingRequest) (*openai.EmbeddingResponse, error) {
	meta := beginRequest(ctx, req.Model, false)
	requirements := analyzeEmbeddingRequest(req)
	return dispatch(ctx, e, req.Model, false, requirements, meta, func(callCtx context.Context, _ measured, p provider.Provider, route Route) (*openai.EmbeddingResponse, error) {
		upstream := *req
		upstream.Model = route.Model
		resp, err := p.Embeddings(callCtx, &upstream)
		if err != nil {
			return nil, err
		}
		e.recordUsage(route, embeddingUsage(resp.Usage, &upstream), false, meta)
		return resp, nil
	})
}

// dispatch walks the request-aware fallback chain, retrying transient failures.
func dispatch[T any](ctx context.Context, e *Engine, model string, streamed bool, requirements RequestRequirements, meta callMeta, call func(context.Context, measured, provider.Provider, Route) (T, error)) (T, error) {
	var zero T
	rt := e.routes.Load()
	chain := rt.chain(model, requirements)
	if len(chain) == 0 {
		if combo, ok := rt.combos[model]; ok && combo.Strategy == storage.ComboIntelligent {
			_, err := rt.intelligentOrder(combo, requirements)
			if err != nil {
				e.finishTrace(meta.trace, classify(err))
				return zero, err
			}
		}
		return zero, fmt.Errorf("%w: %s", ErrUnknownModel, model)
	}
	var lastErr error
	var lastRoute Route
	attempted := false
	for i, alias := range chain {
		fallback := i > 0
		p, route, err := rt.pick(alias)
		if err != nil {
			if alias == model {
				// The requested alias itself is unroutable: terminal. A combo
				// never matches here, so its members are all skippable.
				e.finishTrace(meta.trace, classify(err))
				return zero, err
			}
			e.logger.Warn("skipping unroutable route",
				slog.String("requested", model),
				slog.String("alias", alias),
				slog.Any("error", err))
			lastErr = err
			continue
		}

		// A provider whose circuit is open is skipped without a call: the
		// whole point is not to pay its timeout again. The chain advances as
		// if the route had failed.
		if !e.breakers.allow(route.Provider) {
			e.logger.Warn("skipping route with open circuit",
				slog.String("requested", model),
				slog.String("alias", alias),
				slog.String("provider", route.Provider))
			e.record(route.Provider, "circuit_open")
			lastErr = fmt.Errorf("%w: %s", ErrCircuitOpen, route.Provider)
			meta.trace.skipped(route, "circuit_open", lastErr, fallback)
			continue
		}

		// A provider that failed its last background health probe is skipped
		// the same way: unhealthy means known-down, so waiting for this
		// request to fail would only add latency.
		if skipErr := e.skipUnhealthy(route.Provider); skipErr != nil {
			e.logger.Warn("skipping unhealthy route",
				slog.String("requested", model),
				slog.String("alias", alias),
				slog.String("provider", route.Provider))
			e.record(route.Provider, "unhealthy")
			lastErr = skipErr
			meta.trace.skipped(route, "unhealthy", skipErr, fallback)
			continue
		}

		lastRoute, attempted = route, true

		callCtx, m := measure(ctx)
		result, err := attempt(ctx, e, route, meta, fallback, func() (T, error) { return call(callCtx, m, p, route) })
		if err == nil {
			// A stream reports itself once it drains: its latency is the time
			// to the last token, not the time to open the channel. Its trace
			// closes with it, for the same reason.
			if !streamed {
				e.recordMetric(route, true, m.ttfb(), time.Since(m.start))
				e.finishTrace(meta.trace, "ok")
			}
			return result, nil
		}
		lastErr = err
		e.recordMetric(route, false, m.ttfb(), time.Since(m.start))

		if !provider.Retryable(err) {
			e.recordFailure(route, streamed, classify(err), meta)
			e.finishTrace(meta.trace, classify(err))
			return zero, err
		}
		if ctx.Err() != nil {
			e.recordFailure(route, streamed, classify(ctx.Err()), meta)
			e.finishTrace(meta.trace, classify(ctx.Err()))
			return zero, ctx.Err()
		}
	}

	if lastErr == nil {
		err := fmt.Errorf("%w: %s", ErrUnknownModel, model)
		e.finishTrace(meta.trace, classify(err))
		return zero, err
	}
	if attempted {
		e.recordFailure(lastRoute, streamed, classify(lastErr), meta)
	}
	e.finishTrace(meta.trace, classify(lastErr))
	if combo, ok := rt.combos[model]; ok && combo.Strategy == storage.ComboIntelligent {
		return zero, fmt.Errorf("%w: %w", ErrNoHealthyModel, lastErr)
	}
	return zero, fmt.Errorf("%w: %w", ErrChainExhausted, lastErr)
}

// attempt runs call against one route with bounded retries and exponential
// backoff plus jitter. It records and logs every failed try, and appends every
// try — successful or not — to the request's trace.
func attempt[T any](ctx context.Context, e *Engine, route Route, meta callMeta, fallback bool, call func() (T, error)) (T, error) {
	var zero T

	attempts := max(e.retry.Attempts, 1)
	var lastErr error

	for i := 1; i <= attempts; i++ {
		// Only the first try of the first route is neither: a repeat of this
		// route is a retry, and reaching this route at all may be a fallback.
		started := time.Now()
		result, err := call()
		if err == nil {
			e.breakers.success(route.Provider)
			meta.trace.attempt(route, started, time.Since(started), "ok", nil, i > 1, fallback)
			return result, nil
		}
		lastErr = err

		// Only transient upstream failures speak to provider health: a 4xx is
		// the caller's fault and must never take a provider out of rotation.
		if provider.Retryable(err) {
			e.breakers.failure(route.Provider)
		}

		reason := classify(err)
		e.record(route.Provider, reason)
		meta.trace.attempt(route, started, time.Since(started), reason, err, i > 1, fallback)
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
