package router

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/zealish/zealish-router/internal/provider"
)

// ErrProviderUnhealthy is returned when a route is skipped because its
// provider failed its last health probe. Like ErrCircuitOpen it is never
// retryable: the chain advances immediately.
var ErrProviderUnhealthy = errors.New("router: provider unhealthy")

// HealthCheckPolicy configures the background provider health checker.
type HealthCheckPolicy struct {
	// Interval is how often every provider is pinged.
	Interval time.Duration
	// Timeout bounds a single ping.
	Timeout time.Duration
}

// DefaultHealthCheck pings every provider twice a minute, which keeps the
// worst-case window a dead provider stays in rotation under thirty seconds.
var DefaultHealthCheck = HealthCheckPolicy{Interval: 30 * time.Second, Timeout: 5 * time.Second}

// probes tracks which providers failed their last health probe. Like breaker
// health it is a property of the upstream, so it survives a Reload. Absence
// means healthy: a provider that has never been probed is routable.
type probes struct {
	mu   sync.Mutex
	down map[string]error
}

func newProbes() *probes {
	return &probes{down: make(map[string]error)}
}

// healthy reports whether the provider passed its last probe (or was never
// probed).
func (p *probes) healthy(name string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, down := p.down[name]
	return !down
}

// set records a probe result and reports whether the provider's health
// changed.
func (p *probes) set(name string, err error) (changed bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, wasDown := p.down[name]
	if err == nil {
		delete(p.down, name)
		return wasDown
	}
	p.down[name] = err
	return !wasDown
}

// snapshot returns the last error of every provider that is currently down.
func (p *probes) snapshot() map[string]error {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]error, len(p.down))
	for name, err := range p.down {
		out[name] = err
	}
	return out
}

// HealthChecker pings every registered provider on a fixed interval and takes
// failing ones out of rotation, so a dead upstream is skipped without any
// request paying for the discovery.
type HealthChecker struct {
	engine *Engine
	policy HealthCheckPolicy
	logger *slog.Logger
}

// NewHealthChecker wires a checker onto an engine. Non-positive policy fields
// fall back to DefaultHealthCheck.
func NewHealthChecker(e *Engine, policy HealthCheckPolicy, logger *slog.Logger) *HealthChecker {
	if policy.Interval <= 0 {
		policy.Interval = DefaultHealthCheck.Interval
	}
	if policy.Timeout <= 0 {
		policy.Timeout = DefaultHealthCheck.Timeout
	}
	return &HealthChecker{engine: e, policy: policy, logger: logger}
}

// Run pings all providers immediately and then on every interval tick until
// ctx is cancelled. It is meant to be launched as a goroutine.
func (c *HealthChecker) Run(ctx context.Context) {
	c.CheckAll(ctx)

	ticker := time.NewTicker(c.policy.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.CheckAll(ctx)
		}
	}
}

// CheckAll pings every provider in the current registry concurrently and
// publishes the results. Providers that do not implement provider.Pinger are
// always considered healthy.
func (c *HealthChecker) CheckAll(ctx context.Context) {
	registry := c.engine.routes.Load().registry

	var wg sync.WaitGroup
	for _, name := range registry.Names() {
		p, err := registry.Get(name)
		if err != nil {
			continue
		}
		pinger, ok := p.(provider.Pinger)
		if !ok {
			continue
		}
		wg.Add(1)
		go func(name string, pinger provider.Pinger) {
			defer wg.Done()
			c.check(ctx, name, pinger)
		}(name, pinger)
	}
	wg.Wait()
}

// check runs one probe and logs and records health transitions.
func (c *HealthChecker) check(ctx context.Context, name string, pinger provider.Pinger) {
	pingCtx, cancel := context.WithTimeout(ctx, c.policy.Timeout)
	defer cancel()

	err := pinger.Ping(pingCtx)
	if ctx.Err() != nil {
		// Shutdown, not provider failure: leave health as it was.
		return
	}

	changed := c.engine.probes.set(name, err)
	if !changed {
		return
	}
	if err != nil {
		c.logger.Error("provider health check failed",
			slog.String("provider", name),
			slog.Any("error", err))
		c.engine.record(name, "health_check")
		return
	}
	c.logger.Info("provider health check recovered", slog.String("provider", name))
}

// skipUnhealthy returns the reason the route must be skipped because its
// provider failed its last probe, or nil when the provider is routable.
func (e *Engine) skipUnhealthy(providerName string) error {
	if e.probes.healthy(providerName) {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrProviderUnhealthy, providerName)
}
