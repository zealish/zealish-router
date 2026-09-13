package router

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/zealish/zealish-router/internal/provider"
	"github.com/zealish/zealish-router/internal/storage"
	"github.com/zealish/zealish-router/pkg/openai"
)

func reloadAliases(alias, providerName, model string) []storage.ModelAlias {
	return []storage.ModelAlias{{Alias: alias, Provider: providerName, Model: model}}
}

func TestReloadSwapsRoutingTable(t *testing.T) {
	before := &usageProvider{name: "openai"}
	after := &usageProvider{name: "ollama"}
	e := newTestEngine(t, nil, before, after)

	route, err := e.Resolve("gpt-5")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if route.Provider != "openai" {
		t.Fatalf("provider = %q, want openai", route.Provider)
	}

	e.Reload(reloadAliases("gpt-5", "ollama", "qwen3:32b"), nil, provider.NewRegistry(after))

	route, err = e.Resolve("gpt-5")
	if err != nil {
		t.Fatalf("Resolve after reload: %v", err)
	}
	if route.Provider != "ollama" || route.Model != "qwen3:32b" {
		t.Errorf("route = %+v, want ollama/qwen3:32b", route)
	}
}

func TestReloadRemovesStaleAliases(t *testing.T) {
	e := newTestEngine(t, nil, &usageProvider{name: "openai"})

	e.Reload(reloadAliases("only", "openai", "m"), nil, provider.NewRegistry(&usageProvider{name: "openai"}))

	if got := e.Aliases(); len(got) != 1 || got[0] != "only" {
		t.Errorf("aliases = %v, want [only]", got)
	}
	if _, err := e.Resolve("gpt-5"); err == nil {
		t.Error("removed alias still resolves")
	}
}

func TestReloadDuringConcurrentDispatch(t *testing.T) {
	p := &usageProvider{name: "openai"}
	e := newTestEngine(t, nil, p)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	// Readers hammer the request path while the table is swapped underneath.
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				req := &openai.ChatCompletionRequest{
					Model:    "gpt-5",
					Messages: []openai.Message{{Role: "user", Content: mustJSON("hi")}},
				}
				// Either config routes "gpt-5" to a live provider, so every
				// dispatch must succeed regardless of which snapshot it saw.
				if _, err := e.ChatCompletion(ctx, req); err != nil && ctx.Err() == nil {
					t.Errorf("ChatCompletion: %v", err)
					return
				}
				e.Chain("gpt-5")
				e.Aliases()
			}
		}()
	}

	for i := range 200 {
		model := "a"
		if i%2 == 1 {
			model = "b"
		}
		e.Reload(reloadAliases("gpt-5", "openai", model), nil, provider.NewRegistry(p))
	}

	cancel()
	wg.Wait()
}

func TestNewEngineIsUsableBeforeAnyReload(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	e := NewEngine(logger, nil)

	if _, err := e.Resolve("gpt-5"); err == nil {
		t.Fatal("empty engine resolved an alias")
	}

	e.Reload(reloadAliases("gpt-5", "openai", "m"), nil, provider.NewRegistry(&usageProvider{name: "openai"}))
	if _, err := e.Resolve("gpt-5"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
}
