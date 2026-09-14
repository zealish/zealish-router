// Package storage defines persistence contracts for API keys, providers and
// model aliases. The database is the source of truth for routing: providers
// and aliases are managed through the admin API, not the YAML file.
package storage

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

// ErrNotFound is returned when a record does not exist.
var ErrNotFound = errors.New("storage: not found")

// APIKey is a hashed credential record.
type APIKey struct {
	ID         string
	Name       string
	KeyHash    string
	Enabled    bool
	CreatedAt  time.Time
	LastUsedAt time.Time
}

// Provider is an upstream endpoint configuration.
type Provider struct {
	ID      string
	Name    string
	Kind    string
	BaseURL string
	APIKey  string
	Timeout time.Duration
	Enabled bool
	// Group classifies the provider as custom, oauth or api_key. It decides how
	// the credential is obtained, not how the upstream is spoken to.
	Group string
	// CatalogID names the preset a non-custom provider was created from.
	CatalogID string
	// AliasPrefix namespaces aliases imported from this provider so the same
	// upstream model name can be served by several providers.
	AliasPrefix string
	// UseProxyPool routes this provider's upstream traffic through the enabled
	// proxies in the proxy pool, rotated per request.
	UseProxyPool bool
}

// ModelAlias maps a client-facing alias onto a provider model, with an
// ordered fallback chain of other aliases.
type ModelAlias struct {
	Alias    string
	Provider string
	Model    string
	Fallback []string
}

// ComboStrategy selects how a combo picks the order of its member aliases.
type ComboStrategy string

const (
	// ComboFallback always starts at the first member and cascades on failure.
	ComboFallback ComboStrategy = "fallback"
	// ComboRoundRobin rotates the starting member per request, then cascades.
	ComboRoundRobin ComboStrategy = "round_robin"
)

// Combo is a virtual model: one client-facing name backed by an ordered pool
// of aliases tried according to Strategy.
type Combo struct {
	Name     string
	Strategy ComboStrategy
	Members  []string
	Enabled  bool
}

// Proxy is one outbound proxy endpoint in the pool. URL carries the scheme
// (http, https or socks5) and optional credentials.
type Proxy struct {
	Name    string
	URL     string
	Enabled bool
}

// APIKeyStore persists API keys.
type APIKeyStore interface {
	Create(ctx context.Context, key APIKey) error
	GetByHash(ctx context.Context, hash string) (APIKey, error)
	List(ctx context.Context) ([]APIKey, error)
	TouchLastUsed(ctx context.Context, id string, at time.Time) error
	Delete(ctx context.Context, id string) error
}

// ProviderStore persists upstream provider definitions.
type ProviderStore interface {
	List(ctx context.Context) ([]Provider, error)
	Get(ctx context.Context, name string) (Provider, error)
	Put(ctx context.Context, p Provider) error
	Delete(ctx context.Context, name string) error
}

// ModelStore persists model aliases and their fallback chains.
type ModelStore interface {
	List(ctx context.Context) ([]ModelAlias, error)
	Get(ctx context.Context, alias string) (ModelAlias, error)
	Put(ctx context.Context, m ModelAlias) error
	Delete(ctx context.Context, alias string) error
}

// ComboStore persists virtual models that group aliases behind one name.
type ComboStore interface {
	List(ctx context.Context) ([]Combo, error)
	Get(ctx context.Context, name string) (Combo, error)
	Put(ctx context.Context, c Combo) error
	Delete(ctx context.Context, name string) error
}

// ProxyStore persists the outbound proxy pool.
type ProxyStore interface {
	List(ctx context.Context) ([]Proxy, error)
	Get(ctx context.Context, name string) (Proxy, error)
	Put(ctx context.Context, p Proxy) error
	Delete(ctx context.Context, name string) error
}

// SettingStore persists free-form runtime settings as key/value pairs.
type SettingStore interface {
	All(ctx context.Context) (map[string]string, error)
	Put(ctx context.Context, key, value string) error
}

// UsageEvent is one completed chat request, priced at the time it finished.
type UsageEvent struct {
	ID               int64
	CreatedAt        time.Time
	Alias            string
	Provider         string
	Model            string
	Streamed         bool
	Status           string
	Duration         time.Duration
	PromptTokens     int
	CompletionTokens int
	CachedTokens     int
	CacheWriteTokens int
	ReasoningTokens  int
	CostUSD          float64
}

// UsageTotals aggregates usage over a time window.
type UsageTotals struct {
	Requests         int
	Errors           int
	PromptTokens     int
	CompletionTokens int
	CachedTokens     int
	CostUSD          float64
}

// UsageBucket is one point of a usage time series.
type UsageBucket struct {
	Start            time.Time
	Requests         int
	PromptTokens     int
	CompletionTokens int
	CachedTokens     int
	CostUSD          float64
}

// ModelUsage aggregates lifetime usage for one alias.
type ModelUsage struct {
	Alias            string
	Requests         int
	PromptTokens     int
	CompletionTokens int
	CachedTokens     int
	CostUSD          float64
	LastUsed         time.Time
}

// LeaderboardSort selects the metric a usage leaderboard is ranked by.
type LeaderboardSort string

const (
	LeaderboardByRequests LeaderboardSort = "requests"
	LeaderboardByTokens   LeaderboardSort = "tokens"
	LeaderboardByCost     LeaderboardSort = "cost"
)

// UsageStore persists and aggregates the per-request usage log.
type UsageStore interface {
	Record(ctx context.Context, e UsageEvent) error
	Recent(ctx context.Context, limit int) ([]UsageEvent, error)
	Totals(ctx context.Context, since time.Time) (UsageTotals, error)
	Series(ctx context.Context, since time.Time, bucket time.Duration) ([]UsageBucket, error)
	ByModel(ctx context.Context) ([]ModelUsage, error)
	Leaderboard(ctx context.Context, since time.Time, limit int, sortBy LeaderboardSort) ([]ModelUsage, error)
}

// Store aggregates every persistence contract of the application.
type Store interface {
	APIKeys() APIKeyStore
	Providers() ProviderStore
	Models() ModelStore
	Combos() ComboStore
	Proxies() ProxyStore
	Settings() SettingStore
	Usage() UsageStore
	Close() error
}

// Memory is an in-memory Store implementation.
type Memory struct {
	keys      *memoryAPIKeys
	providers *memoryProviders
	models    *memoryModels
	combos    *memoryCombos
	proxies   *memoryProxies
	settings  *memorySettings
	usage     *memoryUsage
}

// NewMemory constructs an empty in-memory store.
func NewMemory() *Memory {
	models := &memoryModels{items: map[string]ModelAlias{}}
	combos := &memoryCombos{items: map[string]Combo{}}
	usage := &memoryUsage{}
	return &Memory{
		keys:      &memoryAPIKeys{items: map[string]APIKey{}},
		providers: &memoryProviders{items: map[string]Provider{}, models: models, combos: combos, usage: usage},
		models:    models,
		combos:    combos,
		proxies:   &memoryProxies{items: map[string]Proxy{}},
		settings:  &memorySettings{items: map[string]string{}},
		usage:     usage,
	}
}

// APIKeys implements Store.
func (m *Memory) APIKeys() APIKeyStore { return m.keys }

// Providers implements Store.
func (m *Memory) Providers() ProviderStore { return m.providers }

// Models implements Store.
func (m *Memory) Models() ModelStore { return m.models }

// Combos implements Store.
func (m *Memory) Combos() ComboStore { return m.combos }

// Proxies implements Store.
func (m *Memory) Proxies() ProxyStore { return m.proxies }

// Settings implements Store.
func (m *Memory) Settings() SettingStore { return m.settings }

// Usage implements Store.
func (m *Memory) Usage() UsageStore { return m.usage }

// Close implements Store.
func (m *Memory) Close() error { return nil }

type memoryAPIKeys struct {
	mu    sync.RWMutex
	items map[string]APIKey
}

func (s *memoryAPIKeys) Create(_ context.Context, key APIKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[key.ID] = key
	return nil
}

func (s *memoryAPIKeys) GetByHash(_ context.Context, hash string) (APIKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, key := range s.items {
		if key.KeyHash == hash {
			return key, nil
		}
	}
	return APIKey{}, ErrNotFound
}

func (s *memoryAPIKeys) List(_ context.Context) ([]APIKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]APIKey, 0, len(s.items))
	for _, key := range s.items {
		out = append(out, key)
	}
	return out, nil
}

func (s *memoryAPIKeys) TouchLastUsed(_ context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, ok := s.items[id]
	if !ok {
		return ErrNotFound
	}
	key.LastUsedAt = at
	s.items[id] = key
	return nil
}

func (s *memoryAPIKeys) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.items[id]; !ok {
		return ErrNotFound
	}
	delete(s.items, id)
	return nil
}

type memoryProviders struct {
	mu     sync.RWMutex
	items  map[string]Provider
	models *memoryModels
	combos *memoryCombos
	usage  *memoryUsage
}

func (s *memoryProviders) List(_ context.Context) ([]Provider, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Provider, 0, len(s.items))
	for _, p := range s.items {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *memoryProviders) Get(_ context.Context, name string) (Provider, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.items[name]
	if !ok {
		return Provider{}, ErrNotFound
	}
	return p, nil
}

func (s *memoryProviders) Put(_ context.Context, p Provider) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[p.Name] = p
	return nil
}

// Delete removes the provider and everything that only exists because of it:
// its model aliases, references to those aliases in other fallback chains and
// combos, and its usage log. Nothing orphaned survives the provider it belongs
// to.
func (s *memoryProviders) Delete(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.items[name]; !ok {
		return ErrNotFound
	}
	delete(s.items, name)
	dropped := s.models.deleteProvider(name)
	s.combos.pruneMembers(dropped)
	s.usage.deleteProvider(name)
	return nil
}

type memoryModels struct {
	mu    sync.RWMutex
	items map[string]ModelAlias
}

func (s *memoryModels) List(_ context.Context) ([]ModelAlias, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ModelAlias, 0, len(s.items))
	for _, m := range s.items {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Alias < out[j].Alias })
	return out, nil
}

func (s *memoryModels) Get(_ context.Context, alias string) (ModelAlias, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.items[alias]
	if !ok {
		return ModelAlias{}, ErrNotFound
	}
	return m, nil
}

func (s *memoryModels) Put(_ context.Context, m ModelAlias) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[m.Alias] = m
	return nil
}

func (s *memoryModels) Delete(_ context.Context, alias string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.items[alias]; !ok {
		return ErrNotFound
	}
	delete(s.items, alias)
	return nil
}

// deleteProvider drops every alias served by a provider, strips those aliases
// from the fallback chains that still reference them, and reports their names.
func (s *memoryModels) deleteProvider(provider string) map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	dropped := map[string]bool{}
	for alias, m := range s.items {
		if m.Provider == provider {
			dropped[alias] = true
			delete(s.items, alias)
		}
	}
	if len(dropped) == 0 {
		return dropped
	}

	for alias, m := range s.items {
		kept := make([]string, 0, len(m.Fallback))
		for _, ref := range m.Fallback {
			if !dropped[ref] {
				kept = append(kept, ref)
			}
		}
		if len(kept) != len(m.Fallback) {
			m.Fallback = kept
			s.items[alias] = m
		}
	}
	return dropped
}

type memoryCombos struct {
	mu    sync.RWMutex
	items map[string]Combo
}

func (s *memoryCombos) List(_ context.Context) ([]Combo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Combo, 0, len(s.items))
	for _, c := range s.items {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *memoryCombos) Get(_ context.Context, name string) (Combo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.items[name]
	if !ok {
		return Combo{}, ErrNotFound
	}
	return c, nil
}

func (s *memoryCombos) Put(_ context.Context, c Combo) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[c.Name] = c
	return nil
}

func (s *memoryCombos) Delete(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.items[name]; !ok {
		return ErrNotFound
	}
	delete(s.items, name)
	return nil
}

// pruneMembers strips deleted aliases from every combo pool, dropping combos
// left without a single member.
func (s *memoryCombos) pruneMembers(dropped map[string]bool) {
	if len(dropped) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	for name, c := range s.items {
		kept := make([]string, 0, len(c.Members))
		for _, member := range c.Members {
			if !dropped[member] {
				kept = append(kept, member)
			}
		}
		if len(kept) == len(c.Members) {
			continue
		}
		if len(kept) == 0 {
			delete(s.items, name)
			continue
		}
		c.Members = kept
		s.items[name] = c
	}
}

type memoryProxies struct {
	mu    sync.RWMutex
	items map[string]Proxy
}

func (s *memoryProxies) List(_ context.Context) ([]Proxy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Proxy, 0, len(s.items))
	for _, p := range s.items {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *memoryProxies) Get(_ context.Context, name string) (Proxy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.items[name]
	if !ok {
		return Proxy{}, ErrNotFound
	}
	return p, nil
}

func (s *memoryProxies) Put(_ context.Context, p Proxy) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[p.Name] = p
	return nil
}

func (s *memoryProxies) Delete(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.items[name]; !ok {
		return ErrNotFound
	}
	delete(s.items, name)
	return nil
}

type memorySettings struct {
	mu    sync.RWMutex
	items map[string]string
}

func (s *memorySettings) All(_ context.Context) (map[string]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]string, len(s.items))
	for k, v := range s.items {
		out[k] = v
	}
	return out, nil
}

func (s *memorySettings) Put(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[key] = value
	return nil
}

// memoryUsage keeps the usage log in insertion order; tests and the in-memory
// store never hold enough events for the linear scans to matter.
type memoryUsage struct {
	mu     sync.RWMutex
	events []UsageEvent
}

func (s *memoryUsage) Record(_ context.Context, e UsageEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e.ID = int64(len(s.events) + 1)
	s.events = append(s.events, e)
	return nil
}

// deleteProvider drops every event recorded for a provider.
func (s *memoryUsage) deleteProvider(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.events[:0]
	for _, e := range s.events {
		if e.Provider != name {
			kept = append(kept, e)
		}
	}
	s.events = kept
}

func (s *memoryUsage) Recent(_ context.Context, limit int) ([]UsageEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]UsageEvent, 0, limit)
	for i := len(s.events) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, s.events[i])
	}
	return out, nil
}

func (s *memoryUsage) Totals(_ context.Context, since time.Time) (UsageTotals, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var t UsageTotals
	for _, e := range s.events {
		if e.CreatedAt.Before(since) {
			continue
		}
		t.Requests++
		if e.Status != "ok" {
			t.Errors++
		}
		t.PromptTokens += e.PromptTokens
		t.CompletionTokens += e.CompletionTokens
		t.CachedTokens += e.CachedTokens
		t.CostUSD += e.CostUSD
	}
	return t, nil
}

func (s *memoryUsage) Series(_ context.Context, since time.Time, bucket time.Duration) ([]UsageBucket, error) {
	if bucket <= 0 {
		return nil, nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	byStart := map[int64]*UsageBucket{}
	for _, e := range s.events {
		if e.CreatedAt.Before(since) {
			continue
		}
		start := e.CreatedAt.Truncate(bucket)
		b, ok := byStart[start.Unix()]
		if !ok {
			b = &UsageBucket{Start: start}
			byStart[start.Unix()] = b
		}
		b.Requests++
		b.PromptTokens += e.PromptTokens
		b.CompletionTokens += e.CompletionTokens
		b.CachedTokens += e.CachedTokens
		b.CostUSD += e.CostUSD
	}

	out := make([]UsageBucket, 0, len(byStart))
	for _, b := range byStart {
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out, nil
}

func (s *memoryUsage) ByModel(_ context.Context) ([]ModelUsage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	byAlias := map[string]*ModelUsage{}
	for _, e := range s.events {
		m, ok := byAlias[e.Alias]
		if !ok {
			m = &ModelUsage{Alias: e.Alias}
			byAlias[e.Alias] = m
		}
		m.Requests++
		m.PromptTokens += e.PromptTokens
		m.CompletionTokens += e.CompletionTokens
		m.CachedTokens += e.CachedTokens
		m.CostUSD += e.CostUSD
		if e.CreatedAt.After(m.LastUsed) {
			m.LastUsed = e.CreatedAt
		}
	}

	out := make([]ModelUsage, 0, len(byAlias))
	for _, m := range byAlias {
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastUsed.After(out[j].LastUsed) })
	return out, nil
}

func (s *memoryUsage) Leaderboard(_ context.Context, since time.Time, limit int, sortBy LeaderboardSort) ([]ModelUsage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	byAlias := map[string]*ModelUsage{}
	for _, e := range s.events {
		if e.CreatedAt.Before(since) {
			continue
		}
		m, ok := byAlias[e.Alias]
		if !ok {
			m = &ModelUsage{Alias: e.Alias}
			byAlias[e.Alias] = m
		}
		m.Requests++
		m.PromptTokens += e.PromptTokens
		m.CompletionTokens += e.CompletionTokens
		m.CachedTokens += e.CachedTokens
		m.CostUSD += e.CostUSD
		if e.CreatedAt.After(m.LastUsed) {
			m.LastUsed = e.CreatedAt
		}
	}

	out := make([]ModelUsage, 0, len(byAlias))
	for _, m := range byAlias {
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool {
		switch sortBy {
		case LeaderboardByTokens:
			return out[i].PromptTokens+out[i].CompletionTokens > out[j].PromptTokens+out[j].CompletionTokens
		case LeaderboardByCost:
			return out[i].CostUSD > out[j].CostUSD
		default:
			return out[i].Requests > out[j].Requests
		}
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
