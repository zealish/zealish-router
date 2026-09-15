package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zealish/zealish-router/internal/auth"
	"github.com/zealish/zealish-router/internal/config"
	"github.com/zealish/zealish-router/internal/metrics"
	"github.com/zealish/zealish-router/internal/provider"
	"github.com/zealish/zealish-router/internal/router"
	"github.com/zealish/zealish-router/internal/storage"
)

const adminToken = "admin-secret"

// newAdminServer wires the admin API over an in-memory store.
func newAdminServer(t *testing.T) (http.Handler, storage.Store, *router.Engine) {
	t.Helper()

	cfg := config.Default()
	cfg.Auth.Enabled = false
	cfg.Admin = config.Admin{Enabled: true, Token: adminToken, Origins: []string{"http://localhost:3000"}}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	collector := metrics.New()
	store := storage.NewMemory()
	engine := router.NewEngine(logger, collector)
	loader := router.NewLoader(store.Providers(), store.Models(), store.Combos(), store.Proxies(), engine)

	deps := Dependencies{
		Config:    cfg,
		Engine:    engine,
		Loader:    loader,
		Store:     store,
		Auth:      auth.NewService(false, nil, nil, logger),
		AdminAuth: auth.NewAdminService(true, adminToken),
		Quota:     auth.NewQuota(store.Usage()),
		Metrics:   collector,
		Logger:    logger,
	}
	return newRoutes(deps, newHandler(deps)), store, engine
}

func adminRequest(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeJSON[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode body: %v (body=%q)", err, rec.Body.String())
	}
	return out
}

func TestAdminRequiresToken(t *testing.T) {
	h, _, _ := newAdminServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/overview", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status without a token = %d, want 401", rec.Code)
	}
}

func TestAdminRejectsGatewayKey(t *testing.T) {
	h, _, _ := newAdminServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/overview", nil)
	req.Header.Set("Authorization", "Bearer not-the-admin-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status with a wrong token = %d, want 401", rec.Code)
	}
}

func TestAdminDisabledLeavesRoutesUnmounted(t *testing.T) {
	cfg := config.Default()
	cfg.Auth.Enabled = false
	cfg.Admin.Enabled = false

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	collector := metrics.New()
	deps := Dependencies{
		Config:  cfg,
		Engine:  router.NewEngine(logger, collector),
		Store:   storage.NewMemory(),
		Auth:    auth.NewService(false, nil, nil, logger),
		Metrics: collector,
		Logger:  logger,
	}
	h := newRoutes(deps, newHandler(deps))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/overview", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 when admin is disabled", rec.Code)
	}
}

func TestAdminCORSPreflight(t *testing.T) {
	h, _, _ := newAdminServer(t)

	req := httptest.NewRequest(http.MethodOptions, "/api/v1/keys", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:3000" {
		t.Errorf("allow-origin = %q, want the configured origin", got)
	}
}

func TestAdminCORSRejectsUnknownOrigin(t *testing.T) {
	h, _, _ := newAdminServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/keys", nil)
	req.Header.Set("Origin", "http://evil.example.com")
	req.Header.Set("Authorization", "Bearer "+adminToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("allow-origin = %q, want empty for an unlisted origin", got)
	}
}

func TestAdminKeyLifecycle(t *testing.T) {
	h, _, _ := newAdminServer(t)

	rec := adminRequest(t, h, http.MethodPost, "/api/v1/keys", `{"name":"laptop"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201 (body=%q)", rec.Code, rec.Body.String())
	}

	created := decodeJSON[createKeyResponse](t, rec)
	if !strings.HasPrefix(created.Key, "zr_") {
		t.Errorf("raw key = %q, want a zr_ prefix", created.Key)
	}
	if created.ID == "" {
		t.Fatal("created key has no id")
	}

	listed := decodeJSON[[]apiKeyResponse](t, adminRequest(t, h, http.MethodGet, "/api/v1/keys", ""))
	if len(listed) != 1 || listed[0].Name != "laptop" {
		t.Fatalf("listed keys = %+v, want one named laptop", listed)
	}
	// The raw credential must never appear again after creation.
	if strings.Contains(rec.Body.String(), created.Key) && len(listed) > 0 {
		if body := adminRequest(t, h, http.MethodGet, "/api/v1/keys", "").Body.String(); strings.Contains(body, created.Key) {
			t.Error("list leaked the raw key")
		}
	}

	if got := adminRequest(t, h, http.MethodDelete, "/api/v1/keys/"+created.ID, "").Code; got != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", got)
	}
	if got := adminRequest(t, h, http.MethodDelete, "/api/v1/keys/"+created.ID, "").Code; got != http.StatusNotFound {
		t.Errorf("second delete status = %d, want 404", got)
	}
}

func TestAdminKeyRequiresName(t *testing.T) {
	h, _, _ := newAdminServer(t)

	if got := adminRequest(t, h, http.MethodPost, "/api/v1/keys", `{}`).Code; got != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 without a name", got)
	}
	if got := adminRequest(t, h, http.MethodPost, "/api/v1/keys", `{"name":`).Code; got != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 on malformed JSON", got)
	}
}

func TestAdminProviderMutationRepublishesRoutes(t *testing.T) {
	h, _, engine := newAdminServer(t)

	rec := adminRequest(t, h, http.MethodPut, "/api/v1/providers/openai",
		`{"kind":"openai","base_url":"https://api.openai.com/v1","api_key":"sk-test","timeout_ms":30000,"enabled":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put provider status = %d (body=%q)", rec.Code, rec.Body.String())
	}

	rec = adminRequest(t, h, http.MethodPut, "/api/v1/models/gpt-5",
		`{"provider":"openai","model":"gpt-5-upstream","fallback":["fast"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put model status = %d (body=%q)", rec.Code, rec.Body.String())
	}

	// The engine must see the alias without any restart or config reload.
	route, err := engine.Resolve("gpt-5")
	if err != nil {
		t.Fatalf("Resolve after admin write: %v", err)
	}
	if route.Provider != "openai" || route.Model != "gpt-5-upstream" {
		t.Errorf("route = %+v, want openai/gpt-5-upstream", route)
	}
	if chain := engine.Chain("gpt-5"); len(chain) != 2 || chain[1] != "fast" {
		t.Errorf("chain = %v, want [gpt-5 fast]", chain)
	}

	if got := adminRequest(t, h, http.MethodDelete, "/api/v1/models/gpt-5", "").Code; got != http.StatusNoContent {
		t.Fatalf("delete model status = %d, want 204", got)
	}
	if _, err := engine.Resolve("gpt-5"); err == nil {
		t.Error("deleted alias still resolves")
	}
}

// Capabilities are inferred from the upstream model name when the request
// omits them, so importing or adding a well-known model needs no hand-tagging.
func TestAdminAliasInfersCapabilities(t *testing.T) {
	h, _, engine := newAdminServer(t)

	adminRequest(t, h, http.MethodPut, "/api/v1/providers/openai",
		`{"kind":"openai","base_url":"https://api.openai.com/v1","api_key":"sk-test","enabled":true}`)

	rec := adminRequest(t, h, http.MethodPut, "/api/v1/models/gpt-4o",
		`{"provider":"openai","model":"gpt-4o"}`)
	got := decodeJSON[modelResponse](t, rec)
	if !slices.Contains(got.Capabilities, provider.CapVision) {
		t.Errorf("capabilities = %v, want vision inferred for gpt-4o", got.Capabilities)
	}
	if !slices.Equal(engine.Capabilities("gpt-4o"), got.Capabilities) {
		t.Errorf("engine capabilities = %v, want %v", engine.Capabilities("gpt-4o"), got.Capabilities)
	}

	// An explicit list overrides the guess, and unknown entries are dropped.
	rec = adminRequest(t, h, http.MethodPut, "/api/v1/models/gpt-4o",
		`{"provider":"openai","model":"gpt-4o","capabilities":["chat","telepathy"]}`)
	got = decodeJSON[modelResponse](t, rec)
	if !slices.Equal(got.Capabilities, []string{provider.CapChat}) {
		t.Errorf("capabilities = %v, want [chat]", got.Capabilities)
	}

	// An explicit empty list states "unclassified" rather than accepting the
	// inferred set.
	rec = adminRequest(t, h, http.MethodPut, "/api/v1/models/gpt-4o",
		`{"provider":"openai","model":"gpt-4o","capabilities":[]}`)
	if got = decodeJSON[modelResponse](t, rec); len(got.Capabilities) != 0 {
		t.Errorf("capabilities = %v, want empty", got.Capabilities)
	}
}

func TestAdminAliasWithSlashRoundTrips(t *testing.T) {
	h, _, engine := newAdminServer(t)

	adminRequest(t, h, http.MethodPut, "/api/v1/providers/weizerouter",
		`{"kind":"openai","base_url":"https://example.test/v1","api_key":"sk-test","enabled":true}`)

	// Clients percent-encode the slash; the handler must decode it so the
	// stored alias matches what /v1 callers send as "model".
	rec := adminRequest(t, h, http.MethodPut, "/api/v1/models/wz%2Fgemini-3.8-flash",
		`{"provider":"weizerouter","model":"wz/gemini-3.8-flash"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put alias status = %d (body=%q)", rec.Code, rec.Body.String())
	}

	route, err := engine.Resolve("wz/gemini-3.8-flash")
	if err != nil {
		t.Fatalf("Resolve encoded alias: %v", err)
	}
	if route.Provider != "weizerouter" || route.Model != "wz/gemini-3.8-flash" {
		t.Errorf("route = %+v, want weizerouter/wz/gemini-3.8-flash", route)
	}

	if got := adminRequest(t, h, http.MethodDelete, "/api/v1/models/wz%2Fgemini-3.8-flash", "").Code; got != http.StatusNoContent {
		t.Fatalf("delete encoded alias status = %d, want 204", got)
	}
	if _, err := engine.Resolve("wz/gemini-3.8-flash"); err == nil {
		t.Error("deleted alias still resolves")
	}
}

func TestAdminProviderSecretNotExposed(t *testing.T) {
	h, store, _ := newAdminServer(t)

	adminRequest(t, h, http.MethodPut, "/api/v1/providers/openai",
		`{"base_url":"https://api.openai.com/v1","api_key":"sk-secret","enabled":true}`)

	body := adminRequest(t, h, http.MethodGet, "/api/v1/providers", "").Body.String()
	if strings.Contains(body, "sk-secret") {
		t.Fatalf("provider list leaked the api key: %s", body)
	}

	listed := decodeJSON[[]providerResponse](t, adminRequest(t, h, http.MethodGet, "/api/v1/providers", ""))
	if len(listed) != 1 || !listed[0].HasAPIKey {
		t.Fatalf("providers = %+v, want one with has_api_key", listed)
	}

	// Updating without an api_key keeps the stored secret rather than wiping it.
	adminRequest(t, h, http.MethodPut, "/api/v1/providers/openai",
		`{"base_url":"https://proxy.example.com/v1","enabled":true}`)

	stored, err := store.Providers().Get(t.Context(), "openai")
	if err != nil {
		t.Fatalf("Get provider: %v", err)
	}
	if stored.APIKey != "sk-secret" {
		t.Errorf("api key = %q, want the preserved secret", stored.APIKey)
	}
	if stored.BaseURL != "https://proxy.example.com/v1" {
		t.Errorf("base url = %q, want the updated value", stored.BaseURL)
	}
}

func TestAdminProviderGroups(t *testing.T) {
	h, store, _ := newAdminServer(t)

	// A catalogue entry supplies the endpoint and dialect; only the key is sent.
	rec := adminRequest(t, h, http.MethodPut, "/api/v1/providers/commandcode",
		`{"group":"api_key","catalog_id":"commandcode","api_key":"cc-key","enabled":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put catalogue provider status = %d (body=%q)", rec.Code, rec.Body.String())
	}
	stored, err := store.Providers().Get(t.Context(), "commandcode")
	if err != nil {
		t.Fatalf("Get provider: %v", err)
	}
	if stored.BaseURL != "https://api.commandcode.ai/provider/v1" || stored.Kind != provider.KindAnthropic {
		t.Errorf("stored = %+v, want the catalogue base url and anthropic kind", stored)
	}

	// Custom providers still need their own base URL.
	rec = adminRequest(t, h, http.MethodPut, "/api/v1/providers/local",
		`{"group":"custom","kind":"anthropic","enabled":true}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 without a base_url", rec.Code)
	}

	// Key- and OAuth-backed providers are useless without a credential.
	rec = adminRequest(t, h, http.MethodPut, "/api/v1/providers/claude",
		`{"group":"oauth","kind":"anthropic","base_url":"https://api.anthropic.com/v1","enabled":true}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 without a credential", rec.Code)
	}

	rec = adminRequest(t, h, http.MethodPut, "/api/v1/providers/ghost",
		`{"group":"nope","base_url":"https://example.test/v1"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an unknown group", rec.Code)
	}
}

func TestAdminProviderCatalogPresets(t *testing.T) {
	h, _, _ := newAdminServer(t)

	all := decodeJSON[[]provider.CatalogEntry](t,
		adminRequest(t, h, http.MethodGet, "/api/v1/provider-catalog", ""))
	if len(all) == 0 {
		t.Fatal("catalogue is empty")
	}

	keyed := decodeJSON[[]provider.CatalogEntry](t,
		adminRequest(t, h, http.MethodGet, "/api/v1/provider-catalog?group=api_key", ""))
	for _, e := range keyed {
		if e.Group != provider.GroupAPIKey {
			t.Errorf("entry %s has group %s, want api_key", e.ID, e.Group)
		}
	}
	if len(keyed) == 0 || len(keyed) == len(all) {
		t.Errorf("group filter returned %d of %d entries", len(keyed), len(all))
	}
}

func TestAdminModelRejectsUnknownProvider(t *testing.T) {
	h, _, _ := newAdminServer(t)

	rec := adminRequest(t, h, http.MethodPut, "/api/v1/models/gpt-5",
		`{"provider":"ghost","model":"m"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an unknown provider", rec.Code)
	}
}

func TestAdminModelRequiresProviderAndModel(t *testing.T) {
	h, _, _ := newAdminServer(t)

	if got := adminRequest(t, h, http.MethodPut, "/api/v1/models/x", `{"provider":"openai"}`).Code; got != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 without a model", got)
	}
}

func TestAdminSettingsRoundTrip(t *testing.T) {
	h, _, _ := newAdminServer(t)

	rec := adminRequest(t, h, http.MethodPut, "/api/v1/settings", `{"log_level":"debug"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put settings status = %d (body=%q)", rec.Code, rec.Body.String())
	}

	settings := decodeJSON[map[string]string](t, adminRequest(t, h, http.MethodGet, "/api/v1/settings", ""))
	if settings["log_level"] != "debug" {
		t.Errorf("settings = %v, want log_level=debug", settings)
	}
}

func TestAdminOverviewCountsResources(t *testing.T) {
	h, _, _ := newAdminServer(t)

	adminRequest(t, h, http.MethodPut, "/api/v1/providers/openai",
		`{"base_url":"https://api.openai.com/v1","enabled":true}`)
	adminRequest(t, h, http.MethodPut, "/api/v1/models/gpt-5",
		`{"provider":"openai","model":"m"}`)
	adminRequest(t, h, http.MethodPost, "/api/v1/keys", `{"name":"laptop"}`)

	overview := decodeJSON[overviewResponse](t, adminRequest(t, h, http.MethodGet, "/api/v1/overview", ""))
	if overview.Providers != 1 || overview.Models != 1 || overview.APIKeys != 1 {
		t.Errorf("overview = %+v, want 1 of each resource", overview)
	}
	// Every admin call above was instrumented, so requests must be non-zero.
	if overview.Requests == 0 {
		t.Error("overview reported zero requests")
	}
}

func TestAdminOverviewHoursWindowFiltersTotals(t *testing.T) {
	h, store, _ := newAdminServer(t)
	ctx := context.Background()

	now := time.Now().UTC()
	for _, at := range []time.Time{now.Add(-48 * time.Hour), now.Add(-30 * time.Minute)} {
		if err := store.Usage().Record(ctx, storage.UsageEvent{
			CreatedAt: at, Alias: "a", Provider: "openai", Model: "m", Status: "ok",
			PromptTokens: 10, CompletionTokens: 5, CostUSD: 0.01,
		}); err != nil {
			t.Fatalf("record usage: %v", err)
		}
	}

	lifetime := decodeJSON[overviewResponse](t, adminRequest(t, h, http.MethodGet, "/api/v1/overview", ""))
	if lifetime.TotalRequests != 2 {
		t.Errorf("lifetime total_requests = %d, want 2", lifetime.TotalRequests)
	}

	windowed := decodeJSON[overviewResponse](t, adminRequest(t, h, http.MethodGet, "/api/v1/overview?hours=1", ""))
	if windowed.TotalRequests != 1 {
		t.Errorf("hours=1 total_requests = %d, want 1", windowed.TotalRequests)
	}
	if windowed.PromptTokens != 10 || windowed.CompletionTokens != 5 {
		t.Errorf("hours=1 tokens = %d/%d, want 10/5", windowed.PromptTokens, windowed.CompletionTokens)
	}

	// A malformed or non-positive window falls back to lifetime totals.
	for _, raw := range []string{"nope", "0", "-3"} {
		got := decodeJSON[overviewResponse](t, adminRequest(t, h, http.MethodGet, "/api/v1/overview?hours="+raw, ""))
		if got.TotalRequests != 2 {
			t.Errorf("hours=%s total_requests = %d, want 2 (lifetime)", raw, got.TotalRequests)
		}
	}
}

// newUpstream serves an OpenAI-style /models catalogue.
func newUpstream(t *testing.T, ids ...string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		data := make([]map[string]string, 0, len(ids))
		for _, id := range ids {
			data = append(data, map[string]string{"id": id, "owned_by": "acme"})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestAdminProviderCatalogAndImport(t *testing.T) {
	h, _, engine := newAdminServer(t)
	base := newUpstream(t, "gpt-5", "o3")

	adminRequest(t, h, http.MethodPut, "/api/v1/providers/acme",
		`{"kind":"openai","base_url":"`+base+`","timeout_ms":5000,"enabled":true}`)

	catalog := decodeJSON[[]catalogModelResponse](t,
		adminRequest(t, h, http.MethodGet, "/api/v1/providers/acme/catalog", ""))
	if len(catalog) != 2 || catalog[0].ID != "gpt-5" || catalog[0].Imported {
		t.Fatalf("catalog = %+v, want two un-imported models", catalog)
	}

	rec := adminRequest(t, h, http.MethodPost, "/api/v1/providers/acme/import",
		`{"models":["gpt-5","o3"],"prefix":"acme/"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("import status = %d (body=%q)", rec.Code, rec.Body.String())
	}
	result := decodeJSON[importModelsResponse](t, rec)
	if len(result.Imported) != 2 || len(result.Skipped) != 0 {
		t.Fatalf("import result = %+v, want two imported aliases", result)
	}

	route, err := engine.Resolve("acme/gpt-5")
	if err != nil {
		t.Fatalf("Resolve imported alias: %v", err)
	}
	if route.Provider != "acme" || route.Model != "gpt-5" {
		t.Errorf("route = %+v, want acme/gpt-5", route)
	}

	// A second import is a no-op unless overwrite is requested.
	again := decodeJSON[importModelsResponse](t, adminRequest(t, h, http.MethodPost,
		"/api/v1/providers/acme/import", `{"models":["gpt-5"],"prefix":"acme/"}`))
	if len(again.Imported) != 0 || len(again.Skipped) != 1 {
		t.Errorf("re-import = %+v, want the existing alias skipped", again)
	}

	catalog = decodeJSON[[]catalogModelResponse](t,
		adminRequest(t, h, http.MethodGet, "/api/v1/providers/acme/catalog", ""))
	if !catalog[0].Imported || catalog[0].Alias != "acme/gpt-5" {
		t.Errorf("catalog[0] = %+v, want it marked as imported", catalog[0])
	}
}

// Two providers advertising the same upstream model must not collide: each
// provider's configured alias prefix namespaces its imports.
func TestAdminImportUsesProviderAliasPrefix(t *testing.T) {
	h, _, engine := newAdminServer(t)
	first := newUpstream(t, "gpt-5")
	second := newUpstream(t, "gpt-5")

	adminRequest(t, h, http.MethodPut, "/api/v1/providers/acme",
		`{"kind":"openai","base_url":"`+first+`","timeout_ms":5000,"enabled":true,"alias_prefix":"acme/"}`)
	adminRequest(t, h, http.MethodPut, "/api/v1/providers/globex",
		`{"kind":"openai","base_url":"`+second+`","timeout_ms":5000,"enabled":true,"alias_prefix":"globex/"}`)

	listed := decodeJSON[[]providerResponse](t,
		adminRequest(t, h, http.MethodGet, "/api/v1/providers", ""))
	if len(listed) != 2 || listed[0].AliasPrefix != "acme/" {
		t.Fatalf("providers = %+v, want the alias prefix round-tripped", listed)
	}

	// Omitting "prefix" falls back to each provider's configured prefix.
	for _, name := range []string{"acme", "globex"} {
		result := decodeJSON[importModelsResponse](t, adminRequest(t, h, http.MethodPost,
			"/api/v1/providers/"+name+"/import", `{"models":["gpt-5"]}`))
		if len(result.Imported) != 1 || result.Imported[0].Alias != name+"/gpt-5" {
			t.Fatalf("%s import = %+v, want alias %s/gpt-5", name, result, name)
		}
	}

	for _, name := range []string{"acme", "globex"} {
		route, err := engine.Resolve(name + "/gpt-5")
		if err != nil {
			t.Fatalf("Resolve %s/gpt-5: %v", name, err)
		}
		if route.Provider != name {
			t.Errorf("%s/gpt-5 routes to %q, want %q", name, route.Provider, name)
		}
	}

	// An alias owned by another provider is skipped even with overwrite set.
	stolen := decodeJSON[importModelsResponse](t, adminRequest(t, h, http.MethodPost,
		"/api/v1/providers/globex/import",
		`{"models":["gpt-5"],"prefix":"acme/","overwrite":true}`))
	if len(stolen.Imported) != 0 || len(stolen.Skipped) != 1 {
		t.Fatalf("cross-provider import = %+v, want the alias skipped", stolen)
	}
	if route, _ := engine.Resolve("acme/gpt-5"); route.Provider != "acme" {
		t.Errorf("acme/gpt-5 rerouted to %q", route.Provider)
	}
}

func TestAdminCatalogUnknownProvider(t *testing.T) {
	h, _, _ := newAdminServer(t)

	if got := adminRequest(t, h, http.MethodGet, "/api/v1/providers/nope/catalog", "").Code; got != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for an unknown provider", got)
	}
}

func TestAdminCatalogUpstreamFailure(t *testing.T) {
	h, _, _ := newAdminServer(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
	}))
	t.Cleanup(srv.Close)

	adminRequest(t, h, http.MethodPut, "/api/v1/providers/acme",
		`{"base_url":"`+srv.URL+`","timeout_ms":5000,"enabled":true}`)

	rec := adminRequest(t, h, http.MethodGet, "/api/v1/providers/acme/catalog", "")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (body=%q)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "bad key") {
		t.Errorf("body = %q, want the upstream message", rec.Body.String())
	}
}

func TestAdminTestAlias(t *testing.T) {
	h, _, _ := newAdminServer(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[]}`))
	}))
	t.Cleanup(srv.Close)

	adminRequest(t, h, http.MethodPut, "/api/v1/providers/acme",
		`{"base_url":"`+srv.URL+`","timeout_ms":5000,"enabled":true}`)
	adminRequest(t, h, http.MethodPut, "/api/v1/models/acme%2Fgpt-5",
		`{"provider":"acme","model":"gpt-5"}`)

	rec := adminRequest(t, h, http.MethodPost, "/api/v1/models/acme%2Fgpt-5/test", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%q)", rec.Code, rec.Body.String())
	}
	result := decodeJSON[testAliasResponse](t, rec)
	if !result.OK || result.Error != "" {
		t.Errorf("result = %+v, want a successful probe", result)
	}
	if result.Provider != "acme" || result.Model != "gpt-5" {
		t.Errorf("result = %+v, want the alias's own route", result)
	}
}

// A failing upstream is a reported result, not a failed request: the dashboard
// renders the error next to the alias.
func TestAdminTestAliasUpstreamFailure(t *testing.T) {
	h, _, _ := newAdminServer(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
	}))
	t.Cleanup(srv.Close)

	adminRequest(t, h, http.MethodPut, "/api/v1/providers/acme",
		`{"base_url":"`+srv.URL+`","timeout_ms":5000,"enabled":true}`)
	adminRequest(t, h, http.MethodPut, "/api/v1/models/gpt-5",
		`{"provider":"acme","model":"gpt-5"}`)

	rec := adminRequest(t, h, http.MethodPost, "/api/v1/models/gpt-5/test", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%q)", rec.Code, rec.Body.String())
	}
	result := decodeJSON[testAliasResponse](t, rec)
	if result.OK || !strings.Contains(result.Error, "bad key") {
		t.Errorf("result = %+v, want a failed probe carrying the upstream message", result)
	}
}

func TestAdminTestAliasUnknown(t *testing.T) {
	h, _, _ := newAdminServer(t)

	if got := adminRequest(t, h, http.MethodPost, "/api/v1/models/ghost/test", `{}`).Code; got != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for an unknown alias", got)
	}
}

func TestAdminKeyQuotaLifecycle(t *testing.T) {
	h, store, _ := newAdminServer(t)
	ctx := context.Background()

	created := decodeJSON[createKeyResponse](t,
		adminRequest(t, h, http.MethodPost, "/api/v1/keys",
			`{"name":"agent","rate_limit_per_min":30,"monthly_budget_usd":12.5}`))
	if created.RateLimitPerMin != 30 || created.MonthlyBudgetUSD != 12.5 {
		t.Fatalf("created = %+v, want the quotas from the request", created)
	}

	// Usage recorded for the key shows up on the list, split into lifetime
	// cost and current-month spend.
	if err := store.Usage().Record(ctx, storage.UsageEvent{
		CreatedAt: time.Now().UTC(), KeyID: created.ID, Alias: "a",
		Provider: "p", Model: "m", Status: "ok",
		PromptTokens: 100, CompletionTokens: 20, CostUSD: 2,
	}); err != nil {
		t.Fatalf("record usage: %v", err)
	}

	keys := decodeJSON[[]apiKeyResponse](t, adminRequest(t, h, http.MethodGet, "/api/v1/keys", ""))
	if len(keys) != 1 {
		t.Fatalf("listed %d keys, want 1", len(keys))
	}
	if keys[0].Requests != 1 || keys[0].CostUSD != 2 || keys[0].MonthSpendUSD != 2 {
		t.Errorf("key usage = %+v, want 1 request costing 2 this month", keys[0])
	}
	if keys[0].TokensIn != 100 || keys[0].TokensOut != 20 {
		t.Errorf("key tokens = %d/%d, want 100/20", keys[0].TokensIn, keys[0].TokensOut)
	}

	// Updating quotas persists.
	path := "/api/v1/keys/" + created.ID + "/quota"
	if got := adminRequest(t, h, http.MethodPut, path, `{"rate_limit_per_min":5,"monthly_budget_usd":0}`).Code; got != http.StatusNoContent {
		t.Fatalf("put quota status = %d, want 204", got)
	}
	keys = decodeJSON[[]apiKeyResponse](t, adminRequest(t, h, http.MethodGet, "/api/v1/keys", ""))
	if keys[0].RateLimitPerMin != 5 || keys[0].MonthlyBudgetUSD != 0 {
		t.Errorf("quotas = %d/%v, want 5/0", keys[0].RateLimitPerMin, keys[0].MonthlyBudgetUSD)
	}

	// Negative values are rejected, and an unknown key is a 404.
	if got := adminRequest(t, h, http.MethodPut, path, `{"rate_limit_per_min":-1}`).Code; got != http.StatusBadRequest {
		t.Errorf("negative rate limit status = %d, want 400", got)
	}
	if got := adminRequest(t, h, http.MethodPut, "/api/v1/keys/nope/quota", `{"rate_limit_per_min":1}`).Code; got != http.StatusNotFound {
		t.Errorf("unknown key status = %d, want 404", got)
	}
}

func TestAdminUsageByKeyNamesUnattributedTraffic(t *testing.T) {
	h, store, _ := newAdminServer(t)
	ctx := context.Background()
	now := time.Now().UTC()

	created := decodeJSON[createKeyResponse](t,
		adminRequest(t, h, http.MethodPost, "/api/v1/keys", `{"name":"agent"}`))

	for _, e := range []storage.UsageEvent{
		{CreatedAt: now, KeyID: created.ID, Alias: "a", Provider: "p", Model: "m", Status: "ok", CostUSD: 3},
		{CreatedAt: now, Alias: "a", Provider: "p", Model: "m", Status: "ok", CostUSD: 1},
	} {
		if err := store.Usage().Record(ctx, e); err != nil {
			t.Fatalf("record usage: %v", err)
		}
	}

	rows := decodeJSON[[]keyUsageResponse](t,
		adminRequest(t, h, http.MethodGet, "/api/v1/usage/keys?hours=24", ""))
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	// Rows are ranked by spend, so the named key leads.
	if rows[0].Name != "agent" || rows[0].CostUSD != 3 {
		t.Errorf("row 0 = %+v, want the agent key at 3", rows[0])
	}
	if rows[1].Name != "unattributed" || rows[1].KeyID != "" {
		t.Errorf("row 1 = %+v, want unattributed traffic", rows[1])
	}
}

func TestListProvidersReportsCircuitState(t *testing.T) {
	h, _, _ := newAdminServer(t)

	body := `{"group":"custom","kind":"openai","base_url":"https://api.example.com","api_key":"sk-test","timeout_ms":60000,"enabled":true,"alias_prefix":"acme/"}`
	if rec := adminRequest(t, h, http.MethodPut, "/api/v1/providers/acme", body); rec.Code != http.StatusOK {
		t.Fatalf("put provider: status %d (%s)", rec.Code, rec.Body.String())
	}

	// A provider the engine has never dispatched to is healthy, not unknown.
	rec := adminRequest(t, h, http.MethodGet, "/api/v1/providers", "")
	providers := decodeJSON[[]providerResponse](t, rec)
	if len(providers) != 1 {
		t.Fatalf("got %d providers, want 1", len(providers))
	}
	if providers[0].Circuit != "closed" {
		t.Errorf("circuit = %q, want %q", providers[0].Circuit, "closed")
	}
	if providers[0].CircuitRetryAt != "" {
		t.Errorf("CircuitRetryAt = %q, want empty for a closed circuit", providers[0].CircuitRetryAt)
	}
}

func TestPutProviderBreakerOverride(t *testing.T) {
	h, store, _ := newAdminServer(t)

	body := `{"group":"custom","kind":"openai","base_url":"https://api.example.com","api_key":"sk-test","timeout_ms":60000,"enabled":true,"breaker_threshold":2,"breaker_cooldown_ms":90000}`
	rec := adminRequest(t, h, http.MethodPut, "/api/v1/providers/acme", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("put provider: status %d (%s)", rec.Code, rec.Body.String())
	}

	resp := decodeJSON[providerResponse](t, rec)
	if resp.BreakerThreshold == nil || *resp.BreakerThreshold != 2 {
		t.Errorf("breaker_threshold = %v, want 2", resp.BreakerThreshold)
	}
	if resp.BreakerCooldownMS == nil || *resp.BreakerCooldownMS != 90000 {
		t.Errorf("breaker_cooldown_ms = %v, want 90000", resp.BreakerCooldownMS)
	}

	stored, err := store.Providers().Get(t.Context(), "acme")
	if err != nil {
		t.Fatalf("get stored provider: %v", err)
	}
	if stored.BreakerThreshold == nil || *stored.BreakerThreshold != 2 {
		t.Errorf("stored threshold = %v, want 2", stored.BreakerThreshold)
	}
}

func TestPutProviderRejectsNegativeBreakerValues(t *testing.T) {
	h, _, _ := newAdminServer(t)

	for _, body := range []string{
		`{"base_url":"https://api.example.com","enabled":true,"breaker_threshold":-1}`,
		`{"base_url":"https://api.example.com","enabled":true,"breaker_cooldown_ms":-1}`,
	} {
		rec := adminRequest(t, h, http.MethodPut, "/api/v1/providers/acme", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400 for body %s", rec.Code, body)
		}
	}
}

func TestProviderWithoutOverrideReportsNull(t *testing.T) {
	h, _, _ := newAdminServer(t)

	adminRequest(t, h, http.MethodPut, "/api/v1/providers/plain",
		`{"base_url":"https://api.example.com","enabled":true}`)

	providers := decodeJSON[[]providerResponse](t,
		adminRequest(t, h, http.MethodGet, "/api/v1/providers", ""))
	if len(providers) != 1 {
		t.Fatalf("got %d providers, want 1", len(providers))
	}
	// Null tells the dashboard to render "inherited", not a concrete number.
	if providers[0].BreakerThreshold != nil {
		t.Errorf("breaker_threshold = %v, want null", *providers[0].BreakerThreshold)
	}
	if providers[0].BreakerCooldownMS != nil {
		t.Errorf("breaker_cooldown_ms = %v, want null", *providers[0].BreakerCooldownMS)
	}
}
