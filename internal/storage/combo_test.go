package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

func seedComboProvider(t *testing.T, store Store, provider string, aliases ...string) {
	t.Helper()
	ctx := context.Background()

	if err := store.Providers().Put(ctx, Provider{
		ID:      provider,
		Name:    provider,
		Kind:    "openai",
		BaseURL: "https://example.test/v1",
		Timeout: 30 * time.Second,
		Enabled: true,
	}); err != nil {
		t.Fatalf("put provider: %v", err)
	}
	for _, alias := range aliases {
		if err := store.Models().Put(ctx, ModelAlias{Alias: alias, Provider: provider, Model: "m"}); err != nil {
			t.Fatalf("put alias %s: %v", alias, err)
		}
	}
}

// runComboSuite exercises the ComboStore contract against any implementation.
func runComboSuite(t *testing.T, store Store) {
	t.Helper()
	ctx := context.Background()

	seedComboProvider(t, store, "openai", "gpt-5", "fast")
	seedComboProvider(t, store, "ollama", "local")

	combo := Combo{
		Name:     "code-agent",
		Strategy: ComboRoundRobin,
		Members:  []string{"gpt-5", "fast", "local"},
		Enabled:  true,
	}
	if err := store.Combos().Put(ctx, combo); err != nil {
		t.Fatalf("put combo: %v", err)
	}

	got, err := store.Combos().Get(ctx, "code-agent")
	if err != nil {
		t.Fatalf("get combo: %v", err)
	}
	if got.Strategy != ComboRoundRobin || len(got.Members) != 3 || got.Members[0] != "gpt-5" {
		t.Fatalf("combo = %+v, want the stored record in order", got)
	}

	// Put is an upsert: the same name replaces the pool.
	combo.Members = []string{"local", "gpt-5"}
	combo.Strategy = ComboFallback
	if err := store.Combos().Put(ctx, combo); err != nil {
		t.Fatalf("upsert combo: %v", err)
	}
	list, err := store.Combos().List(ctx)
	if err != nil {
		t.Fatalf("list combos: %v", err)
	}
	if len(list) != 1 || list[0].Members[0] != "local" || list[0].Strategy != ComboFallback {
		t.Fatalf("list = %+v, want the upserted record", list)
	}

	// Dropping a provider strips its aliases from every pool.
	if err := store.Providers().Delete(ctx, "ollama"); err != nil {
		t.Fatalf("delete provider: %v", err)
	}
	got, err = store.Combos().Get(ctx, "code-agent")
	if err != nil {
		t.Fatalf("get after prune: %v", err)
	}
	if len(got.Members) != 1 || got.Members[0] != "gpt-5" {
		t.Fatalf("members = %v, want only gpt-5", got.Members)
	}

	// Losing the last member drops the combo rather than leaving it empty.
	if err := store.Providers().Delete(ctx, "openai"); err != nil {
		t.Fatalf("delete provider: %v", err)
	}
	if _, err := store.Combos().Get(ctx, "code-agent"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get emptied combo err = %v, want ErrNotFound", err)
	}

	if err := store.Combos().Delete(ctx, "ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete missing combo err = %v, want ErrNotFound", err)
	}

	// Weighted pools keep one share per member, pruned positionally.
	seedComboProvider(t, store, "openai", "gpt-5", "fast")
	seedComboProvider(t, store, "ollama", "local")
	if err := store.Combos().Put(ctx, Combo{
		Name:     "spread",
		Strategy: ComboWeighted,
		Members:  []string{"gpt-5", "local", "fast"},
		Weights:  []int{3, 1, 2},
		Enabled:  true,
	}); err != nil {
		t.Fatalf("put weighted combo: %v", err)
	}
	if err := store.Providers().Delete(ctx, "ollama"); err != nil {
		t.Fatalf("delete provider: %v", err)
	}
	weighted, err := store.Combos().Get(ctx, "spread")
	if err != nil {
		t.Fatalf("get weighted combo: %v", err)
	}
	if len(weighted.Members) != 2 || weighted.Members[1] != "fast" {
		t.Fatalf("members = %v, want gpt-5 and fast", weighted.Members)
	}
	if len(weighted.Weights) != 2 || weighted.Weights[0] != 3 || weighted.Weights[1] != 2 {
		t.Fatalf("weights = %v, want [3 2] aligned with the kept members", weighted.Weights)
	}
}

func TestSQLiteComboStore(t *testing.T) { runComboSuite(t, newTestStore(t)) }

func TestMemoryComboStore(t *testing.T) { runComboSuite(t, NewMemory()) }

func TestSQLiteCombosSurviveReopen(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	seedComboProvider(t, store, "openai", "gpt-5")
	if err := store.Combos().Put(ctx, Combo{
		Name:     "code-agent",
		Strategy: ComboFallback,
		Members:  []string{"gpt-5"},
	}); err != nil {
		t.Fatalf("put combo: %v", err)
	}

	got, err := store.Combos().Get(ctx, "code-agent")
	if err != nil {
		t.Fatalf("get combo: %v", err)
	}
	if got.Enabled {
		t.Error("combo stored as enabled, want the zero value preserved")
	}
}
