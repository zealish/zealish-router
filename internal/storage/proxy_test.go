package storage

import (
	"context"
	"errors"
	"testing"
)

// runProxySuite exercises the ProxyStore contract against any implementation.
func runProxySuite(t *testing.T, store Store) {
	t.Helper()
	ctx := context.Background()

	proxies := store.Proxies()

	if err := proxies.Put(ctx, Proxy{Name: "dc1", URL: "http://proxy-a:8080", Enabled: true}); err != nil {
		t.Fatalf("put proxy: %v", err)
	}
	if err := proxies.Put(ctx, Proxy{Name: "dc2", URL: "socks5://user:pass@proxy-b:1080", Enabled: false}); err != nil {
		t.Fatalf("put second proxy: %v", err)
	}

	got, err := proxies.Get(ctx, "dc2")
	if err != nil {
		t.Fatalf("get proxy: %v", err)
	}
	if got.URL != "socks5://user:pass@proxy-b:1080" || got.Enabled {
		t.Fatalf("proxy = %+v, want the stored record", got)
	}

	// Put is an upsert keyed by name.
	if err := proxies.Put(ctx, Proxy{Name: "dc1", URL: "http://proxy-c:3128", Enabled: false}); err != nil {
		t.Fatalf("upsert proxy: %v", err)
	}
	list, err := proxies.List(ctx)
	if err != nil {
		t.Fatalf("list proxies: %v", err)
	}
	if len(list) != 2 || list[0].Name != "dc1" || list[0].URL != "http://proxy-c:3128" || list[0].Enabled {
		t.Fatalf("list = %+v, want dc1 upserted and sorted first", list)
	}

	if err := proxies.Delete(ctx, "dc1"); err != nil {
		t.Fatalf("delete proxy: %v", err)
	}
	if _, err := proxies.Get(ctx, "dc1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get deleted proxy err = %v, want ErrNotFound", err)
	}
	if err := proxies.Delete(ctx, "ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete missing proxy err = %v, want ErrNotFound", err)
	}
}

func TestSQLiteProxyStore(t *testing.T) { runProxySuite(t, newTestStore(t)) }

func TestMemoryProxyStore(t *testing.T) { runProxySuite(t, NewMemory()) }

func TestProviderUseProxyPoolPersists(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	p := sampleProvider("openai")
	p.UseProxyPool = true
	if err := store.Providers().Put(ctx, p); err != nil {
		t.Fatalf("put provider: %v", err)
	}
	got, err := store.Providers().Get(ctx, "openai")
	if err != nil {
		t.Fatalf("get provider: %v", err)
	}
	if !got.UseProxyPool {
		t.Error("UseProxyPool = false, want true after round-trip")
	}
}
