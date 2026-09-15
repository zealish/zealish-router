// Package cache owns the in-process response cache. It stores serialized
// responses keyed on the exact request body, bounded by both a TTL and a
// maximum entry count, and has no dependency on any other internal package.
package cache

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// Entry is a cached response body together with the accounting the dashboard
// reports. Body is the serialized JSON exactly as it was written to the client
// that missed.
type Entry struct {
	Body []byte
	// Model is the requested model name, kept for observability only; the key
	// already covers it.
	Model string
	// StoredAt is when the entry was admitted, used to compute its age.
	StoredAt time.Time
}

// Stats is a point-in-time read of cache effectiveness.
type Stats struct {
	Entries  int   `json:"entries"`
	Capacity int   `json:"capacity"`
	Hits     int64 `json:"hits"`
	Misses   int64 `json:"misses"`
	Stores   int64 `json:"stores"`
	// Evictions counts entries dropped to stay under capacity. Expired
	// entries are not evictions; they simply aged out.
	Evictions int64 `json:"evictions"`
}

// HitRate is hits over lookups, 0 when nothing has been looked up yet.
func (s Stats) HitRate() float64 {
	lookups := s.Hits + s.Misses
	if lookups == 0 {
		return 0
	}
	return float64(s.Hits) / float64(lookups)
}

type node struct {
	key     string
	entry   Entry
	expires time.Time
}

// Cache is a fixed-capacity LRU of responses with a per-entry TTL. A nil
// *Cache is a valid disabled cache: every lookup misses and every store is a
// no-op, so callers need no nil checks of their own.
type Cache struct {
	ttl      time.Duration
	capacity int
	// now is the clock, swapped in tests to age entries without sleeping.
	now func() time.Time

	mu      sync.Mutex
	entries map[string]*list.Element
	order   *list.List // front = most recently used

	hits      int64
	misses    int64
	stores    int64
	evictions int64
}

// New builds a cache holding at most capacity entries for ttl each. A
// non-positive ttl or capacity disables caching and returns nil, which every
// method tolerates.
func New(ttl time.Duration, capacity int) *Cache {
	if ttl <= 0 || capacity <= 0 {
		return nil
	}
	return &Cache{
		ttl:      ttl,
		capacity: capacity,
		now:      time.Now,
		entries:  make(map[string]*list.Element, capacity),
		order:    list.New(),
	}
}

// Enabled reports whether lookups can hit.
func (c *Cache) Enabled() bool { return c != nil }

// Key derives the cache key for a request. The body is hashed rather than
// stored so a large prompt costs 32 bytes of key, and the endpoint is mixed in
// so a chat and an embeddings request with identical bodies never collide.
func Key(endpoint, model string, body []byte) string {
	sum := sha256.New()
	sum.Write([]byte(endpoint))
	sum.Write([]byte{0})
	sum.Write([]byte(model))
	sum.Write([]byte{0})
	sum.Write(body)
	return hex.EncodeToString(sum.Sum(nil))
}

// Get returns the cached entry for key. An expired entry is dropped and
// reported as a miss.
func (c *Cache) Get(key string) (Entry, bool) {
	if c == nil {
		return Entry{}, false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	elem, ok := c.entries[key]
	if !ok {
		c.misses++
		return Entry{}, false
	}

	n := elem.Value.(*node)
	if !c.now().Before(n.expires) {
		c.remove(elem)
		c.misses++
		return Entry{}, false
	}

	c.order.MoveToFront(elem)
	c.hits++
	return n.entry, true
}

// Put stores body under key, replacing any existing entry and evicting the
// least recently used one when the cache is full.
func (c *Cache) Put(key string, entry Entry) {
	if c == nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	entry.StoredAt = now
	c.stores++

	if elem, ok := c.entries[key]; ok {
		n := elem.Value.(*node)
		n.entry = entry
		n.expires = now.Add(c.ttl)
		c.order.MoveToFront(elem)
		return
	}

	c.entries[key] = c.order.PushFront(&node{key: key, entry: entry, expires: now.Add(c.ttl)})
	for c.order.Len() > c.capacity {
		c.remove(c.order.Back())
		c.evictions++
	}
}

// Purge drops every entry and returns how many were dropped. Counters survive,
// so a purge does not erase the hit rate history.
func (c *Cache) Purge() int {
	if c == nil {
		return 0
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	dropped := c.order.Len()
	c.entries = make(map[string]*list.Element, c.capacity)
	c.order.Init()
	return dropped
}

// Stats reports current occupancy and lifetime counters.
func (c *Cache) Stats() Stats {
	if c == nil {
		return Stats{}
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	return Stats{
		Entries:   c.order.Len(),
		Capacity:  c.capacity,
		Hits:      c.hits,
		Misses:    c.misses,
		Stores:    c.stores,
		Evictions: c.evictions,
	}
}

// TTL returns the configured entry lifetime, 0 when disabled.
func (c *Cache) TTL() time.Duration {
	if c == nil {
		return 0
	}
	return c.ttl
}

// remove unlinks an element from both the map and the LRU order. The caller
// holds the lock.
func (c *Cache) remove(elem *list.Element) {
	n := elem.Value.(*node)
	delete(c.entries, n.key)
	c.order.Remove(elem)
}
