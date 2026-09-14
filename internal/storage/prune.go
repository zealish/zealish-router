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
	if retention <= 0 {
		return
	}

	sweep := func() {
		swept, cancel := context.WithTimeout(ctx, pruneTimeout)
		defer cancel()

		removed, err := usage.Prune(swept, time.Now().Add(-retention))
		switch {
		case err != nil:
			logger.Warn("usage log prune failed", slog.Any("error", err))
		case removed > 0:
			logger.Info("usage log pruned",
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
