package router

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/zealish/zealish-router/internal/provider"
	"github.com/zealish/zealish-router/internal/storage"
)

// Loader rebuilds an engine's routing table from persisted routing records.
// The database is the source of truth, so every mutation through the admin API
// ends with a Load to publish the new table.
type Loader struct {
	providers storage.ProviderStore
	models    storage.ModelStore
	combos    storage.ComboStore
	proxies   storage.ProxyStore
	engine    *Engine
}

// NewLoader wires a loader onto an engine.
func NewLoader(providers storage.ProviderStore, models storage.ModelStore, combos storage.ComboStore, proxies storage.ProxyStore, engine *Engine) *Loader {
	return &Loader{providers: providers, models: models, combos: combos, proxies: proxies, engine: engine}
}

// Load reads providers, aliases and combos and installs a fresh routing table.
func (l *Loader) Load(ctx context.Context) error {
	providers, err := l.providers.List(ctx)
	if err != nil {
		return fmt.Errorf("router: load providers: %w", err)
	}
	aliases, err := l.models.List(ctx)
	if err != nil {
		return fmt.Errorf("router: load model aliases: %w", err)
	}
	combos, err := l.combos.List(ctx)
	if err != nil {
		return fmt.Errorf("router: load combos: %w", err)
	}
	proxies, err := l.proxies.List(ctx)
	if err != nil {
		return fmt.Errorf("router: load proxies: %w", err)
	}

	l.engine.Reload(aliases, combos, buildRegistry(providers, NewProxyPool(proxies)))
	l.engine.SetBreakerOverrides(breakerOverrides(providers, l.engine.BreakerPolicy()))
	return nil
}

// buildRegistry instantiates one provider client per enabled record.
func buildRegistry(records []storage.Provider, pool *ProxyPool) *provider.Registry {
	clients := make([]provider.Provider, 0, len(records))
	for _, rec := range records {
		if !rec.Enabled {
			continue
		}
		clients = append(clients, NewProviderClient(rec, pool))
	}
	return provider.NewRegistry(clients...)
}

// breakerOverrides collects the per-provider circuit breaker policies. A
// record that overrides only one field inherits the other from the default,
// so a provider can retune its threshold without restating the cooldown.
func breakerOverrides(records []storage.Provider, base BreakerPolicy) map[string]BreakerPolicy {
	out := make(map[string]BreakerPolicy)
	for _, rec := range records {
		if rec.BreakerThreshold == nil && rec.BreakerCooldown == nil {
			continue
		}
		policy := base
		if rec.BreakerThreshold != nil {
			policy.FailureThreshold = *rec.BreakerThreshold
		}
		if rec.BreakerCooldown != nil {
			policy.Cooldown = *rec.BreakerCooldown
		}
		out[rec.Name] = policy
	}
	return out
}

// NewProviderClient instantiates a provider client for a stored record. Kind
// selects the wire dialect; an unknown kind falls back to plain OpenAI, which
// every compatible upstream speaks. The group decides how the credential is
// presented: OAuth tokens are always bearer tokens, whatever the dialect.
func NewProviderClient(rec storage.Provider, pool *ProxyPool) provider.Provider {
	client := &http.Client{Timeout: rec.Timeout}
	// Providers opted into the proxy pool get a transport that rotates across
	// the enabled proxies; everyone else keeps the default direct transport.
	if rec.UseProxyPool {
		if transport := pool.transport(); transport != nil {
			client.Transport = transport
		}
	}
	opts := provider.Options{
		Name:         rec.Name,
		BaseURL:      rec.BaseURL,
		APIKey:       rec.APIKey,
		APIKeys:      rec.APIKeys,
		APIKeyMethod: rec.APIKeyMethod,
		HTTPClient:   client,
	}
	if rec.Name == "commandcode" || rec.CatalogID == "commandcode" {
		return provider.NewCommandCode(opts)
	}
	if rec.Group == string(provider.GroupOAuth) {
		opts.AuthHeader = "Authorization"
		opts.APIKey = bearer(rec.APIKey)
		for i, key := range opts.APIKeys {
			opts.APIKeys[i] = bearer(key)
		}
	}
	if provider.NormalizeKind(rec.Kind) == provider.KindAnthropic {
		return provider.NewAnthropic(opts)
	}
	return provider.NewOpenAI(opts)
}

// bearer prefixes a raw OAuth token, tolerating a token stored with the scheme
// already attached.
func bearer(token string) string {
	if token == "" || strings.HasPrefix(token, "Bearer ") {
		return token
	}
	return "Bearer " + token
}
