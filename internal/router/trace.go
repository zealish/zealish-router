package router

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/zealish/zealish-router/internal/storage"
)

// traceWriteTimeout bounds a single trace insert. Like the usage log it runs
// off the request's context so a finished stream still records after the
// client disconnects.
const traceWriteTimeout = 5 * time.Second

// maxTraceAttempts caps how many attempts one trace keeps. A chain is already
// bounded by maxChainAttempts times the retry budget; this is the last line of
// defence against a trace row growing without bound.
const maxTraceAttempts = 64

// requestIDKey carries the gateway request id through the request context.
type requestIDKey struct{}

// WithRequestID attaches the gateway request id to a context. The HTTP layer
// sets it once per request; everything downstream reads it.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestIDFrom returns the gateway request id attached to a context, if any.
func RequestIDFrom(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(requestIDKey{}).(string)
	return id, ok && id != ""
}

// trace accumulates the attempts of one gateway request. One request produces
// exactly one trace however many times the chain retried or fell back, so the
// collector is created once per dispatch and never per attempt.
//
// It is written from the request goroutine and, for a streamed request, from
// the metering goroutine that finishes after the relay drains; the mutex is
// what makes that handoff safe.
type trace struct {
	requestID string
	keyID     string
	model     string
	streamed  bool
	started   time.Time

	mu       sync.Mutex
	attempts []storage.RequestAttempt
	tokens   int
	costUSD  float64
	// lastRoute is the route of the most recent attempt, which is the one
	// whose provider the summary reports.
	lastRoute Route
	// done guards against a double finish: a streamed request records when the
	// relay drains, and the dispatch path must not race it to the row.
	done bool
}

// newTrace starts collecting for one request. It returns nil when the request
// carries no id, which is what makes tracing opt-in and free when the HTTP
// layer has not enabled it.
func newTrace(ctx context.Context, model string, streamed bool, meta callMeta) *trace {
	id, ok := RequestIDFrom(ctx)
	if !ok {
		return nil
	}
	return &trace{
		requestID: id,
		keyID:     meta.keyID,
		model:     model,
		streamed:  streamed,
		started:   meta.started,
	}
}

// attempt appends one upstream call to the timeline. retry marks a repeat of
// the same route and fallback a move onto the next one; the first attempt of a
// request is neither. A nil trace is a no-op so callers never branch.
func (t *trace) attempt(route Route, started time.Time, latency time.Duration, status string, err error, retry, fallback bool) {
	if t == nil {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	t.lastRoute = route
	if len(t.attempts) >= maxTraceAttempts {
		return
	}

	a := storage.RequestAttempt{
		Seq:       len(t.attempts) + 1,
		StartedAt: started.UTC(),
		Alias:     route.Alias,
		Provider:  route.Provider,
		Model:     route.Model,
		Latency:   latency,
		Status:    status,
		Retry:     retry,
		Fallback:  fallback,
	}
	if err != nil {
		a.Error = err.Error()
	}
	t.attempts = append(t.attempts, a)
}

// skipped records a route the chain passed over without calling it: an open
// circuit, a failed health probe or an alias that no longer resolves. It is
// part of the timeline because it explains why the next attempt happened.
// fallback carries the same meaning as on attempt: the chain position, not the
// fact that this route was passed over.
func (t *trace) skipped(route Route, status string, err error, fallback bool) {
	if t == nil {
		return
	}
	t.attempt(route, time.Now(), 0, status, err, false, fallback)
}

// meter attributes tokens and cost to the trace. A request that fell back
// bills only the attempt that succeeded, but the totals stay per-request so
// the summary matches what the caller was charged.
func (t *trace) meter(tokens int, costUSD float64) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	t.tokens += tokens
	t.costUSD += costUSD
}

// finish writes the summary and its attempts. It is idempotent: whichever of
// the dispatch path and the stream metering goroutine gets there first wins,
// so a streamed request records once, after its relay drained.
func (e *Engine) finishTrace(t *trace, status string) {
	if t == nil || e.traces == nil {
		return
	}

	t.mu.Lock()
	if t.done {
		t.mu.Unlock()
		return
	}
	t.done = true

	record := storage.RequestTrace{
		RequestID:     t.requestID,
		CreatedAt:     t.started.UTC(),
		KeyID:         t.keyID,
		Model:         t.model,
		Streamed:      t.streamed,
		TotalLatency:  time.Since(t.started),
		TotalTokens:   t.tokens,
		TotalCostUSD:  t.costUSD,
		FinalProvider: t.lastRoute.Provider,
		FinalAlias:    t.lastRoute.Alias,
		FinalStatus:   status,
		Attempts:      t.attempts,
	}
	t.mu.Unlock()

	// Detached context: a streamed request finishes its trace after the
	// client's context is already cancelled, and the row must still land.
	ctx, cancel := context.WithTimeout(context.Background(), traceWriteTimeout)
	defer cancel()

	if err := e.traces.Record(ctx, record); err != nil && e.logger != nil {
		e.logger.Warn("request trace write failed",
			slog.String("request_id", record.RequestID), slog.Any("error", err))
	}
}
