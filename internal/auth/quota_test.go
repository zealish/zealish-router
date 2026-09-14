package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zealish/zealish-router/internal/storage"
)

// newTestQuota returns a quota enforcer over an empty in-memory usage log with
// a clock the caller drives.
func newTestQuota(t *testing.T) (*Quota, storage.UsageStore, *time.Time) {
	t.Helper()

	usage := storage.NewMemory().Usage()
	clock := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)
	q := NewQuota(usage)
	q.now = func() time.Time { return clock }
	return q, usage, &clock
}

func TestQuotaAllowsUnmeteredIdentities(t *testing.T) {
	q, _, _ := newTestQuota(t)
	ctx := context.Background()

	for _, id := range []Identity{
		{KeyID: "anonymous", RateLimitPerMin: 1},
		{KeyID: "static", RateLimitPerMin: 1},
		{KeyID: "", RateLimitPerMin: 1},
	} {
		for range 5 {
			if _, err := q.Allow(ctx, id); err != nil {
				t.Fatalf("Allow(%q) = %v, want nil: unmetered identities bypass quotas", id.KeyID, err)
			}
		}
	}
}

func TestQuotaAllowsWhenNoLimitConfigured(t *testing.T) {
	q, _, _ := newTestQuota(t)
	ctx := context.Background()

	id := Identity{KeyID: "k1"}
	for range 100 {
		if _, err := q.Allow(ctx, id); err != nil {
			t.Fatalf("Allow = %v, want nil for a key with no limits", err)
		}
	}
}

func TestQuotaRateLimitBlocksPastLimitAndWindowSlides(t *testing.T) {
	q, _, clock := newTestQuota(t)
	ctx := context.Background()
	id := Identity{KeyID: "k1", RateLimitPerMin: 3}

	for i := range 3 {
		remaining, err := q.Allow(ctx, id)
		if err != nil {
			t.Fatalf("request %d: %v, want allowed", i, err)
		}
		if want := 2 - i; remaining != want {
			t.Errorf("request %d remaining = %d, want %d", i, remaining, want)
		}
	}

	if _, err := q.Allow(ctx, id); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("fourth request = %v, want ErrRateLimited", err)
	}

	// Still inside the same minute: still blocked.
	*clock = clock.Add(30 * time.Second)
	if _, err := q.Allow(ctx, id); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("mid-window request = %v, want ErrRateLimited", err)
	}

	// Past the minute, the earliest hits expire and slots free up.
	*clock = clock.Add(31 * time.Second)
	if _, err := q.Allow(ctx, id); err != nil {
		t.Fatalf("after window slid = %v, want allowed", err)
	}
}

func TestQuotaIsolatesKeys(t *testing.T) {
	q, _, _ := newTestQuota(t)
	ctx := context.Background()

	busy := Identity{KeyID: "busy", RateLimitPerMin: 1}
	idle := Identity{KeyID: "idle", RateLimitPerMin: 1}

	if _, err := q.Allow(ctx, busy); err != nil {
		t.Fatalf("busy first = %v, want allowed", err)
	}
	if _, err := q.Allow(ctx, busy); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("busy second = %v, want ErrRateLimited", err)
	}
	if _, err := q.Allow(ctx, idle); err != nil {
		t.Fatalf("idle key = %v, want allowed: quotas are per key", err)
	}
}

func TestQuotaBudgetBlocksWhenMonthlySpendReached(t *testing.T) {
	q, usage, clock := newTestQuota(t)
	ctx := context.Background()
	id := Identity{KeyID: "k1", MonthlyBudgetUSD: 1.0}

	if _, err := q.Allow(ctx, id); err != nil {
		t.Fatalf("with no spend = %v, want allowed", err)
	}

	// Spend the budget inside the current month.
	if err := usage.Record(ctx, storage.UsageEvent{
		CreatedAt: *clock, KeyID: "k1", Alias: "a", Provider: "p", Model: "m",
		Status: "ok", CostUSD: 1.5,
	}); err != nil {
		t.Fatalf("record usage: %v", err)
	}

	// The cached total is still fresh, so the request is allowed.
	if _, err := q.Allow(ctx, id); err != nil {
		t.Fatalf("within cache TTL = %v, want allowed", err)
	}

	// Past the TTL the enforcer re-reads and sees the overspend.
	*clock = clock.Add(spendTTL + time.Second)
	if _, err := q.Allow(ctx, id); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("after refresh = %v, want ErrBudgetExceeded", err)
	}
}

func TestQuotaBudgetIgnoresPreviousMonths(t *testing.T) {
	q, usage, clock := newTestQuota(t)
	ctx := context.Background()

	// A large spend in the previous month must not count against this one.
	if err := usage.Record(ctx, storage.UsageEvent{
		CreatedAt: clock.AddDate(0, -1, 0), KeyID: "k1", Alias: "a",
		Provider: "p", Model: "m", Status: "ok", CostUSD: 100,
	}); err != nil {
		t.Fatalf("record usage: %v", err)
	}

	if _, err := q.Allow(ctx, Identity{KeyID: "k1", MonthlyBudgetUSD: 1.0}); err != nil {
		t.Fatalf("Allow = %v, want allowed: budgets reset each calendar month", err)
	}
}

func TestQuotaForgetClearsWindowAndSpend(t *testing.T) {
	q, _, _ := newTestQuota(t)
	ctx := context.Background()
	id := Identity{KeyID: "k1", RateLimitPerMin: 1}

	if _, err := q.Allow(ctx, id); err != nil {
		t.Fatalf("first = %v, want allowed", err)
	}
	if _, err := q.Allow(ctx, id); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("second = %v, want ErrRateLimited", err)
	}

	q.Forget("k1")
	if _, err := q.Allow(ctx, id); err != nil {
		t.Fatalf("after Forget = %v, want allowed", err)
	}
}

func TestNilQuotaAllowsEverything(t *testing.T) {
	var q *Quota
	if _, err := q.Allow(context.Background(), Identity{KeyID: "k1", RateLimitPerMin: 1}); err != nil {
		t.Fatalf("nil quota = %v, want nil", err)
	}
	q.Forget("k1") // must not panic
}

func TestMonthStartIsFirstInstantUTC(t *testing.T) {
	got := MonthStart(time.Date(2026, 3, 15, 12, 34, 56, 789, time.UTC))
	want := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("MonthStart = %v, want %v", got, want)
	}
}
