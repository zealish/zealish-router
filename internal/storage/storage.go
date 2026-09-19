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

// ErrConflict is returned when an operation would overwrite an existing record.
var ErrConflict = errors.New("storage: conflict")

// APIKey is a hashed credential record.
type APIKey struct {
	ID         string
	Name       string
	KeyHash    string
	Enabled    bool
	CreatedAt  time.Time
	LastUsedAt time.Time
	// RateLimitPerMin caps requests per rolling minute. 0 means unlimited.
	RateLimitPerMin int
	// MonthlyBudgetUSD caps spend in the current calendar month. 0 means
	// unlimited.
	MonthlyBudgetUSD float64
	// AllowedModels restricts which aliases and combos the key may address.
	// Empty means every model, which is what keys created before the
	// allowlist shipped report.
	AllowedModels []string
}

// Provider is an upstream endpoint configuration.
type Provider struct {
	ID      string
	Name    string
	Kind    string
	BaseURL string
	// APIKey is the legacy primary credential. APIKeys is authoritative when
	// non-empty, while APIKey remains populated for compatibility and redaction.
	APIKey  string
	APIKeys []string
	// APIKeyMethod controls credential selection. Empty and "off" use APIKey;
	// "round_robin" rotates APIKeys atomically per request.
	APIKeyMethod string
	Timeout      time.Duration
	Enabled      bool
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
	// BreakerThreshold overrides the configured circuit breaker threshold for
	// this provider. Nil inherits the global policy; 0 disables the breaker.
	BreakerThreshold *int
	// BreakerCooldown overrides the configured breaker cooldown. Nil inherits
	// the global policy.
	BreakerCooldown *time.Duration
}

// Pricing contains static USD prices per million tokens.
type Pricing struct {
	Input  float64
	Output float64
}

// ModelAlias maps a client-facing alias onto a provider model, with an
// ordered fallback chain of other aliases.
type ModelAlias struct {
	Alias    string
	Provider string
	Model    string
	Fallback []string
	// Capabilities advertises what the route serves: chat, vision, tools,
	// embeddings, reasoning, streaming, audio, json_mode. It is generated on
	// import and editable afterwards, so a custom provider is never stuck with
	// a guess. Empty means the alias has not been classified.
	Capabilities []string
	// MaxContext is the model context window in tokens. Zero means unknown,
	// including for aliases created before this metadata was introduced.
	MaxContext int
	// QualityTier is a 0..100 static quality score. Zero means unclassified.
	QualityTier int
	Pricing     Pricing
}

// ComboStrategy selects how a combo picks the order of its member aliases.
type ComboStrategy string

const (
	// ComboFallback always starts at the first member and cascades on failure.
	ComboFallback ComboStrategy = "fallback"
	// ComboRoundRobin rotates the starting member per request, then cascades.
	ComboRoundRobin ComboStrategy = "round_robin"
	// ComboWeighted rotates the starting member per request in proportion to
	// the member weights, then cascades.
	ComboWeighted ComboStrategy = "weighted"
	// ComboIntelligent orders the pool per request by observed behaviour —
	// circuit and probe health first, then success rate and latency — so the
	// member most likely to answer fastest is tried first.
	ComboIntelligent ComboStrategy = "intelligent"
)

// Combo is a virtual model: one client-facing name backed by an ordered pool
// of aliases tried according to Strategy.
type Combo struct {
	Name     string
	Strategy ComboStrategy
	Members  []string
	// Weights holds one share per member, used by ComboWeighted. It is either
	// empty — every member weighs the same — or exactly as long as Members.
	Weights []int
	Enabled bool
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
	// SetQuota replaces the rate limit and monthly budget of one key.
	SetQuota(ctx context.Context, id string, perMin int, budgetUSD float64) error
	// SetAllowedModels replaces the model allowlist of one key. An empty
	// slice clears the restriction.
	SetAllowedModels(ctx context.Context, id string, models []string) error
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
	Rename(ctx context.Context, oldName, newName string) error
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
	ID        int64
	CreatedAt time.Time
	// KeyID attributes the request to the API key that authenticated it.
	// Empty means unattributed: auth disabled, a static key, or a row written
	// before per-key attribution existed.
	KeyID            string
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

// KeyUsage aggregates usage for one API key over a window.
type KeyUsage struct {
	KeyID            string
	Requests         int
	PromptTokens     int
	CompletionTokens int
	CachedTokens     int
	CostUSD          float64
	LastUsed         time.Time
}

// LeaderboardSort selects the metric a usage leaderboard is ranked by.
type LeaderboardSort string

// The metrics a leaderboard can be ranked by.
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
	// ByKey aggregates usage per API key since a point in time.
	ByKey(ctx context.Context, since time.Time) ([]KeyUsage, error)
	// KeySpend totals one key's cost since a point in time. It backs the
	// budget check on the request path, so it stays a single narrow read.
	KeySpend(ctx context.Context, keyID string, since time.Time) (float64, error)
	// Prune deletes events older than a cutoff and reports how many went.
	Prune(ctx context.Context, before time.Time) (int64, error)
}

// The wire dialects a client can speak. The gateway translates each onto one
// internal shape, so the dialect is recorded rather than inferred from a path.
const (
	DialectOpenAI    = "openai"
	DialectAnthropic = "anthropic"
	// DialectResponses is the OpenAI Responses API — same vendor as
	// DialectOpenAI, but a different wire shape and client population.
	DialectResponses = "responses"
)

// RequestTrace is the summary of one gateway request: a single row however
// many providers the fallback chain walked. It holds routing metadata only —
// never prompt or completion content.
type RequestTrace struct {
	// RequestID is the gateway-assigned identifier the client also sees.
	RequestID string
	CreatedAt time.Time
	// KeyID attributes the request to the API key that authenticated it.
	// Empty means unattributed, exactly as on UsageEvent.
	KeyID string
	// Model is the name the client asked for: an alias or a combo.
	Model string
	// Dialect is the wire format the *client* spoke: "openai" or "anthropic".
	// The upstream dialect is visible through each attempt's provider.
	Dialect  string
	Streamed bool
	// TotalLatency covers the whole request, including retries and backoff.
	TotalLatency time.Duration
	TotalTokens  int
	TotalCostUSD float64
	// FinalProvider and FinalAlias name the route that produced the outcome.
	FinalProvider string
	FinalAlias    string
	// FinalStatus is "ok" or the failure class of the last attempt.
	FinalStatus string
	// Attempts is the ordered timeline. The list view leaves it empty on
	// purpose; Get always fills it.
	Attempts []RequestAttempt
	// AttemptCount is how many upstream calls the request made. It is stored
	// on the summary row so the list view needs no join.
	AttemptCount int
}

// RequestAttempt is one upstream call inside a trace.
type RequestAttempt struct {
	// Seq is the 1-based position of this attempt within its trace.
	Seq       int
	StartedAt time.Time
	// Alias is the route chosen for this attempt: the alias itself, or the
	// combo member that was picked.
	Alias    string
	Provider string
	// Model is the upstream model name the provider was called with.
	Model   string
	Latency time.Duration
	// Status is "ok" or a failure class: timeout, rate_limited, upstream_5xx,
	// connection, client_error or canceled.
	Status string
	// Retry marks a repeat of the same route; Fallback marks a move onto the
	// next route in the chain. The first attempt is neither.
	Retry    bool
	Fallback bool
	// Error is the upstream message, truncated. Empty on success.
	Error string
}

// TraceFilter narrows a trace listing. Zero values mean "no filter".
type TraceFilter struct {
	// Status matches FinalStatus exactly.
	Status string
	// Model matches the requested model name exactly.
	Model string
	// Provider matches any provider the request attempted, not only the final
	// one: a trace that fell back off a provider is still a trace about it.
	Provider string
	KeyID    string
	// Dialect matches the client-side wire format exactly.
	Dialect string
	// Limit and Offset paginate the newest-first listing.
	Limit  int
	Offset int
}

// TraceStore persists request traces and their attempts.
type TraceStore interface {
	// Record writes a trace and its attempts as one unit. Re-recording the
	// same request id replaces the previous trace.
	Record(ctx context.Context, t RequestTrace) error
	// List returns traces newest-first with their attempts, plus the total
	// number of traces matching the filter before pagination.
	List(ctx context.Context, f TraceFilter) ([]RequestTrace, int, error)
	// Get returns one trace with every attempt in sequence order.
	Get(ctx context.Context, requestID string) (RequestTrace, error)
	// Prune deletes traces older than a cutoff and reports how many went.
	Prune(ctx context.Context, before time.Time) (int64, error)
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
	Traces() TraceStore
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
	traces    *memoryTraces
}

// NewMemory constructs an empty in-memory store.
func NewMemory() *Memory {
	models := &memoryModels{items: map[string]ModelAlias{}}
	combos := &memoryCombos{items: map[string]Combo{}}
	usage := &memoryUsage{}
	traces := &memoryTraces{}
	return &Memory{
		keys:      &memoryAPIKeys{items: map[string]APIKey{}},
		providers: &memoryProviders{items: map[string]Provider{}, models: models, combos: combos, usage: usage},
		models:    models,
		combos:    combos,
		proxies:   &memoryProxies{items: map[string]Proxy{}},
		settings:  &memorySettings{items: map[string]string{}},
		usage:     usage,
		traces:    traces,
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

// Traces implements Store.
func (m *Memory) Traces() TraceStore { return m.traces }

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

func (s *memoryAPIKeys) SetQuota(_ context.Context, id string, perMin int, budgetUSD float64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, ok := s.items[id]
	if !ok {
		return ErrNotFound
	}
	key.RateLimitPerMin = perMin
	key.MonthlyBudgetUSD = budgetUSD
	s.items[id] = key
	return nil
}

func (s *memoryAPIKeys) SetAllowedModels(_ context.Context, id string, models []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, ok := s.items[id]
	if !ok {
		return ErrNotFound
	}
	key.AllowedModels = models
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

func (s *memoryCombos) Rename(_ context.Context, oldName, newName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	c, ok := s.items[oldName]
	if !ok {
		return ErrNotFound
	}
	if oldName == newName {
		return nil
	}
	if _, exists := s.items[newName]; exists {
		return ErrConflict
	}
	delete(s.items, oldName)
	c.Name = newName
	s.items[newName] = c
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
// left without a single member. Weights are positional, so they follow the
// members they belong to.
func (s *memoryCombos) pruneMembers(dropped map[string]bool) {
	if len(dropped) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	for name, c := range s.items {
		kept := make([]string, 0, len(c.Members))
		keptWeights := make([]int, 0, len(c.Weights))
		for i, member := range c.Members {
			if dropped[member] {
				continue
			}
			kept = append(kept, member)
			if i < len(c.Weights) {
				keptWeights = append(keptWeights, c.Weights[i])
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
		c.Weights = keptWeights
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

func (s *memoryUsage) ByKey(_ context.Context, since time.Time) ([]KeyUsage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	byKey := map[string]*KeyUsage{}
	for _, e := range s.events {
		if e.CreatedAt.Before(since) {
			continue
		}
		k, ok := byKey[e.KeyID]
		if !ok {
			k = &KeyUsage{KeyID: e.KeyID}
			byKey[e.KeyID] = k
		}
		k.Requests++
		k.PromptTokens += e.PromptTokens
		k.CompletionTokens += e.CompletionTokens
		k.CachedTokens += e.CachedTokens
		k.CostUSD += e.CostUSD
		if e.CreatedAt.After(k.LastUsed) {
			k.LastUsed = e.CreatedAt
		}
	}

	out := make([]KeyUsage, 0, len(byKey))
	for _, k := range byKey {
		out = append(out, *k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CostUSD > out[j].CostUSD })
	return out, nil
}

func (s *memoryUsage) KeySpend(_ context.Context, keyID string, since time.Time) (float64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var total float64
	for _, e := range s.events {
		if e.KeyID == keyID && !e.CreatedAt.Before(since) {
			total += e.CostUSD
		}
	}
	return total, nil
}

func (s *memoryUsage) Prune(_ context.Context, before time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	kept := s.events[:0]
	var removed int64
	for _, e := range s.events {
		if e.CreatedAt.Before(before) {
			removed++
			continue
		}
		kept = append(kept, e)
	}
	s.events = kept
	return removed, nil
}

// memoryTraces keeps traces in insertion order, like memoryUsage: the
// in-memory store never holds enough of them for the linear scans to matter.
type memoryTraces struct {
	mu    sync.RWMutex
	items []RequestTrace
}

func (s *memoryTraces) Record(_ context.Context, t RequestTrace) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	t.AttemptCount = len(t.Attempts)
	t.Dialect = dialectOrDefault(t.Dialect)
	for i := range s.items {
		if s.items[i].RequestID == t.RequestID {
			s.items[i] = t
			return nil
		}
	}
	s.items = append(s.items, t)
	return nil
}

func (s *memoryTraces) List(_ context.Context, f TraceFilter) ([]RequestTrace, int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	matched := make([]RequestTrace, 0, len(s.items))
	for _, t := range s.items {
		if traceMatches(t, f) {
			matched = append(matched, t)
		}
	}
	sort.SliceStable(matched, func(i, j int) bool {
		return matched[i].CreatedAt.After(matched[j].CreatedAt)
	})
	total := len(matched)

	if f.Offset >= len(matched) {
		return nil, total, nil
	}
	matched = matched[f.Offset:]
	if f.Limit > 0 && f.Limit < len(matched) {
		matched = matched[:f.Limit]
	}

	// The list view reads summaries; attempts belong to Get.
	out := make([]RequestTrace, 0, len(matched))
	for _, t := range matched {
		t.Attempts = nil
		out = append(out, t)
	}
	return out, total, nil
}

// traceMatches applies every set filter. Provider reaches into the attempts so
// a request that fell back off a provider still counts as a trace about it.
func traceMatches(t RequestTrace, f TraceFilter) bool {
	if f.Status != "" && t.FinalStatus != f.Status {
		return false
	}
	if f.Model != "" && t.Model != f.Model {
		return false
	}
	if f.Dialect != "" && t.Dialect != f.Dialect {
		return false
	}
	if f.KeyID != "" && t.KeyID != f.KeyID {
		return false
	}
	if f.Provider == "" {
		return true
	}
	for _, a := range t.Attempts {
		if a.Provider == f.Provider {
			return true
		}
	}
	return false
}

func (s *memoryTraces) Get(_ context.Context, requestID string) (RequestTrace, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, t := range s.items {
		if t.RequestID == requestID {
			return t, nil
		}
	}
	return RequestTrace{}, ErrNotFound
}

func (s *memoryTraces) Prune(_ context.Context, before time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	kept := s.items[:0]
	var removed int64
	for _, t := range s.items {
		if t.CreatedAt.Before(before) {
			removed++
			continue
		}
		kept = append(kept, t)
	}
	s.items = kept
	return removed, nil
}
