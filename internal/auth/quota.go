package auth

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/zealish/zealish-router/internal/storage"
)

// ErrRateLimited is returned when a key exceeded its requests-per-minute quota.
var ErrRateLimited = errors.New("auth: rate limit exceeded")

// ErrBudgetExceeded is returned when a key spent its monthly budget.
var ErrBudgetExceeded = errors.New("auth: monthly budget exceeded")

// spendTTL bounds how stale a cached monthly-spend figure may be. Budgets are
// a cost guardrail, not a ledger: a few seconds of overshoot is acceptable and
// far cheaper than summing the usage log on every request.
const spendTTL = 30 * time.Second

// budgetReadTimeout bounds the spend query so a slow database degrades to
// allowing the request rather than stalling it.
const budgetReadTimeout = 2 * time.Second

// window is one key's rolling-minute request counter. Timestamps are kept so
// the window slides rather than resetting on a fixed boundary, which would let
// a client burst twice the limit across an edge.
type window struct {
	hits []time.Time
}

// spend is a cached monthly cost total for one key.
type spend struct {
	usd       float64
	refreshed time.Time
}

// Quota enforces per-key rate limits and monthly budgets. It is safe for
// concurrent use; a nil Quota allows everything, which is what the unmetered
// paths and tests want.
type Quota struct {
	usage storage.UsageStore

	mu       sync.Mutex
	windows  map[string]*window
	spending map[string]spend

	// now is injectable so tests can drive the clock.
	now func() time.Time
}

// NewQuota constructs a quota enforcer over the usage log. A nil store
// disables budget checks but leaves rate limiting intact.
func NewQuota(usage storage.UsageStore) *Quota {
	return &Quota{
		usage:    usage,
		windows:  map[string]*window{},
		spending: map[string]spend{},
		now:      time.Now,
	}
}

// Allow reports whether a request from id may proceed, and how much of the
// rate quota is left. An identity with no key id — auth disabled or a static
// key — is unmetered and always allowed.
//
// Rate is checked before budget: it is the cheaper test and the one that
// protects the database read behind the budget check.
func (q *Quota) Allow(ctx context.Context, id Identity) (remaining int, err error) {
	if q == nil || id.KeyID == "" || id.KeyID == "static" || id.KeyID == "anonymous" {
		return 0, nil
	}

	if id.RateLimitPerMin > 0 {
		allowed, left := q.takeSlot(id.KeyID, id.RateLimitPerMin)
		if !allowed {
			return 0, ErrRateLimited
		}
		remaining = left
	}

	if id.MonthlyBudgetUSD > 0 && q.usage != nil {
		if q.spentThisMonth(ctx, id.KeyID) >= id.MonthlyBudgetUSD {
			return remaining, ErrBudgetExceeded
		}
	}
	return remaining, nil
}

// takeSlot records a hit against the rolling minute and reports whether it fit
// inside the limit, along with the slots left afterwards.
func (q *Quota) takeSlot(keyID string, limit int) (bool, int) {
	now := q.now()
	cutoff := now.Add(-time.Minute)

	q.mu.Lock()
	defer q.mu.Unlock()

	w, ok := q.windows[keyID]
	if !ok {
		w = &window{}
		q.windows[keyID] = w
	}

	// Hits are appended in order, so the expired ones are a prefix.
	drop := 0
	for drop < len(w.hits) && !w.hits[drop].After(cutoff) {
		drop++
	}
	w.hits = w.hits[drop:]

	if len(w.hits) >= limit {
		return false, 0
	}
	w.hits = append(w.hits, now)
	return true, limit - len(w.hits)
}

// spentThisMonth returns the key's calendar-month spend, reading through to
// the usage log at most once per spendTTL. A read failure reports zero so a
// database problem cannot lock every budgeted key out of the gateway.
func (q *Quota) spentThisMonth(ctx context.Context, keyID string) float64 {
	now := q.now()

	q.mu.Lock()
	cached, ok := q.spending[keyID]
	q.mu.Unlock()
	if ok && now.Sub(cached.refreshed) < spendTTL {
		return cached.usd
	}

	read, cancel := context.WithTimeout(ctx, budgetReadTimeout)
	defer cancel()

	usd, err := q.usage.KeySpend(read, keyID, MonthStart(now))
	if err != nil {
		return 0
	}

	q.mu.Lock()
	q.spending[keyID] = spend{usd: usd, refreshed: now}
	q.mu.Unlock()
	return usd
}

// Forget drops the cached state of a key. Editing a quota or revoking a key
// must not leave a stale window or spend total behind.
func (q *Quota) Forget(keyID string) {
	if q == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.windows, keyID)
	delete(q.spending, keyID)
}

// MonthStart is the first instant of the calendar month containing t, in UTC.
// Monthly budgets reset on that boundary.
func MonthStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}
