package storage

import (
	"context"
	"errors"
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
