package router

import (
	"net/http"
	"testing"

	"github.com/zealish/zealish-router/internal/provider"
	"github.com/zealish/zealish-router/internal/storage"
)

func TestNewProxyPoolSkipsDisabledAndInvalid(t *testing.T) {
	pool := NewProxyPool([]storage.Proxy{
		{Name: "a", URL: "http://proxy-a:8080", Enabled: true},
		{Name: "b", URL: "http://proxy-b:8080", Enabled: false},
		{Name: "c", URL: "://not-a-url", Enabled: true},
		{Name: "d", URL: "socks5://user:pass@proxy-d:1080", Enabled: true},
	})
	if pool.Size() != 2 {
		t.Fatalf("Size = %d, want 2 (disabled and unparseable skipped)", pool.Size())
	}
}

func TestProxyPoolRotatesRoundRobin(t *testing.T) {
	pool := NewProxyPool([]storage.Proxy{
		{Name: "a", URL: "http://proxy-a:8080", Enabled: true},
		{Name: "b", URL: "http://proxy-b:8080", Enabled: true},
	})

	got := []string{pool.pick().Host, pool.pick().Host, pool.pick().Host}
	want := []string{"proxy-a:8080", "proxy-b:8080", "proxy-a:8080"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pick #%d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestEmptyProxyPoolMeansDirect(t *testing.T) {
	pool := NewProxyPool(nil)
	if pool.transport() != nil {
		t.Error("transport() != nil, want nil for an empty pool")
	}
	if pool.pick() != nil {
		t.Error("pick() != nil, want nil for an empty pool")
	}
}

func TestNewProviderClientHonoursUseProxyPool(t *testing.T) {
	pool := NewProxyPool([]storage.Proxy{{Name: "a", URL: "http://proxy-a:8080", Enabled: true}})

	transportOf := func(rec storage.Provider) http.RoundTripper {
		client := clientOf(t, NewProviderClient(rec, pool))
		return client.Transport
	}

	direct := storage.Provider{Name: "direct", BaseURL: "http://up", Enabled: true}
	if transportOf(direct) != nil {
		t.Error("provider without use_proxy_pool got a proxy transport")
	}

	pooled := storage.Provider{Name: "pooled", BaseURL: "http://up", Enabled: true, UseProxyPool: true}
	rt := transportOf(pooled)
	transport, ok := rt.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T, want *http.Transport with a Proxy func", rt)
	}
	u, err := transport.Proxy(nil)
	if err != nil || u == nil || u.Host != "proxy-a:8080" {
		t.Errorf("Proxy() = %v, %v; want proxy-a:8080", u, err)
	}
}

func TestCommandCodeAliasesUseNativeProvider(t *testing.T) {
	for _, rec := range []storage.Provider{
		{Name: "commandcode", Kind: "openai", BaseURL: "https://api.commandcode.ai"},
		{Name: "cmc", Kind: "openai", BaseURL: "https://api.commandcode.ai"},
		{Name: "custom", CatalogID: "commandcode", Kind: "openai", BaseURL: "https://api.commandcode.ai"},
		{Name: "custom-cmc", CatalogID: "cmc", Kind: "openai", BaseURL: "https://api.commandcode.ai"},
	} {
		got := NewProviderClient(rec, NewProxyPool(nil))
		if got.Name() != rec.Name {
			t.Errorf("record %+v produced provider %q, want native provider named %q", rec, got.Name(), rec.Name)
		}
		if _, ok := got.(*provider.CommandCode); !ok {
			t.Errorf("record %+v produced %T, want *provider.CommandCode", rec, got)
		}
		engine := newTestEngine(t, nil, got)
		engine.Reload([]storage.ModelAlias{
			{Alias: "cc/gpt-5", Provider: rec.Name, Model: "gpt-5"},
			{Alias: "cmc/gpt-5", Provider: rec.Name, Model: "gpt-5"},
		}, nil, provider.NewRegistry(got))
		for _, alias := range []string{"cc/gpt-5", "cmc/gpt-5"} {
			route, err := engine.Resolve(alias)
			if err != nil || route.Provider != rec.Name || route.Model != "gpt-5" {
				t.Errorf("Resolve(%q) = %+v, %v", alias, route, err)
			}
		}
	}
}

// clientOf digs the HTTP client out of a constructed provider for inspection.
func clientOf(t *testing.T, p any) *http.Client {
	t.Helper()
	type optioned interface{ Client() *http.Client }
	if o, ok := p.(optioned); ok {
		return o.Client()
	}
	t.Fatalf("provider %T does not expose its client", p)
	return nil
}
