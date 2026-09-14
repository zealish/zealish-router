package provider

import (
	"context"
	"net/http/httptrace"
	"sync"
	"time"
)

// firstByteKey carries a *FirstByte through the request context. It is unset
// for callers that do not measure, so the trace costs nothing.
type firstByteKey struct{}

// FirstByte records when the upstream's first response byte arrived. A caller
// obtains one from WithFirstByte and reads it after the call returns.
type FirstByte struct {
	mu sync.Mutex
	at time.Time
}

// WithFirstByte returns a context that measures time to first response byte on
// every upstream call made with it, plus the recorder holding the result.
func WithFirstByte(ctx context.Context) (context.Context, *FirstByte) {
	f := &FirstByte{}
	return context.WithValue(ctx, firstByteKey{}, f), f
}

// Since returns the time from start until the first response byte. It reports
// zero when no response byte ever arrived.
func (f *FirstByte) Since(start time.Time) time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.at.IsZero() || f.at.Before(start) {
		return 0
	}
	return f.at.Sub(start)
}

func (f *FirstByte) set(at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()

	// Retries reuse the recorder; the first arrival is the one that counts.
	if f.at.IsZero() {
		f.at = at
	}
}

// traceFirstByte installs the httptrace hook when the caller asked for it.
func traceFirstByte(ctx context.Context) context.Context {
	f, ok := ctx.Value(firstByteKey{}).(*FirstByte)
	if !ok {
		return ctx
	}
	return httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GotFirstResponseByte: func() { f.set(time.Now()) },
	})
}
