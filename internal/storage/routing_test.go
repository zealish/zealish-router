package storage

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

func sampleProvider(name string) Provider {
	return Provider{
		ID:      name,
		Name:    name,
		Kind:    "openai",
		BaseURL: "https://api.example.com/v1",
		APIKey:  "secret-" + name,
		Timeout: 30 * time.Second,
		Enabled: true,
	}
}

func TestProviderRoundTrip(t *testing.T) {
	ctx := context.Background()
	providers := newTestStore(t).Providers()

	want := sampleProvider("openai")
	if err := providers.Put(ctx, want); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := providers.Get(ctx, "openai")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != want {
		t.Errorf("provider = %+v, want %+v", got, want)
	}
}

func TestProviderPutUpserts(t *testing.T) {
	ctx := context.Background()
	providers := newTestStore(t).Providers()

	if err := providers.Put(ctx, sampleProvider("openai")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	updated := sampleProvider("openai")
	updated.BaseURL = "https://proxy.example.com/v1"
	updated.Enabled = false
	if err := providers.Put(ctx, updated); err != nil {
		t.Fatalf("Put update: %v", err)
	}

	all, err := providers.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("providers = %d, want 1", len(all))
	}
	if all[0].BaseURL != "https://proxy.example.com/v1" || all[0].Enabled {
		t.Errorf("provider = %+v, want the updated record", all[0])
	}
}

func TestProviderNotFound(t *testing.T) {
	ctx := context.Background()
	providers := newTestStore(t).Providers()

	if _, err := providers.Get(ctx, "ghost"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get: err = %v, want ErrNotFound", err)
	}
	if err := providers.Delete(ctx, "ghost"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete: err = %v, want ErrNotFound", err)
	}
}

func TestModelAliasRoundTripWithFallback(t *testing.T) {
	ctx := context.Background()
	models := newTestStore(t).Models()

	want := ModelAlias{
		Alias:    "gpt-5",
		Provider: "openai",
		Model:    "gpt-5-upstream",
		Fallback: []string{"fast", "local"},
	}
	if err := models.Put(ctx, want); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := models.Get(ctx, "gpt-5")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Provider != want.Provider || got.Model != want.Model {
		t.Errorf("alias = %+v, want %+v", got, want)
	}
	if len(got.Fallback) != 2 || got.Fallback[0] != "fast" || got.Fallback[1] != "local" {
		t.Errorf("fallback = %v, want [fast local]", got.Fallback)
	}
}

func TestModelAliasEmptyFallbackIsNil(t *testing.T) {
	ctx := context.Background()
	models := newTestStore(t).Models()

	if err := models.Put(ctx, ModelAlias{Alias: "solo", Provider: "openai", Model: "m"}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := models.Get(ctx, "solo")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Fallback != nil {
		t.Errorf("fallback = %v, want nil", got.Fallback)
	}
}

func TestModelAliasRoundTripsCapabilities(t *testing.T) {
	ctx := context.Background()
	models := newTestStore(t).Models()

	want := []string{"chat", "vision", "streaming"}
	if err := models.Put(ctx, ModelAlias{
		Alias:        "gpt-4o",
		Provider:     "openai",
		Model:        "gpt-4o",
		Capabilities: want,
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := models.Get(ctx, "gpt-4o")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !slices.Equal(got.Capabilities, want) {
		t.Errorf("capabilities = %v, want %v", got.Capabilities, want)
	}
}

// An alias predating capabilities reads back unclassified, not as a model
// that supports nothing.
func TestModelAliasEmptyCapabilitiesIsNil(t *testing.T) {
	ctx := context.Background()
	models := newTestStore(t).Models()

	if err := models.Put(ctx, ModelAlias{Alias: "solo", Provider: "openai", Model: "m"}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := models.Get(ctx, "solo")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Capabilities != nil {
		t.Errorf("capabilities = %v, want nil", got.Capabilities)
	}
}

func TestModelAliasListIsOrdered(t *testing.T) {
	ctx := context.Background()
	models := newTestStore(t).Models()

	for _, alias := range []string{"c", "a", "b"} {
		if err := models.Put(ctx, ModelAlias{Alias: alias, Provider: "openai", Model: "m"}); err != nil {
			t.Fatalf("Put %s: %v", alias, err)
		}
	}

	all, err := models.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for i, want := range []string{"a", "b", "c"} {
		if all[i].Alias != want {
			t.Fatalf("aliases = %+v, want a,b,c order", all)
		}
	}
}

func TestSettingsUpsert(t *testing.T) {
	ctx := context.Background()
	settings := newTestStore(t).Settings()

	if err := settings.Put(ctx, "log_level", "info"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := settings.Put(ctx, "log_level", "debug"); err != nil {
		t.Fatalf("Put update: %v", err)
	}

	all, err := settings.All(ctx)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(all) != 1 || all["log_level"] != "debug" {
		t.Errorf("settings = %v, want log_level=debug", all)
	}
}

// The in-memory store backs tests elsewhere, so it must honour the same
// contract as the SQLite one.
func TestMemoryRoutingStores(t *testing.T) {
	ctx := context.Background()
	store := NewMemory()

	if err := store.Providers().Put(ctx, sampleProvider("openai")); err != nil {
		t.Fatalf("Put provider: %v", err)
	}
	if err := store.Models().Put(ctx, ModelAlias{Alias: "gpt-5", Provider: "openai", Model: "m"}); err != nil {
		t.Fatalf("Put alias: %v", err)
	}
	if err := store.Settings().Put(ctx, "k", "v"); err != nil {
		t.Fatalf("Put setting: %v", err)
	}

	if _, err := store.Providers().Get(ctx, "openai"); err != nil {
		t.Errorf("Get provider: %v", err)
	}
	if _, err := store.Models().Get(ctx, "gpt-5"); err != nil {
		t.Errorf("Get alias: %v", err)
	}
	if _, err := store.Models().Get(ctx, "ghost"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get unknown alias: err = %v, want ErrNotFound", err)
	}
}

// Deleting a provider must leave nothing behind that points at it.
func TestProviderDeleteCascades(t *testing.T) {
	for _, tc := range []struct {
		name  string
		store func(t *testing.T) Store
	}{
		{"sqlite", func(t *testing.T) Store { return newTestStore(t) }},
		{"memory", func(*testing.T) Store { return NewMemory() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store := tc.store(t)

			for _, name := range []string{"openai", "groq"} {
				if err := store.Providers().Put(ctx, sampleProvider(name)); err != nil {
					t.Fatalf("Put provider %s: %v", name, err)
				}
			}
			aliases := []ModelAlias{
				{Alias: "gpt-5", Provider: "openai", Model: "m"},
				{Alias: "groq-fast", Provider: "groq", Model: "m", Fallback: []string{"gpt-5", "groq-slow"}},
				{Alias: "groq-slow", Provider: "groq", Model: "m"},
			}
			for _, a := range aliases {
				if err := store.Models().Put(ctx, a); err != nil {
					t.Fatalf("Put alias %s: %v", a.Alias, err)
				}
			}
			for _, provider := range []string{"openai", "groq"} {
				if err := store.Usage().Record(ctx, UsageEvent{
					CreatedAt: time.Now(), Alias: "a", Provider: provider, Model: "m", Status: "ok",
				}); err != nil {
					t.Fatalf("Record usage %s: %v", provider, err)
				}
			}

			if err := store.Providers().Delete(ctx, "openai"); err != nil {
				t.Fatalf("Delete: %v", err)
			}

			if _, err := store.Models().Get(ctx, "gpt-5"); !errors.Is(err, ErrNotFound) {
				t.Errorf("alias gpt-5: err = %v, want ErrNotFound", err)
			}
			remaining, err := store.Models().Get(ctx, "groq-fast")
			if err != nil {
				t.Fatalf("Get groq-fast: %v", err)
			}
			if len(remaining.Fallback) != 1 || remaining.Fallback[0] != "groq-slow" {
				t.Errorf("fallback = %v, want [groq-slow]", remaining.Fallback)
			}

			events, err := store.Usage().Recent(ctx, 10)
			if err != nil {
				t.Fatalf("Recent: %v", err)
			}
			if len(events) != 1 || events[0].Provider != "groq" {
				t.Errorf("usage = %+v, want only the groq event", events)
			}
		})
	}
}

func TestProviderBreakerOverrideRoundTrip(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()

	threshold := 2
	cooldown := 90 * time.Second
	rec := Provider{
		ID: "acme", Name: "acme", Kind: "openai", BaseURL: "https://acme.test/v1",
		Timeout: 30 * time.Second, Enabled: true,
		BreakerThreshold: &threshold, BreakerCooldown: &cooldown,
	}
	if err := store.Providers().Put(ctx, rec); err != nil {
		t.Fatalf("put provider: %v", err)
	}

	got, err := store.Providers().Get(ctx, "acme")
	if err != nil {
		t.Fatalf("get provider: %v", err)
	}
	if got.BreakerThreshold == nil || *got.BreakerThreshold != 2 {
		t.Errorf("BreakerThreshold = %v, want 2", got.BreakerThreshold)
	}
	if got.BreakerCooldown == nil || *got.BreakerCooldown != 90*time.Second {
		t.Errorf("BreakerCooldown = %v, want 90s", got.BreakerCooldown)
	}
}

func TestProviderWithoutBreakerOverrideInherits(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()

	// No override stored: the columns stay NULL so the provider inherits the
	// global policy rather than reading as "breaker disabled".
	rec := Provider{ID: "plain", Name: "plain", Kind: "openai",
		BaseURL: "https://plain.test/v1", Timeout: 30 * time.Second, Enabled: true}
	if err := store.Providers().Put(ctx, rec); err != nil {
		t.Fatalf("put provider: %v", err)
	}

	got, err := store.Providers().Get(ctx, "plain")
	if err != nil {
		t.Fatalf("get provider: %v", err)
	}
	if got.BreakerThreshold != nil {
		t.Errorf("BreakerThreshold = %v, want nil", *got.BreakerThreshold)
	}
	if got.BreakerCooldown != nil {
		t.Errorf("BreakerCooldown = %v, want nil", *got.BreakerCooldown)
	}
}

func TestProviderBreakerOverrideCanBeCleared(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()

	threshold := 7
	base := Provider{ID: "acme", Name: "acme", Kind: "openai",
		BaseURL: "https://acme.test/v1", Timeout: 30 * time.Second, Enabled: true}

	withOverride := base
	withOverride.BreakerThreshold = &threshold
	if err := store.Providers().Put(ctx, withOverride); err != nil {
		t.Fatalf("put with override: %v", err)
	}
	if err := store.Providers().Put(ctx, base); err != nil {
		t.Fatalf("put without override: %v", err)
	}

	got, err := store.Providers().Get(ctx, "acme")
	if err != nil {
		t.Fatalf("get provider: %v", err)
	}
	if got.BreakerThreshold != nil {
		t.Errorf("BreakerThreshold = %v, want nil after clearing", *got.BreakerThreshold)
	}
}
