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
	registry *provider.Registry
}

// Engine dispatches chat completions to providers using alias and fallback
// configuration. Its routing table may be swapped at runtime; the request path
// takes a single atomic load and never a lock.
type Engine struct {
	routes   atomic.Pointer[routes]
	logger   *slog.Logger
	recorder Recorder
	retry    Retry
}

// NewEngine constructs a routing engine with an empty routing table. A nil
// recorder disables telemetry; Reload installs the first table.
func NewEngine(logger *slog.Logger, rec Recorder) *Engine {
	e := &Engine{
		logger:   logger,
		recorder: rec,
		retry:    DefaultRetry,
	}
	e.Reload(nil, provider.NewRegistry())
	return e
}

// Reload swaps the routing table for one built from the given aliases and
// provider registry. In-flight requests keep the snapshot they started with.
func (e *Engine) Reload(aliases []storage.ModelAlias, registry *provider.Registry) {
	models := make(map[string]storage.ModelAlias, len(aliases))
	for _, m := range aliases {
		models[m.Alias] = m
	}
	e.routes.Store(&routes{models: models, registry: registry})
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

// Resolve maps an alias onto its provider and upstream model name.
func (e *Engine) Resolve(alias string) (Route, error) {
	return e.routes.Load().resolve(alias)
}

func (rt *routes) resolve(alias string) (Route, error) {
	m, ok := rt.models[alias]
	if !ok {
		return Route{}, fmt.Errorf("%w: %s", ErrUnknownModel, alias)
	}
	return Route{Alias: alias, Provider: m.Provider, Model: m.Model}, nil
}

// Chain returns the deterministic attempt order for an alias: the alias itself
// followed by its configured fallbacks, with duplicates removed and the total
// length capped.
func (e *Engine) Chain(alias string) []string {
	return e.routes.Load().chain(alias)
}

func (rt *routes) chain(alias string) []string {
	fallback := rt.models[alias].Fallback
	chain := make([]string, 0, 1+len(fallback))
	seen := make(map[string]struct{}, cap(chain))

	for _, candidate := range append([]string{alias}, fallback...) {
		if _, dup := seen[candidate]; dup {
			continue
		}
		seen[candidate] = struct{}{}
		chain = append(chain, candidate)
		if len(chain) == maxChainAttempts {
			break
		}
	}
	return chain
}

// ChatCompletion routes a non-streaming completion request, advancing through
// the fallback chain on retryable upstream failures.
func (e *Engine) ChatCompletion(ctx context.Context, req *openai.ChatCompletionRequest) (*openai.ChatCompletionResponse, error) {
	return dispatch(ctx, e, req, func(p provider.Provider, route Route, upstream *openai.ChatCompletionRequest) (*openai.ChatCompletionResponse, error) {
		resp, err := p.ChatCompletion(ctx, upstream)
		if err != nil {
			return nil, err
		}
		e.recordUsage(route, usageOf(resp.Usage, upstream, resp.Choices))
		return resp, nil
	})
}

// ChatCompletionStream routes a streaming completion request. Fallback applies
// only while establishing the stream: once the channel is handed back the first
// chunk may already be in flight, so the response is committed.
func (e *Engine) ChatCompletionStream(ctx context.Context, req *openai.ChatCompletionRequest) (<-chan openai.StreamChunk, error) {
	return dispatch(ctx, e, req, func(p provider.Provider, route Route, upstream *openai.ChatCompletionRequest) (<-chan openai.StreamChunk, error) {
		chunks, err := p.ChatCompletionStream(ctx, upstream)
		if err != nil {
			return nil, err
		}
		return e.meterStream(route, upstream, chunks), nil
	})
}

// dispatch walks the fallback chain for req.Model, retrying each provider
// according to the engine policy, and returns the first successful result.
func dispatch[T any](ctx context.Context, e *Engine, req *openai.ChatCompletionRequest, call func(provider.Provider, Route, *openai.ChatCompletionRequest) (T, error)) (T, error) {
	var zero T

	// Pin one snapshot for the whole walk: a reload mid-chain must not move
	// the aliases out from under this request.
	rt := e.routes.Load()
	chain := rt.chain(req.Model)
	var lastErr error

	for i, alias := range chain {
		p, route, err := rt.pick(alias)
		if err != nil {
			if i == 0 {
				// The requested alias itself is unroutable: terminal.
				return zero, err
			}
			e.logger.Warn("skipping unroutable fallback",
				slog.String("requested", req.Model),
				slog.String("alias", alias),
				slog.Any("error", err))
			lastErr = err
			continue
		}

		upstream := *req
		upstream.Model = route.Model

		result, err := attempt(ctx, e, route, func() (T, error) { return call(p, route, &upstream) })
		if err == nil {
			return result, nil
		}
		lastErr = err

		if !provider.Retryable(err) {
			return zero, err
		}
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
	}

	if lastErr == nil {
		return zero, fmt.Errorf("%w: %s", ErrUnknownModel, req.Model)
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
