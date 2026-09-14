package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

func sampleTrace(id string, at time.Time, model, finalProvider, status string) RequestTrace {
	return RequestTrace{
		RequestID:     id,
		CreatedAt:     at,
		KeyID:         "key-1",
		Model:         model,
		TotalLatency:  1200 * time.Millisecond,
		TotalTokens:   140,
		TotalCostUSD:  0.002,
		FinalProvider: finalProvider,
		FinalAlias:    model,
		FinalStatus:   status,
		Attempts: []RequestAttempt{{
			Seq:       1,
			StartedAt: at,
			Alias:     model,
			Provider:  finalProvider,
			Model:     model + "-upstream",
			Latency:   1200 * time.Millisecond,
			Status:    status,
		}},
	}
}

func TestTraceRecordRoundTrips(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	want := sampleTrace("req-1", now, "gpt-5", "openai", "ok")
	want.Streamed = true
	want.Attempts = append(want.Attempts, RequestAttempt{
		Seq:       2,
		StartedAt: now.Add(time.Second),
		Alias:     "fast",
		Provider:  "openrouter",
		Model:     "gpt-5-mini",
		Latency:   400 * time.Millisecond,
		Status:    "timeout",
		Retry:     true,
		Fallback:  true,
		Error:     "upstream timed out",
	})

	if err := store.Traces().Record(ctx, want); err != nil {
		t.Fatalf("Record: %v", err)
	}

	got, err := store.Traces().Get(ctx, "req-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.CreatedAt.Equal(now) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, now)
	}
	if got.TotalLatency != 1200*time.Millisecond {
		t.Errorf("TotalLatency = %v, want 1.2s", got.TotalLatency)
	}
	if !got.Streamed {
		t.Error("Streamed = false, want true")
	}
	if got.AttemptCount != 2 {
		t.Errorf("AttemptCount = %d, want 2", got.AttemptCount)
	}
	if len(got.Attempts) != 2 {
		t.Fatalf("len(Attempts) = %d, want 2", len(got.Attempts))
	}

	second := got.Attempts[1]
	if second.Seq != 2 || second.Provider != "openrouter" {
		t.Errorf("attempt 2 = seq %d on %s, want seq 2 on openrouter", second.Seq, second.Provider)
	}
	if !second.Retry || !second.Fallback {
		t.Errorf("retry=%t fallback=%t, want both true", second.Retry, second.Fallback)
	}
	if second.Error != "upstream timed out" {
		t.Errorf("Error = %q, want the recorded message", second.Error)
	}
}

// Re-recording the same id replaces the trace instead of duplicating it, so a
// retried write after a partial failure converges.
func TestTraceRecordReplacesSameRequestID(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	first := sampleTrace("req-1", now, "gpt-5", "openai", "timeout")
	if err := store.Traces().Record(ctx, first); err != nil {
		t.Fatalf("Record first: %v", err)
	}

	second := sampleTrace("req-1", now, "gpt-5", "openrouter", "ok")
	if err := store.Traces().Record(ctx, second); err != nil {
		t.Fatalf("Record second: %v", err)
	}

	_, total, err := store.Traces().List(ctx, TraceFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 1 {
		t.Fatalf("total = %d, want 1", total)
	}

	got, err := store.Traces().Get(ctx, "req-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.FinalStatus != "ok" || got.FinalProvider != "openrouter" {
		t.Errorf("trace = %s/%s, want openrouter/ok", got.FinalProvider, got.FinalStatus)
	}
	if len(got.Attempts) != 1 {
		t.Errorf("len(Attempts) = %d, want 1: the old attempts must not linger", len(got.Attempts))
	}
}

func TestTraceListIsNewestFirstAndPaginates(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	for i, id := range []string{"first", "second", "third"} {
		trace := sampleTrace(id, now.Add(time.Duration(i)*time.Minute), "gpt-5", "openai", "ok")
		if err := store.Traces().Record(ctx, trace); err != nil {
			t.Fatalf("Record %s: %v", id, err)
		}
	}

	page, total, err := store.Traces().List(ctx, TraceFilter{Limit: 2})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 3 {
		t.Errorf("total = %d, want 3: the count ignores pagination", total)
	}
	if len(page) != 2 {
		t.Fatalf("len(page) = %d, want 2", len(page))
	}
	if page[0].RequestID != "third" || page[1].RequestID != "second" {
		t.Errorf("page = %s,%s, want third,second", page[0].RequestID, page[1].RequestID)
	}

	next, _, err := store.Traces().List(ctx, TraceFilter{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("List offset: %v", err)
	}
	if len(next) != 1 || next[0].RequestID != "first" {
		t.Errorf("second page = %v, want just first", next)
	}
}

func TestTraceListFilters(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	ok := sampleTrace("req-ok", now, "gpt-5", "openai", "ok")
	failed := sampleTrace("req-fail", now.Add(time.Minute), "claude", "anthropic", "timeout")
	failed.KeyID = "key-2"
	// The failed request fell back off openai before landing on anthropic, so
	// filtering by openai must still find it.
	failed.Attempts = append([]RequestAttempt{{
		Seq: 1, StartedAt: now, Alias: "gpt-5", Provider: "openai",
		Model: "gpt-5-upstream", Status: "upstream_5xx",
	}}, RequestAttempt{
		Seq: 2, StartedAt: now, Alias: "claude", Provider: "anthropic",
		Model: "claude-upstream", Status: "timeout", Fallback: true,
	})

	for _, trace := range []RequestTrace{ok, failed} {
		if err := store.Traces().Record(ctx, trace); err != nil {
			t.Fatalf("Record %s: %v", trace.RequestID, err)
		}
	}

	cases := []struct {
		name   string
		filter TraceFilter
		want   []string
	}{
		{"status", TraceFilter{Status: "timeout"}, []string{"req-fail"}},
		{"model", TraceFilter{Model: "gpt-5"}, []string{"req-ok"}},
		{"api key", TraceFilter{KeyID: "key-2"}, []string{"req-fail"}},
		{"final provider", TraceFilter{Provider: "anthropic"}, []string{"req-fail"}},
		{"attempted provider", TraceFilter{Provider: "openai"}, []string{"req-fail", "req-ok"}},
		{"combined", TraceFilter{Provider: "openai", Status: "ok"}, []string{"req-ok"}},
		{"no match", TraceFilter{Model: "absent"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, total, err := store.Traces().List(ctx, tc.filter)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if total != len(tc.want) {
				t.Fatalf("total = %d, want %d", total, len(tc.want))
			}
			for i, id := range tc.want {
				if got[i].RequestID != id {
					t.Errorf("item %d = %s, want %s", i, got[i].RequestID, id)
				}
			}
		})
	}
}

func TestTraceGetMissingReportsNotFound(t *testing.T) {
	store := newTestStore(t)

	_, err := store.Traces().Get(context.Background(), "absent")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

// Pruning a trace takes its attempts with it, so no orphan rows survive.
func TestTracePruneCascadesToAttempts(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	old := sampleTrace("old", now.Add(-48*time.Hour), "gpt-5", "openai", "ok")
	fresh := sampleTrace("fresh", now, "gpt-5", "openai", "ok")
	for _, trace := range []RequestTrace{old, fresh} {
		if err := store.Traces().Record(ctx, trace); err != nil {
			t.Fatalf("Record %s: %v", trace.RequestID, err)
		}
	}

	removed, err := store.Traces().Prune(ctx, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}

	var orphans int
	if err := store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM request_attempts WHERE request_id = 'old'`).Scan(&orphans); err != nil {
		t.Fatalf("count attempts: %v", err)
	}
	if orphans != 0 {
		t.Errorf("orphaned attempts = %d, want 0", orphans)
	}
}

// The in-memory store backs the API tests, so it must answer filters and
// pagination the same way SQLite does.
func TestMemoryTraceListMatchesFilters(t *testing.T) {
	store := NewMemory()
	ctx := context.Background()
	now := time.Now().UTC()

	ok := sampleTrace("req-ok", now, "gpt-5", "openai", "ok")
	failed := sampleTrace("req-fail", now.Add(time.Minute), "claude", "anthropic", "timeout")
	for _, trace := range []RequestTrace{ok, failed} {
		if err := store.Traces().Record(ctx, trace); err != nil {
			t.Fatalf("Record %s: %v", trace.RequestID, err)
		}
	}

	page, total, err := store.Traces().List(ctx, TraceFilter{Limit: 1})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 2 {
		t.Errorf("total = %d, want 2", total)
	}
	if len(page) != 1 || page[0].RequestID != "req-fail" {
		t.Fatalf("page = %v, want the newest trace only", page)
	}
	if page[0].AttemptCount != 1 {
		t.Errorf("AttemptCount = %d, want 1", page[0].AttemptCount)
	}

	filtered, total, err := store.Traces().List(ctx, TraceFilter{Provider: "anthropic"})
	if err != nil {
		t.Fatalf("List filtered: %v", err)
	}
	if total != 1 || filtered[0].RequestID != "req-fail" {
		t.Errorf("filtered = %v, want req-fail", filtered)
	}
}
