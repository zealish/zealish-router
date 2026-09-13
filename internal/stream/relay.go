package stream

import (
	"context"
	"time"
)

// Relay forwards every value from chunks to w as an SSE frame, emitting a
// heartbeat comment whenever the upstream is idle for longer than heartbeat.
//
// It returns when the channel is closed (writing the [DONE] sentinel), when ctx
// is cancelled — typically a client disconnect — or on the first write error.
// A non-positive heartbeat disables keepalives.
func Relay[T any](ctx context.Context, w *Writer, chunks <-chan T, heartbeat time.Duration) error {
	var idle <-chan time.Time
	if heartbeat > 0 {
		ticker := time.NewTicker(heartbeat)
		defer ticker.Stop()
		idle = ticker.C
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case chunk, ok := <-chunks:
			if !ok {
				return w.Done()
			}
			if err := w.Event(chunk); err != nil {
				return err
			}

		case <-idle:
			if err := w.Heartbeat(); err != nil {
				return err
			}
		}
	}
}
