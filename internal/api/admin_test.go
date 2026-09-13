package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zealish/zealish-router/internal/auth"
	"github.com/zealish/zealish-router/internal/config"
	"github.com/zealish/zealish-router/internal/metrics"
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
	loader := router.NewLoader(store.Providers(), store.Models(), engine)

	deps := Dependencies{
		Config:    cfg,
		Engine:    engine,
		Loader:    loader,
		Store:     store,
		Auth:      auth.NewService(false, nil, nil, logger),
		AdminAuth: auth.NewAdminService(true, adminToken),
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
