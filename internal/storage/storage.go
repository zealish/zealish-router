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
}

// ModelAlias maps a client-facing alias onto a provider model, with an
// ordered fallback chain of other aliases.
type ModelAlias struct {
	Alias    string
	Provider string
	Model    string
	Fallback []string
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

// SettingStore persists free-form runtime settings as key/value pairs.
type SettingStore interface {
	All(ctx context.Context) (map[string]string, error)
	Put(ctx context.Context, key, value string) error
}

// Store aggregates every persistence contract of the application.
type Store interface {
	APIKeys() APIKeyStore
	Providers() ProviderStore
	Models() ModelStore
	Settings() SettingStore
	Close() error
}

// Memory is an in-memory Store implementation.
type Memory struct {
	keys      *memoryAPIKeys
	providers *memoryProviders
	models    *memoryModels
	settings  *memorySettings
}

// NewMemory constructs an empty in-memory store.
func NewMemory() *Memory {
	return &Memory{
		keys:      &memoryAPIKeys{items: map[string]APIKey{}},
		providers: &memoryProviders{items: map[string]Provider{}},
		models:    &memoryModels{items: map[string]ModelAlias{}},
		settings:  &memorySettings{items: map[string]string{}},
	}
}

// APIKeys implements Store.
func (m *Memory) APIKeys() APIKeyStore { return m.keys }

// Providers implements Store.
func (m *Memory) Providers() ProviderStore { return m.providers }

// Models implements Store.
func (m *Memory) Models() ModelStore { return m.models }

// Settings implements Store.
func (m *Memory) Settings() SettingStore { return m.settings }

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
	mu    sync.RWMutex
	items map[string]Provider
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

func (s *memoryProviders) Delete(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.items[name]; !ok {
		return ErrNotFound
	}
	delete(s.items, name)
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
