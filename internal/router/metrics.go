package router

import (
	"context"
	"time"

	"github.com/zealish/zealish-router/internal/provider"
	"github.com/zealish/zealish-router/pkg/openai"
)

// Stats exposes the rolling per-alias request window.
func (e *Engine) Stats() *Stats {
	return e.stats
}

// AliasStats returns the rolling aggregate for one alias.
func (e *Engine) AliasStats(alias string) (AliasStats, bool) {
	return e.stats.Alias(alias)
}

// measured is the timing of one routed attempt: when it started and when the
// first upstream byte arrived.
type measured struct {
	start     time.Time
	firstByte *provider.FirstByte
}

// measure prepares a context that records time to first response byte.
func measure(ctx context.Context) (context.Context, measured) {
	ctx, fb := provider.WithFirstByte(ctx)
	return ctx, measured{start: time.Now(), firstByte: fb}
}

// ttfb reports the time to first byte observed so far.
func (m measured) ttfb() time.Duration {
	return m.firstByte.Since(m.start)
}

// record appends the attempt to the alias's rolling window. ttfb is passed
// explicitly because a stream's first token arrives long after its headers.
func (e *Engine) recordMetric(route Route, success bool, ttfb, total time.Duration) {
	e.stats.Record(RequestMetric{
		Alias:     route.Alias,
		Success:   success,
		TTFBMs:    ttfb.Milliseconds(),
		LatencyMs: total.Milliseconds(),
		Timestamp: time.Now(),
	})
}

// meterLatency wraps a streamed relay so the alias window learns the time to
// the first token and the full stream duration, rather than the time it took
// to open the stream.
func (e *Engine) meterLatency(ctx context.Context, route Route, m measured, chunks <-chan openai.StreamChunk) <-chan openai.StreamChunk {
	out := make(chan openai.StreamChunk)
	go func() {
		defer close(out)

		var ttfb time.Duration
	relay:
		for chunk := range chunks {
			if ttfb == 0 {
				ttfb = time.Since(m.start)
			}
			select {
			case out <- chunk:
			case <-ctx.Done():
				break relay
			}
		}
		// A stream that yielded nothing never reached the client, so it counts
		// as a failure even though establishing it succeeded.
		e.recordMetric(route, ttfb > 0, ttfb, time.Since(m.start))
	}()
	return out
}
