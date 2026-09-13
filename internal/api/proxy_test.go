package api

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/zealish/zealish-router/internal/storage"
)

func TestProxyCRUD(t *testing.T) {
	h, store, _ := newAdminServer(t)

	rec := adminRequest(t, h, http.MethodPut, "/api/v1/proxies/dc1",
		`{"url":"http://user:pass@proxy-a:8080"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put proxy status = %d, body=%s", rec.Code, rec.Body.String())
	}
	created := decodeJSON[map[string]any](t, rec)
	if created["enabled"] != true {
		t.Errorf("enabled = %v, want true by default", created["enabled"])
	}

	rec = adminRequest(t, h, http.MethodGet, "/api/v1/proxies", "")
	list := decodeJSON[[]map[string]any](t, rec)
	if len(list) != 1 || list[0]["name"] != "dc1" || list[0]["url"] != "http://user:pass@proxy-a:8080" {
		t.Fatalf("list = %+v, want the stored proxy", list)
	}

	rec = adminRequest(t, h, http.MethodDelete, "/api/v1/proxies/dc1", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete proxy status = %d", rec.Code)
	}
	if _, err := store.Proxies().Get(context.Background(), "dc1"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("get deleted proxy err = %v, want ErrNotFound", err)
	}
}

func TestPutProxyRejectsBadURL(t *testing.T) {
	h, _, _ := newAdminServer(t)

	for _, body := range []string{
		`{"url":""}`,
		`{"url":"proxy-a:8080"}`,
		`{"url":"ftp://proxy-a:21"}`,
	} {
		rec := adminRequest(t, h, http.MethodPut, "/api/v1/proxies/bad", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("put %s status = %d, want 400", body, rec.Code)
		}
	}
}

func TestPutProviderStoresUseProxyPool(t *testing.T) {
	h, store, _ := newAdminServer(t)

	rec := adminRequest(t, h, http.MethodPut, "/api/v1/providers/openai",
		`{"base_url":"https://api.openai.com/v1","enabled":true,"use_proxy_pool":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put provider status = %d, body=%s", rec.Code, rec.Body.String())
	}

	stored, err := store.Providers().Get(context.Background(), "openai")
	if err != nil {
		t.Fatalf("get provider: %v", err)
	}
	if !stored.UseProxyPool {
		t.Error("UseProxyPool = false, want true")
	}
}

func TestImportProxiesParsesMixedFormats(t *testing.T) {
	h, store, _ := newAdminServer(t)

	body := `{"text":"# datacenter list\nhttp://user:pass@proxy-a:8080\nproxy-b:3128\nproxy-c:1080:bob:secret\n\nsocks5://proxy-d:1080\nnot a proxy\nproxy-e:99999"}`
	rec := adminRequest(t, h, http.MethodPost, "/api/v1/proxies/import", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("import status = %d, body=%s", rec.Code, rec.Body.String())
	}
	resp := decodeJSON[map[string]any](t, rec)

	imported := resp["imported"].([]any)
	if len(imported) != 4 {
		t.Fatalf("imported %d proxies, want 4 (body=%s)", len(imported), rec.Body.String())
	}
	invalid := resp["invalid"].([]any)
	if len(invalid) != 2 {
		t.Fatalf("invalid = %v, want the malformed line and the bad port", invalid)
	}

	ctx := context.Background()
	got, err := store.Proxies().Get(ctx, "proxy-b:3128")
	if err != nil {
		t.Fatalf("get bare host:port proxy: %v", err)
	}
	if got.URL != "http://proxy-b:3128" {
		t.Errorf("bare form URL = %q, want http scheme default", got.URL)
	}
	got, err = store.Proxies().Get(ctx, "proxy-c:1080")
	if err != nil {
		t.Fatalf("get host:port:user:pass proxy: %v", err)
	}
	if got.URL != "http://bob:secret@proxy-c:1080" {
		t.Errorf("credential form URL = %q, want embedded userinfo", got.URL)
	}
}

func TestImportProxiesSkipsExistingUnlessOverwrite(t *testing.T) {
	h, store, _ := newAdminServer(t)
	ctx := context.Background()

	if err := store.Proxies().Put(ctx, storage.Proxy{
		Name: "proxy-a:8080", URL: "http://old@proxy-a:8080", Enabled: true,
	}); err != nil {
		t.Fatalf("seed proxy: %v", err)
	}

	rec := adminRequest(t, h, http.MethodPost, "/api/v1/proxies/import",
		`{"text":"proxy-a:8080\nproxy-a:8080"}`)
	resp := decodeJSON[map[string]any](t, rec)
	if n := len(resp["skipped"].([]any)); n != 2 {
		t.Fatalf("skipped = %v, want existing + duplicate line", resp["skipped"])
	}

	rec = adminRequest(t, h, http.MethodPost, "/api/v1/proxies/import",
		`{"text":"proxy-a:8080","overwrite":true,"enabled":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("overwrite import status = %d", rec.Code)
	}
	got, err := store.Proxies().Get(ctx, "proxy-a:8080")
	if err != nil {
		t.Fatalf("get overwritten proxy: %v", err)
	}
	if got.URL != "http://proxy-a:8080" || got.Enabled {
		t.Errorf("proxy = %+v, want overwritten and disabled", got)
	}
}

func TestImportProxiesRejectsEmptyText(t *testing.T) {
	h, _, _ := newAdminServer(t)

	for _, body := range []string{`{"text":""}`, `{"text":"\n# only a comment\n"}`} {
		rec := adminRequest(t, h, http.MethodPost, "/api/v1/proxies/import", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("import %s status = %d, want 400", body, rec.Code)
		}
	}
}
