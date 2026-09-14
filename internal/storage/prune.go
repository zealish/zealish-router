package storage

import (
	"context"
	"log/slog"
	"time"
)

// pruneInterval is how often the retention sweep runs. The usage log grows by
// one row per request, so an hourly sweep is far more often than it needs to
// be for any retention window measured in days.
const pruneInterval = time.Hour

// pruneTimeout bounds a single sweep so a slow delete cannot pin the goroutine
// past its next tick.
const pruneTimeout = 30 * time.Second

// PruneUsage deletes usage events older than retention on a fixed interval
// until ctx is cancelled. A retention of zero disables pruning and returns
// immediately, which keeps the log unbounded as it was before.
//
// The first sweep runs at once so a restart with a shortened retention window
// takes effect without waiting for a tick.
func PruneUsage(ctx context.Context, usage UsageStore, retention time.Duration, logger *slog.Logger) {
	pruneEvery(ctx, "usage log", usage.Prune, retention, logger)
}

// PruneTraces deletes request traces older than retention on the same schedule
// and with the same semantics as PruneUsage. Attempts follow their trace.
func PruneTraces(ctx context.Context, traces TraceStore, retention time.Duration, logger *slog.Logger) {
	pruneEvery(ctx, "request traces", traces.Prune, retention, logger)
}

// pruneEvery runs remove on the retention cutoff until ctx is cancelled.
func pruneEvery(ctx context.Context, label string, remove func(context.Context, time.Time) (int64, error), retention time.Duration, logger *slog.Logger) {
	if retention <= 0 {
		return
	}

	sweep := func() {
		swept, cancel := context.WithTimeout(ctx, pruneTimeout)
		defer cancel()

		removed, err := remove(swept, time.Now().Add(-retention))
		switch {
		case err != nil:
			logger.Warn(label+" prune failed", slog.Any("error", err))
		case removed > 0:
			logger.Info(label+" pruned",
				slog.Int64("removed", removed),
				slog.Duration("retention", retention))
		}
	}

	sweep()

	ticker := time.NewTicker(pruneInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweep()
		}
	}
}
