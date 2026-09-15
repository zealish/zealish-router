package cache

import (
	"strconv"
	"sync"
	"testing"
	"time"
)

// withClock builds a cache whose clock the test drives.
func withClock(t *testing.T, ttl time.Duration, capacity int) (*Cache, *time.Time) {
	t.Helper()
	c := New(ttl, capacity)
	if c == nil {
		t.Fatalf("New(%s, %d) = nil, want a cache", ttl, capacity)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	return c, &now
}

func TestGetMissThenHit(t *testing.T) {
	c, _ := withClock(t, time.Minute, 4)

	if _, ok := c.Get("k"); ok {
		t.Fatal("empty cache reported a hit")
	}
	c.Put("k", Entry{Body: []byte(`{"a":1}`), Model: "gpt-5"})

	entry, ok := c.Get("k")
	if !ok {
		t.Fatal("stored entry did not hit")
	}
	if string(entry.Body) != `{"a":1}` || entry.Model != "gpt-5" {
		t.Fatalf("entry = %+v", entry)
	}
	if entry.StoredAt.IsZero() {
		t.Error("StoredAt was not stamped")
	}

	stats := c.Stats()
	if stats.Hits != 1 || stats.Misses != 1 || stats.Stores != 1 || stats.Entries != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	if got := stats.HitRate(); got != 0.5 {
		t.Errorf("hit rate = %v, want 0.5", got)
	}
}

func TestExpiredEntryMisses(t *testing.T) {
	c, now := withClock(t, time.Minute, 4)
	c.Put("k", Entry{Body: []byte("x")})

	*now = now.Add(59 * time.Second)
	if _, ok := c.Get("k"); !ok {
		t.Fatal("entry expired before its TTL")
	}

	*now = now.Add(2 * time.Second)
	if _, ok := c.Get("k"); ok {
		t.Fatal("entry survived its TTL")
	}
	if entries := c.Stats().Entries; entries != 0 {
		t.Errorf("entries = %d, want the expired entry dropped", entries)
	}
}

func TestEvictsLeastRecentlyUsed(t *testing.T) {
	c, _ := withClock(t, time.Minute, 2)
	c.Put("a", Entry{Body: []byte("a")})
	c.Put("b", Entry{Body: []byte("b")})

	// Touching "a" makes "b" the eviction candidate.
	if _, ok := c.Get("a"); !ok {
		t.Fatal("a missing")
	}
	c.Put("c", Entry{Body: []byte("c")})

	if _, ok := c.Get("b"); ok {
		t.Error("b should have been evicted")
	}
	if _, ok := c.Get("a"); !ok {
		t.Error("a should have survived as the recently used entry")
	}
	if stats := c.Stats(); stats.Evictions != 1 || stats.Entries != 2 {
		t.Errorf("stats = %+v, want 1 eviction and 2 entries", stats)
	}
}

func TestPutReplacesAndRefreshesTTL(t *testing.T) {
	c, now := withClock(t, time.Minute, 4)
	c.Put("k", Entry{Body: []byte("old")})

	*now = now.Add(30 * time.Second)
	c.Put("k", Entry{Body: []byte("new")})

	*now = now.Add(45 * time.Second)
	entry, ok := c.Get("k")
	if !ok {
		t.Fatal("replaced entry did not refresh its TTL")
	}
	if string(entry.Body) != "new" {
		t.Errorf("body = %q, want the replacement", entry.Body)
	}
	if entries := c.Stats().Entries; entries != 1 {
		t.Errorf("entries = %d, want the replacement not to add a second entry", entries)
	}
}

func TestPurgeDropsEntriesAndKeepsCounters(t *testing.T) {
	c, _ := withClock(t, time.Minute, 4)
	c.Put("a", Entry{Body: []byte("a")})
	c.Put("b", Entry{Body: []byte("b")})
	c.Get("a")

	if dropped := c.Purge(); dropped != 2 {
		t.Fatalf("purge dropped %d, want 2", dropped)
	}
	if _, ok := c.Get("a"); ok {
		t.Error("purged entry still hits")
	}
	if stats := c.Stats(); stats.Hits != 1 || stats.Stores != 2 || stats.Entries != 0 {
		t.Errorf("stats = %+v, want counters preserved across the purge", stats)
	}
}

// A disabled cache is represented by a nil pointer, so every method has to be
// safe on it: callers never branch on enablement.
func TestNilCacheIsDisabledAndSafe(t *testing.T) {
	var c *Cache
	if New(0, 10) != nil || New(time.Minute, 0) != nil {
		t.Fatal("non-positive ttl or capacity should disable the cache")
	}

	if c.Enabled() {
		t.Error("nil cache reported enabled")
	}
	if _, ok := c.Get("k"); ok {
		t.Error("nil cache hit")
	}
	c.Put("k", Entry{Body: []byte("x")})
	if _, ok := c.Get("k"); ok {
		t.Error("nil cache stored an entry")
	}
	if c.Purge() != 0 || c.TTL() != 0 || (c.Stats() != Stats{}) {
		t.Error("nil cache reported state")
	}
}

func TestKeySeparatesEndpointModelAndBody(t *testing.T) {
	body := []byte(`{"messages":[]}`)
	base := Key("/v1/chat/completions", "gpt-5", body)

	if Key("/v1/chat/completions", "gpt-5", body) != base {
		t.Error("key is not stable for identical input")
	}
	if Key("/v1/embeddings", "gpt-5", body) == base {
		t.Error("endpoint does not separate keys")
	}
	if Key("/v1/chat/completions", "fast", body) == base {
		t.Error("model does not separate keys")
	}
	if Key("/v1/chat/completions", "gpt-5", []byte(`{"messages":[1]}`)) == base {
		t.Error("body does not separate keys")
	}
	// Field boundaries are delimited, so shifting a character across them must
	// not produce the same key.
	if Key("/v1/chat", "completionsgpt-5", body) == base {
		t.Error("field boundaries are ambiguous")
	}
}

func TestConcurrentAccess(t *testing.T) {
	c, _ := withClock(t, time.Minute, 64)

	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := range 100 {
				key := strconv.Itoa((i + j) % 128)
				if _, ok := c.Get(key); !ok {
					c.Put(key, Entry{Body: []byte(key)})
				}
			}
		}(i)
	}
	wg.Wait()

	if entries := c.Stats().Entries; entries > 64 {
		t.Errorf("entries = %d, want capacity to hold under concurrency", entries)
	}
}
