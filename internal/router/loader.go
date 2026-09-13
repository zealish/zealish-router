package router

import (
	"context"
	"fmt"
	"net/http"

	"github.com/zealish/zealish-router/internal/provider"
	"github.com/zealish/zealish-router/internal/storage"
)

// Loader rebuilds an engine's routing table from persisted routing records.
// The database is the source of truth, so every mutation through the admin API
// ends with a Load to publish the new table.
type Loader struct {
	providers storage.ProviderStore
	models    storage.ModelStore
	engine    *Engine
}

// NewLoader wires a loader onto an engine.
func NewLoader(providers storage.ProviderStore, models storage.ModelStore, engine *Engine) *Loader {
	return &Loader{providers: providers, models: models, engine: engine}
}

// Load reads providers and aliases and installs a fresh routing table.
func (l *Loader) Load(ctx context.Context) error {
	providers, err := l.providers.List(ctx)
	if err != nil {
		return fmt.Errorf("router: load providers: %w", err)
	}
	aliases, err := l.models.List(ctx)
	if err != nil {
		return fmt.Errorf("router: load model aliases: %w", err)
	}

	l.engine.Reload(aliases, buildRegistry(providers))
	return nil
}

// buildRegistry instantiates one provider client per enabled record. Kind
// selects the wire dialect; an unknown kind falls back to plain OpenAI, which
// every compatible upstream speaks.
func buildRegistry(records []storage.Provider) *provider.Registry {
	clients := make([]provider.Provider, 0, len(records))
	for _, rec := range records {
		if !rec.Enabled {
			continue
		}
		opts := provider.Options{
			Name:       rec.Name,
			BaseURL:    rec.BaseURL,
			APIKey:     rec.APIKey,
			HTTPClient: &http.Client{Timeout: rec.Timeout},
		}
		switch rec.Kind {
		case "openrouter":
			clients = append(clients, provider.NewOpenRouter(opts))
		case "ollama":
			clients = append(clients, provider.NewOllama(opts))
		default:
			clients = append(clients, provider.NewOpenAI(opts))
		}
	}
	return provider.NewRegistry(clients...)
}
