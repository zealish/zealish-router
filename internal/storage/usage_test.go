package storage

import (
	"context"
	"math"
	"testing"
	"time"
)

func sampleUsage(at time.Time, alias string, prompt, completion, cached int, cost float64) UsageEvent {
	return UsageEvent{
		CreatedAt:        at,
		Alias:            alias,
		Provider:         "weizerouter",
		Model:            alias,
		Status:           "ok",
		Duration:         900 * time.Millisecond,
		PromptTokens:     prompt,
		CompletionTokens: completion,
		CachedTokens:     cached,
		CostUSD:          cost,
	}
}

func TestUsageRecordAndRecentIsNewestFirst(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	for i, alias := range []string{"first", "second", "third"} {
		e := sampleUsage(now.Add(time.Duration(i)*time.Minute), alias, 100, 10, 0, 0.001)
		if err := store.Usage().Record(ctx, e); err != nil {
			t.Fatalf("Record %s: %v", alias, err)
		}
	}

	events, err := store.Usage().Recent(ctx, 10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("len(events) = %d, want 3", len(events))
	}
	if events[0].Alias != "third" || events[2].Alias != "first" {
		t.Errorf("order = %s..%s, want third..first", events[0].Alias, events[2].Alias)
	}

	// Round-tripped detail must survive the unix-second and millisecond casts.
	if got := events[0].Duration; got != 900*time.Millisecond {
		t.Errorf("Duration = %v, want 900ms", got)
	}
	if !events[0].CreatedAt.Equal(now.Add(2 * time.Minute)) {
		t.Errorf("CreatedAt = %v, want %v", events[0].CreatedAt, now.Add(2*time.Minute))
	}
}

func TestUsageRecentHonoursLimit(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	for i := range 5 {
		if err := store.Usage().Record(ctx, sampleUsage(now.Add(time.Duration(i)*time.Second), "m", 1, 1, 0, 0)); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	events, err := store.Usage().Recent(ctx, 2)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(events) != 2 {
		t.Errorf("len(events) = %d, want 2", len(events))
	}
}

func TestUsageTotalsExcludesRowsBeforeWindow(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	old := sampleUsage(now.Add(-48*time.Hour), "old", 1000, 100, 50, 1.0)
	recent := sampleUsage(now.Add(-1*time.Hour), "recent", 200, 20, 10, 0.25)
	for _, e := range []UsageEvent{old, recent} {
		if err := store.Usage().Record(ctx, e); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	totals, err := store.Usage().Totals(ctx, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("Totals: %v", err)
	}
	if totals.Requests != 1 {
		t.Errorf("Requests = %d, want 1 (the older row is outside the window)", totals.Requests)
	}
	if totals.PromptTokens != 200 || totals.CompletionTokens != 20 || totals.CachedTokens != 10 {
		t.Errorf("tokens = %+v, want 200/20/10", totals)
	}
	if math.Abs(totals.CostUSD-0.25) > 1e-9 {
		t.Errorf("CostUSD = %v, want 0.25", totals.CostUSD)
	}
}

func TestUsageTotalsCountsNonOkAsErrors(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	ok := sampleUsage(now, "ok", 10, 1, 0, 0)
	failed := sampleUsage(now, "bad", 10, 1, 0, 0)
	failed.Status = "upstream_error"
	for _, e := range []UsageEvent{ok, failed} {
		if err := store.Usage().Record(ctx, e); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	totals, err := store.Usage().Totals(ctx, time.Time{})
	if err != nil {
		t.Fatalf("Totals: %v", err)
	}
	if totals.Requests != 2 || totals.Errors != 1 {
		t.Errorf("requests/errors = %d/%d, want 2/1", totals.Requests, totals.Errors)
	}
}

func TestUsageSeriesGroupsIntoBuckets(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Hour)

	// Two events in one hour, one in the next.
	events := []UsageEvent{
		sampleUsage(base.Add(5*time.Minute), "a", 100, 10, 0, 0.01),
		sampleUsage(base.Add(50*time.Minute), "b", 200, 20, 5, 0.02),
		sampleUsage(base.Add(70*time.Minute), "c", 400, 40, 0, 0.04),
	}
	for _, e := range events {
		if err := store.Usage().Record(ctx, e); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	series, err := store.Usage().Series(ctx, base.Add(-time.Hour), time.Hour)
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	if len(series) != 2 {
		t.Fatalf("len(series) = %d, want 2 buckets", len(series))
	}

	if series[0].Requests != 2 || series[0].PromptTokens != 300 || series[0].CachedTokens != 5 {
		t.Errorf("bucket 0 = %+v, want 2 requests / 300 prompt / 5 cached", series[0])
	}
	if series[1].Requests != 1 || series[1].PromptTokens != 400 {
		t.Errorf("bucket 1 = %+v, want 1 request / 400 prompt", series[1])
	}
	if !series[0].Start.Before(series[1].Start) {
		t.Error("series is not ordered oldest-first")
	}
}

func TestUsageSeriesRejectsNonPositiveBucket(t *testing.T) {
	store := newTestStore(t)

	series, err := store.Usage().Series(context.Background(), time.Time{}, 0)
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	if series != nil {
		t.Errorf("series = %v, want nil for a zero bucket", series)
	}
}
