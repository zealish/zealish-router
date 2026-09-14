package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/zealish/zealish-router/internal/auth"
	"github.com/zealish/zealish-router/internal/metrics"
	"github.com/zealish/zealish-router/internal/provider"
	"github.com/zealish/zealish-router/internal/router"
	"github.com/zealish/zealish-router/internal/storage"
	"github.com/zealish/zealish-router/pkg/openai"
)

// adminHandler serves the dashboard's internal REST API. It owns the routing
// configuration: every mutation writes to storage and then republishes the
// engine's routing table, so the database stays the single source of truth.
type adminHandler struct {
	store   storage.Store
	loader  *router.Loader
	engine  *router.Engine
	metrics *metrics.Metrics
	quota   *auth.Quota
	logger  *slog.Logger
}

func newAdminHandler(deps Dependencies) *adminHandler {
	return &adminHandler{
		store:   deps.Store,
		loader:  deps.Loader,
		engine:  deps.Engine,
		metrics: deps.Metrics,
		quota:   deps.Quota,
		logger:  deps.Logger,
	}
}

func (h *adminHandler) routes(r chi.Router) {
	r.Get("/overview", h.overview)
	r.Get("/usage", h.usageSummary)
	r.Get("/usage/recent", h.usageRecent)
	r.Get("/usage/models", h.usageByModel)
	r.Get("/usage/leaderboard", h.usageLeaderboard)
	r.Get("/usage/keys", h.usageByKey)

	r.Get("/requests", h.listRequests)
	r.Get("/requests/{request_id}", h.getRequest)

	r.Get("/keys", h.listKeys)
	r.Post("/keys", h.createKey)
	r.Delete("/keys/{id}", h.deleteKey)
	r.Put("/keys/{id}/quota", h.putKeyQuota)

	r.Get("/provider-catalog", h.providerCatalogPresets)
	r.Get("/providers", h.listProviders)
	r.Put("/providers/{name}", h.putProvider)
	r.Delete("/providers/{name}", h.deleteProvider)
	r.Get("/providers/{name}/catalog", h.providerCatalog)
	r.Post("/providers/{name}/import", h.importModels)
	r.Get("/providers/{name}/metrics", h.providerMetrics)

	r.Get("/models", h.listAliases)
	r.Put("/models/{alias}", h.putAlias)
	r.Delete("/models/{alias}", h.deleteAlias)
	r.Post("/models/{alias}/test", h.testAlias)

	r.Get("/combos", h.listCombos)
	r.Put("/combos/{name}", h.putCombo)
	r.Delete("/combos/{name}", h.deleteCombo)

	r.Get("/proxies", h.listProxies)
	r.Put("/proxies/{name}", h.putProxy)
	r.Post("/proxies/import", h.importProxies)
	r.Delete("/proxies/{name}", h.deleteProxy)

	r.Get("/settings", h.listSettings)
	r.Put("/settings", h.putSettings)
}

// --- overview ---

type overviewResponse struct {
	Requests      float64 `json:"requests"`
	Errors        float64 `json:"errors"`
	ErrorRate     float64 `json:"error_rate"`
	ActiveStreams float64 `json:"active_streams"`
	Providers     int     `json:"providers"`
	Models        int     `json:"models"`
	Combos        int     `json:"combos"`
	APIKeys       int     `json:"api_keys"`

	// Usage totals cover the ?hours= window, or the whole durable log
	// when the parameter is absent, unlike the counters above which
	// reset with the process.
	TotalRequests    int     `json:"total_requests"`
	PromptTokens     int     `json:"prompt_tokens"`
	CachedTokens     int     `json:"cached_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	CostUSD          float64 `json:"cost_usd"`
}

func (h *adminHandler) overview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	providers, err := h.store.Providers().List(ctx)
	if err != nil {
		h.fail(w, err)
		return
	}
	aliases, err := h.store.Models().List(ctx)
	if err != nil {
		h.fail(w, err)
		return
	}
	combos, err := h.store.Combos().List(ctx)
	if err != nil {
		h.fail(w, err)
		return
	}
	keys, err := h.store.APIKeys().List(ctx)
	if err != nil {
		h.fail(w, err)
		return
	}

	snapshot := h.metrics.Snapshot()
	resp := overviewResponse{
		Requests:      snapshot.Requests,
		Errors:        snapshot.ProviderErrors,
		ActiveStreams: snapshot.StreamConnections,
		Providers:     len(providers),
		Models:        len(aliases),
		Combos:        len(combos),
		APIKeys:       len(keys),
	}
	if snapshot.Requests > 0 {
		resp.ErrorRate = snapshot.RequestErrors / snapshot.Requests
	}

	// No ?hours= means lifetime: since the epoch, because the log is never pruned.
	since := time.Time{}
	if raw := r.URL.Query().Get("hours"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			if n > 24*90 {
				n = 24 * 90
			}
			since = time.Now().Add(-time.Duration(n) * time.Hour)
		}
	}
	if totals, err := h.store.Usage().Totals(ctx, since); err == nil {
		resp.TotalRequests = totals.Requests
		resp.PromptTokens = totals.PromptTokens
		resp.CachedTokens = totals.CachedTokens
		resp.CompletionTokens = totals.CompletionTokens
		resp.CostUSD = totals.CostUSD
	} else {
		h.logger.Warn("usage totals unavailable", slog.Any("error", err))
	}
	writeJSON(w, http.StatusOK, resp)
}

// --- api keys ---

type apiKeyResponse struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Enabled    bool       `json:"enabled"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`

	// Quotas. Zero means unlimited on either field.
	RateLimitPerMin  int     `json:"rate_limit_per_min"`
	MonthlyBudgetUSD float64 `json:"monthly_budget_usd"`

	// Spend so far in the current calendar month, against the budget above.
	MonthSpendUSD float64 `json:"month_spend_usd"`

	// Lifetime usage attributed to this key.
	Requests  int     `json:"requests"`
	TokensIn  int     `json:"tokens_in"`
	TokensOut int     `json:"tokens_out"`
	CostUSD   float64 `json:"cost_usd"`
}

type createKeyRequest struct {
	Name             string  `json:"name"`
	RateLimitPerMin  int     `json:"rate_limit_per_min"`
	MonthlyBudgetUSD float64 `json:"monthly_budget_usd"`
}

// quotaRequest updates the limits of an existing key.
type quotaRequest struct {
	RateLimitPerMin  int     `json:"rate_limit_per_min"`
	MonthlyBudgetUSD float64 `json:"monthly_budget_usd"`
}

type createKeyResponse struct {
	apiKeyResponse
	// Key is the raw credential. It is returned exactly once, at creation.
	Key string `json:"key"`
}

func (h *adminHandler) listKeys(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	keys, err := h.store.APIKeys().List(ctx)
	if err != nil {
		h.fail(w, err)
		return
	}

	// Lifetime usage and current-month spend are joined in by key id. A usage
	// read failure degrades to zeroed counters rather than failing the list.
	lifetime := map[string]storage.KeyUsage{}
	if rows, err := h.store.Usage().ByKey(ctx, time.Time{}); err == nil {
		for _, row := range rows {
			lifetime[row.KeyID] = row
		}
	} else {
		h.logger.Warn("key usage unavailable", slog.Any("error", err))
	}
	month := map[string]storage.KeyUsage{}
	if rows, err := h.store.Usage().ByKey(ctx, monthStart(time.Now())); err == nil {
		for _, row := range rows {
			month[row.KeyID] = row
		}
	}

	out := make([]apiKeyResponse, 0, len(keys))
	for _, k := range keys {
		resp := toAPIKeyResponse(k)
		if u, ok := lifetime[k.ID]; ok {
			resp.Requests = u.Requests
			resp.TokensIn = u.PromptTokens
			resp.TokensOut = u.CompletionTokens
			resp.CostUSD = u.CostUSD
		}
		resp.MonthSpendUSD = month[k.ID].CostUSD
		out = append(out, resp)
	}
	writeJSON(w, http.StatusOK, out)
}

// monthStart is the first instant of the calendar month containing t, in UTC.
// Budgets reset on that boundary.
func monthStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

func (h *adminHandler) createKey(w http.ResponseWriter, r *http.Request) {
	var req createKeyRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "Field 'name' is required.")
		return
	}

	generated, err := auth.GenerateKey(req.Name)
	if err != nil {
		h.fail(w, err)
		return
	}
	generated.Record.RateLimitPerMin = req.RateLimitPerMin
	generated.Record.MonthlyBudgetUSD = req.MonthlyBudgetUSD
	if err := h.store.APIKeys().Create(r.Context(), generated.Record); err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, createKeyResponse{
		apiKeyResponse: toAPIKeyResponse(generated.Record),
		Key:            generated.Raw,
	})
}

func (h *adminHandler) deleteKey(w http.ResponseWriter, r *http.Request) {
	id := urlParam(r, "id")
	if err := h.store.APIKeys().Delete(r.Context(), id); err != nil {
		h.fail(w, err)
		return
	}
	h.quota.Forget(id)
	w.WriteHeader(http.StatusNoContent)
}

func (h *adminHandler) putKeyQuota(w http.ResponseWriter, r *http.Request) {
	var req quotaRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.RateLimitPerMin < 0 || req.MonthlyBudgetUSD < 0 {
		writeError(w, http.StatusBadRequest, "invalid_request_error",
			"Quota values must not be negative.")
		return
	}
	id := urlParam(r, "id")
	if err := h.store.APIKeys().SetQuota(r.Context(), id,
		req.RateLimitPerMin, req.MonthlyBudgetUSD); err != nil {
		h.fail(w, err)
		return
	}
	// The new limits must apply to the next request, not after the cached
	// window and spend total expire.
	h.quota.Forget(id)
	w.WriteHeader(http.StatusNoContent)
}

func toAPIKeyResponse(k storage.APIKey) apiKeyResponse {
	resp := apiKeyResponse{
		ID:               k.ID,
		Name:             k.Name,
		Enabled:          k.Enabled,
		CreatedAt:        k.CreatedAt,
		RateLimitPerMin:  k.RateLimitPerMin,
		MonthlyBudgetUSD: k.MonthlyBudgetUSD,
	}
	if !k.LastUsedAt.IsZero() {
		used := k.LastUsedAt
		resp.LastUsedAt = &used
	}
	return resp
}

// --- providers ---

type providerResponse struct {
	Name         string `json:"name"`
	Group        string `json:"group"`
	CatalogID    string `json:"catalog_id,omitempty"`
	Kind         string `json:"kind"`
	BaseURL      string `json:"base_url"`
	HasAPIKey    bool   `json:"has_api_key"`
	TimeoutMS    int64  `json:"timeout_ms"`
	Enabled      bool   `json:"enabled"`
	AliasPrefix  string `json:"alias_prefix"`
	UseProxyPool bool   `json:"use_proxy_pool"`

	// Circuit reports the breaker phase: "closed", "open" or "half_open".
	// A provider the engine has never called reports "closed".
	Circuit string `json:"circuit"`
	// CircuitRetryAt is when the next probe is admitted, set only while open.
	CircuitRetryAt string `json:"circuit_retry_at,omitempty"`
	// BreakerThreshold and BreakerCooldownMS are the per-provider overrides.
	// Null means the provider inherits the policy from config.yaml.
	BreakerThreshold  *int   `json:"breaker_threshold"`
	BreakerCooldownMS *int64 `json:"breaker_cooldown_ms"`
}

type providerRequest struct {
	Group        string `json:"group"`
	CatalogID    string `json:"catalog_id"`
	Kind         string `json:"kind"`
	BaseURL      string `json:"base_url"`
	APIKey       string `json:"api_key"`
	TimeoutMS    int64  `json:"timeout_ms"`
	Enabled      bool   `json:"enabled"`
	AliasPrefix  string `json:"alias_prefix"`
	UseProxyPool bool   `json:"use_proxy_pool"`
	// Null clears the override and returns the provider to the global policy.
	BreakerThreshold  *int   `json:"breaker_threshold"`
	BreakerCooldownMS *int64 `json:"breaker_cooldown_ms"`
}

func toProviderResponse(p storage.Provider, health router.ProviderHealth) providerResponse {
	resp := providerResponse{
		Name:         p.Name,
		Group:        p.Group,
		CatalogID:    p.CatalogID,
		Kind:         p.Kind,
		BaseURL:      p.BaseURL,
		HasAPIKey:    p.APIKey != "",
		TimeoutMS:    p.Timeout.Milliseconds(),
		Enabled:      p.Enabled,
		AliasPrefix:  p.AliasPrefix,
		UseProxyPool: p.UseProxyPool,
		Circuit:      string(router.CircuitClosed),
	}
	if health.State != "" {
		resp.Circuit = string(health.State)
	}
	if !health.RetryAt.IsZero() {
		resp.CircuitRetryAt = health.RetryAt.UTC().Format(time.RFC3339)
	}
	resp.BreakerThreshold = p.BreakerThreshold
	if p.BreakerCooldown != nil {
		ms := p.BreakerCooldown.Milliseconds()
		resp.BreakerCooldownMS = &ms
	}
	return resp
}

func (h *adminHandler) listProviders(w http.ResponseWriter, r *http.Request) {
	providers, err := h.store.Providers().List(r.Context())
	if err != nil {
		h.fail(w, err)
		return
	}

	health := h.engine.Health()
	out := make([]providerResponse, 0, len(providers))
	for _, p := range providers {
		out = append(out, toProviderResponse(p, health[p.Name]))
	}
	writeJSON(w, http.StatusOK, out)
}

// providerCatalogPresets lists the known upstreams the dashboard offers when
// adding a provider, optionally narrowed to one group.
func (h *adminHandler) providerCatalogPresets(w http.ResponseWriter, r *http.Request) {
	group := r.URL.Query().Get("group")
	if group != "" && !provider.ValidGroup(group) {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "Unknown provider group '"+group+"'.")
		return
	}
	writeJSON(w, http.StatusOK, provider.Catalog(group))
}

func (h *adminHandler) putProvider(w http.ResponseWriter, r *http.Request) {
	var req providerRequest
	if !decodeBody(w, r, &req) {
		return
	}

	ctx := r.Context()
	name := urlParam(r, "name")

	group := strings.TrimSpace(req.Group)
	if group == "" {
		group = string(provider.GroupCustom)
	}
	if !provider.ValidGroup(group) {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "Unknown provider group '"+group+"'.")
		return
	}

	record := storage.Provider{
		ID:           name,
		Name:         name,
		Group:        group,
		CatalogID:    strings.TrimSpace(req.CatalogID),
		Kind:         req.Kind,
		BaseURL:      req.BaseURL,
		APIKey:       req.APIKey,
		Timeout:      time.Duration(req.TimeoutMS) * time.Millisecond,
		Enabled:      req.Enabled,
		AliasPrefix:  strings.TrimSpace(req.AliasPrefix),
		UseProxyPool: req.UseProxyPool,
	}

	if req.BreakerThreshold != nil {
		if *req.BreakerThreshold < 0 {
			writeError(w, http.StatusBadRequest, "invalid_request_error",
				"Field 'breaker_threshold' must be zero or greater; 0 disables the breaker.")
			return
		}
		record.BreakerThreshold = req.BreakerThreshold
	}
	if req.BreakerCooldownMS != nil {
		if *req.BreakerCooldownMS < 0 {
			writeError(w, http.StatusBadRequest, "invalid_request_error",
				"Field 'breaker_cooldown_ms' must be zero or greater.")
			return
		}
		cooldown := time.Duration(*req.BreakerCooldownMS) * time.Millisecond
		record.BreakerCooldown = &cooldown
	}

	// A catalogue entry supplies the endpoint and dialect, so a preset-backed
	// provider only needs a credential. Explicit fields still win.
	if preset, ok := findCatalogEntry(record.CatalogID); ok {
		if record.BaseURL == "" {
			record.BaseURL = preset.BaseURL
		}
		if record.Kind == "" {
			record.Kind = preset.Kind
		}
	}
	if record.BaseURL == "" {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "Field 'base_url' is required.")
		return
	}
	if record.Kind == "" {
		record.Kind = provider.KindOpenAI
	}
	record.Kind = provider.NormalizeKind(record.Kind)

	// An omitted api_key means "keep the current secret": the dashboard never
	// receives it back, so it cannot echo it on update.
	if record.APIKey == "" {
		if existing, err := h.store.Providers().Get(ctx, name); err == nil {
			record.APIKey = existing.APIKey
		}
	}
	if record.Group != string(provider.GroupCustom) && record.APIKey == "" {
		writeError(w, http.StatusBadRequest, "invalid_request_error",
			"Field 'api_key' is required for "+record.Group+" providers.")
		return
	}

	if err := h.store.Providers().Put(ctx, record); err != nil {
		h.fail(w, err)
		return
	}
	if err := h.loader.Load(ctx); err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toProviderResponse(record, h.engine.Health()[record.Name]))
}

// findCatalogEntry resolves a preset by id.
func findCatalogEntry(id string) (provider.CatalogEntry, bool) {
	if id == "" {
		return provider.CatalogEntry{}, false
	}
	for _, e := range provider.Catalog("") {
		if e.ID == id {
			return e, true
		}
	}
	return provider.CatalogEntry{}, false
}

func (h *adminHandler) deleteProvider(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := h.store.Providers().Delete(ctx, urlParam(r, "name")); err != nil {
		h.fail(w, err)
		return
	}
	if err := h.loader.Load(ctx); err != nil {
		h.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- provider health metrics ---

type aliasMetricsResponse struct {
	Alias string `json:"alias"`
	// P95MS is null until the window holds enough samples to estimate a tail.
	TTFBMS      int64             `json:"ttfb_ms"`
	P50MS       int64             `json:"p50_ms"`
	P95MS       *int64            `json:"p95_ms"`
	SuccessRate float64           `json:"success_rate"`
	Requests    int               `json:"requests"`
	Confidence  router.Confidence `json:"confidence"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

// summaryMetricsResponse aggregates every alias of the provider. It is pooled
// over all requests, not averaged across aliases.
type summaryMetricsResponse struct {
	TTFBMS      int64             `json:"ttfb_ms"`
	P50MS       int64             `json:"p50_ms"`
	P95MS       *int64            `json:"p95_ms"`
	SuccessRate float64           `json:"success_rate"`
	Requests    int               `json:"requests"`
	Confidence  router.Confidence `json:"confidence"`
}

type providerMetricsResponse struct {
	Aliases []aliasMetricsResponse  `json:"aliases"`
	Summary *summaryMetricsResponse `json:"summary"`
}

// providerMetrics reports the rolling request window of every alias routed to
// a provider. Aliases that have not served a request since the process started
// are omitted: the window is in-memory, and a zeroed row would read as a
// perfect route rather than an unmeasured one.
func (h *adminHandler) providerMetrics(w http.ResponseWriter, r *http.Request) {
	name := urlParam(r, "name")

	aliases, err := h.store.Models().List(r.Context())
	if err != nil {
		h.fail(w, err)
		return
	}

	out := make([]aliasMetricsResponse, 0, len(aliases))
	names := make([]string, 0, len(aliases))
	for _, m := range aliases {
		if m.Provider != name {
			continue
		}
		names = append(names, m.Alias)

		stats, ok := h.engine.AliasStats(m.Alias)
		if !ok {
			continue
		}
		out = append(out, aliasMetricsResponse{
			Alias:       stats.Alias,
			TTFBMS:      stats.TTFBMs,
			P50MS:       stats.P50Ms,
			P95MS:       stats.P95Ms,
			SuccessRate: roundRate(stats.SuccessRate),
			Requests:    stats.Requests,
			Confidence:  stats.Confidence,
			UpdatedAt:   stats.UpdatedAt.UTC(),
		})
	}

	resp := providerMetricsResponse{Aliases: out}
	if summary, ok := h.engine.Stats().Summary(names); ok {
		resp.Summary = &summaryMetricsResponse{
			TTFBMS:      summary.TTFBMs,
			P50MS:       summary.P50Ms,
			P95MS:       summary.P95Ms,
			SuccessRate: roundRate(summary.SuccessRate),
			Requests:    summary.Requests,
			Confidence:  summary.Confidence,
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// roundRate trims a success rate to one decimal, which is all the dashboard
// renders.
func roundRate(rate float64) float64 {
	return math.Round(rate*10) / 10
}

// --- upstream model catalogue ---

type catalogModelResponse struct {
	ID       string `json:"id"`
	OwnedBy  string `json:"owned_by,omitempty"`
	Imported bool   `json:"imported"`
	Alias    string `json:"alias,omitempty"`
}

type importModelsRequest struct {
	Models []string `json:"models"`
	// Prefix overrides the provider's configured alias prefix. Omitted means
	// "use the provider default"; an explicit "" imports unprefixed.
	Prefix    *string `json:"prefix"`
	Overwrite bool    `json:"overwrite"`
}

type importModelsResponse struct {
	Imported []modelResponse `json:"imported"`
	Skipped  []string        `json:"skipped"`
}

// providerCatalog fetches {base_url}/models from the upstream and marks which
// entries already have an alias routed to this provider.
func (h *adminHandler) providerCatalog(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := urlParam(r, "name")

	record, err := h.store.Providers().Get(ctx, name)
	if err != nil {
		h.fail(w, err)
		return
	}

	lister, ok := h.providerClient(ctx, record).(provider.ModelLister)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_request_error",
			"Provider '"+name+"' cannot list models.")
		return
	}
	models, err := lister.ListModels(ctx)
	if err != nil {
		h.upstreamFail(w, name, err)
		return
	}

	aliases, err := h.store.Models().List(ctx)
	if err != nil {
		h.fail(w, err)
		return
	}
	existing := make(map[string]string, len(aliases))
	for _, a := range aliases {
		if a.Provider == name {
			existing[a.Model] = a.Alias
		}
	}

	out := make([]catalogModelResponse, 0, len(models))
	for _, m := range models {
		alias, imported := existing[m.ID]
		out = append(out, catalogModelResponse{
			ID:       m.ID,
			OwnedBy:  m.OwnedBy,
			Imported: imported,
			Alias:    alias,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// importModels creates one alias per requested upstream model. Existing
// aliases are skipped unless overwrite is set.
func (h *adminHandler) importModels(w http.ResponseWriter, r *http.Request) {
	var req importModelsRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if len(req.Models) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "Field 'models' is required.")
		return
	}

	ctx := r.Context()
	name := urlParam(r, "name")
	record, err := h.store.Providers().Get(ctx, name)
	if err != nil {
		h.fail(w, err)
		return
	}

	prefix := record.AliasPrefix
	if req.Prefix != nil {
		prefix = strings.TrimSpace(*req.Prefix)
	}

	imported := make([]modelResponse, 0, len(req.Models))
	skipped := make([]string, 0)
	for _, model := range req.Models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		alias := prefix + model
		// An alias owned by another provider is never overwritten: aliases are
		// globally unique, so stealing one would silently reroute traffic.
		switch existing, err := h.store.Models().Get(ctx, alias); {
		case err == nil && (existing.Provider != name || !req.Overwrite):
			skipped = append(skipped, alias)
			continue
		case err != nil && !errors.Is(err, storage.ErrNotFound):
			h.fail(w, err)
			return
		}

		record := storage.ModelAlias{Alias: alias, Provider: name, Model: model}
		if err := h.store.Models().Put(ctx, record); err != nil {
			h.fail(w, err)
			return
		}
		imported = append(imported, toModelResponse(record))
	}

	if err := h.loader.Load(ctx); err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, importModelsResponse{Imported: imported, Skipped: skipped})
}

// upstreamFail reports a failed provider call as a 502 with the upstream message.
func (h *adminHandler) upstreamFail(w http.ResponseWriter, name string, err error) {
	h.logger.Error("provider catalog failed", slog.String("provider", name), slog.Any("error", err))
	writeError(w, http.StatusBadGateway, "api_error", upstreamMessage(err))
}

// providerClient builds a one-off client for a stored provider, honouring its
// proxy-pool opt-in so admin probes exercise the same path as routing.
func (h *adminHandler) providerClient(ctx context.Context, rec storage.Provider) provider.Provider {
	pool := router.NewProxyPool(nil)
	if rec.UseProxyPool {
		if proxies, err := h.store.Proxies().List(ctx); err == nil {
			pool = router.NewProxyPool(proxies)
		}
	}
	return router.NewProviderClient(rec, pool)
}

// upstreamMessage surfaces the provider's own error text when it has one.
func upstreamMessage(err error) string {
	var perr *provider.Error
	if errors.As(err, &perr) && perr.Message != "" {
		return perr.Message
	}
	return "Upstream request failed."
}

// --- model aliases ---

type modelResponse struct {
	Alias    string   `json:"alias"`
	Provider string   `json:"provider"`
	Model    string   `json:"model"`
	Fallback []string `json:"fallback"`
}

type modelRequest struct {
	Provider string   `json:"provider"`
	Model    string   `json:"model"`
	Fallback []string `json:"fallback"`
}

func (h *adminHandler) listAliases(w http.ResponseWriter, r *http.Request) {
	aliases, err := h.store.Models().List(r.Context())
	if err != nil {
		h.fail(w, err)
		return
	}

	out := make([]modelResponse, 0, len(aliases))
	for _, m := range aliases {
		out = append(out, toModelResponse(m))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *adminHandler) putAlias(w http.ResponseWriter, r *http.Request) {
	var req modelRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Provider == "" || req.Model == "" {
		writeError(w, http.StatusBadRequest, "invalid_request_error",
			"Fields 'provider' and 'model' are required.")
		return
	}

	ctx := r.Context()
	if _, err := h.store.Providers().Get(ctx, req.Provider); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusBadRequest, "invalid_request_error",
				"Unknown provider '"+req.Provider+"'.")
			return
		}
		h.fail(w, err)
		return
	}

	record := storage.ModelAlias{
		Alias:    urlParam(r, "alias"),
		Provider: req.Provider,
		Model:    req.Model,
		Fallback: req.Fallback,
	}
	if err := h.store.Models().Put(ctx, record); err != nil {
		h.fail(w, err)
		return
	}
	if err := h.loader.Load(ctx); err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toModelResponse(record))
}

func (h *adminHandler) deleteAlias(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := h.store.Models().Delete(ctx, urlParam(r, "alias")); err != nil {
		h.fail(w, err)
		return
	}
	if err := h.loader.Load(ctx); err != nil {
		h.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type testAliasResponse struct {
	Alias     string `json:"alias"`
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	OK        bool   `json:"ok"`
	LatencyMS int64  `json:"latency_ms"`
	TTFBMS    int64  `json:"ttfb_ms"`
	Error     string `json:"error,omitempty"`
}

// testAliasTimeout bounds a probe so a dead upstream cannot hold the dashboard.
const testAliasTimeout = 30 * time.Second

// testAlias sends a minimal completion straight to the alias's own provider and
// reports the round-trip latency. It deliberately bypasses the fallback chain:
// the point is to check this route, not whether some other one can cover it.
// The probe is a real request, so it joins the alias's rolling window —
// appended to it, never replacing the history already there.
func (h *adminHandler) testAlias(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	alias := urlParam(r, "alias")

	record, err := h.store.Models().Get(ctx, alias)
	if err != nil {
		h.fail(w, err)
		return
	}
	providerRecord, err := h.store.Providers().Get(ctx, record.Provider)
	if err != nil {
		h.fail(w, err)
		return
	}

	ctx, cancel := context.WithTimeout(ctx, testAliasTimeout)
	defer cancel()

	// A single token is enough to prove the route answers.
	maxTokens := 1
	req := &openai.ChatCompletionRequest{
		Model:     record.Model,
		Messages:  []openai.Message{{Role: "user", Content: json.RawMessage(`"ping"`)}},
		MaxTokens: &maxTokens,
	}

	ctx, firstByte := provider.WithFirstByte(ctx)
	start := time.Now()
	_, err = h.providerClient(ctx, providerRecord).ChatCompletion(ctx, req)
	latency := time.Since(start)
	ttfb := firstByte.Since(start)

	h.engine.Stats().Record(router.RequestMetric{
		Alias:     alias,
		Success:   err == nil,
		TTFBMs:    ttfb.Milliseconds(),
		LatencyMs: latency.Milliseconds(),
		Timestamp: time.Now(),
	})

	resp := testAliasResponse{
		Alias:     alias,
		Provider:  record.Provider,
		Model:     record.Model,
		OK:        err == nil,
		LatencyMS: latency.Milliseconds(),
		TTFBMS:    ttfb.Milliseconds(),
	}
	if err != nil {
		h.logger.Warn("alias test failed",
			slog.String("alias", alias), slog.String("provider", record.Provider), slog.Any("error", err))
		resp.Error = upstreamMessage(err)
	}
	writeJSON(w, http.StatusOK, resp)
}

func toModelResponse(m storage.ModelAlias) modelResponse {
	fallback := m.Fallback
	if fallback == nil {
		fallback = []string{}
	}
	return modelResponse{Alias: m.Alias, Provider: m.Provider, Model: m.Model, Fallback: fallback}
}

// --- combos ---

type comboResponse struct {
	Name     string   `json:"name"`
	Strategy string   `json:"strategy"`
	Members  []string `json:"members"`
	Weights  []int    `json:"weights"`
	Enabled  bool     `json:"enabled"`
}

type comboRequest struct {
	Strategy string   `json:"strategy"`
	Members  []string `json:"members"`
	Weights  []int    `json:"weights"`
	Enabled  *bool    `json:"enabled"`
}

func (h *adminHandler) listCombos(w http.ResponseWriter, r *http.Request) {
	combos, err := h.store.Combos().List(r.Context())
	if err != nil {
		h.fail(w, err)
		return
	}

	out := make([]comboResponse, 0, len(combos))
	for _, c := range combos {
		out = append(out, toComboResponse(c))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *adminHandler) putCombo(w http.ResponseWriter, r *http.Request) {
	var req comboRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if len(req.Members) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request_error",
			"Field 'members' must list at least one alias.")
		return
	}

	strategy := storage.ComboStrategy(req.Strategy)
	if strategy == "" {
		strategy = storage.ComboFallback
	}
	switch strategy {
	case storage.ComboFallback, storage.ComboRoundRobin, storage.ComboWeighted:
	default:
		writeError(w, http.StatusBadRequest, "invalid_request_error",
			"Field 'strategy' must be 'fallback', 'round_robin' or 'weighted'.")
		return
	}

	weights := req.Weights
	if strategy != storage.ComboWeighted {
		// Weights only mean something to the weighted strategy; storing them
		// for the others would leave stale shares behind a strategy switch.
		weights = nil
	} else if len(weights) > 0 {
		if len(weights) != len(req.Members) {
			writeError(w, http.StatusBadRequest, "invalid_request_error",
				"Field 'weights' must have one entry per member.")
			return
		}
		for _, weight := range weights {
			if weight < 1 {
				writeError(w, http.StatusBadRequest, "invalid_request_error",
					"Field 'weights' must hold positive integers.")
				return
			}
		}
	}

	ctx := r.Context()
	name := urlParam(r, "name")
	if _, err := h.store.Models().Get(ctx, name); err == nil {
		writeError(w, http.StatusConflict, "invalid_request_error",
			"A model alias named '"+name+"' already exists.")
		return
	} else if !errors.Is(err, storage.ErrNotFound) {
		h.fail(w, err)
		return
	}

	for _, member := range req.Members {
		if _, err := h.store.Models().Get(ctx, member); err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				writeError(w, http.StatusBadRequest, "invalid_request_error",
					"Unknown model alias '"+member+"'.")
				return
			}
			h.fail(w, err)
			return
		}
	}

	record := storage.Combo{
		Name:     name,
		Strategy: strategy,
		Members:  req.Members,
		Weights:  weights,
		Enabled:  req.Enabled == nil || *req.Enabled,
	}
	if err := h.store.Combos().Put(ctx, record); err != nil {
		h.fail(w, err)
		return
	}
	if err := h.loader.Load(ctx); err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toComboResponse(record))
}

func (h *adminHandler) deleteCombo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := h.store.Combos().Delete(ctx, urlParam(r, "name")); err != nil {
		h.fail(w, err)
		return
	}
	if err := h.loader.Load(ctx); err != nil {
		h.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func toComboResponse(c storage.Combo) comboResponse {
	members := c.Members
	if members == nil {
		members = []string{}
	}
	weights := c.Weights
	if weights == nil {
		weights = []int{}
	}
	return comboResponse{
		Name:     c.Name,
		Strategy: string(c.Strategy),
		Members:  members,
		Weights:  weights,
		Enabled:  c.Enabled,
	}
}

// --- proxy pool ---

type proxyResponse struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	Enabled bool   `json:"enabled"`
}

type proxyRequest struct {
	URL     string `json:"url"`
	Enabled *bool  `json:"enabled"`
}

// proxySchemes are the outbound proxy schemes net/http can dial through.
var proxySchemes = map[string]bool{"http": true, "https": true, "socks5": true, "socks5h": true}

func (h *adminHandler) listProxies(w http.ResponseWriter, r *http.Request) {
	proxies, err := h.store.Proxies().List(r.Context())
	if err != nil {
		h.fail(w, err)
		return
	}

	out := make([]proxyResponse, 0, len(proxies))
	for _, p := range proxies {
		out = append(out, proxyResponse{Name: p.Name, URL: p.URL, Enabled: p.Enabled})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *adminHandler) putProxy(w http.ResponseWriter, r *http.Request) {
	var req proxyRequest
	if !decodeBody(w, r, &req) {
		return
	}

	raw := strings.TrimSpace(req.URL)
	if raw == "" {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "Field 'url' is required.")
		return
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || !proxySchemes[u.Scheme] {
		writeError(w, http.StatusBadRequest, "invalid_request_error",
			"Field 'url' must be an http, https or socks5 proxy URL.")
		return
	}

	ctx := r.Context()
	record := storage.Proxy{
		Name:    urlParam(r, "name"),
		URL:     raw,
		Enabled: req.Enabled == nil || *req.Enabled,
	}
	if err := h.store.Proxies().Put(ctx, record); err != nil {
		h.fail(w, err)
		return
	}
	if err := h.loader.Load(ctx); err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, proxyResponse{Name: record.Name, URL: record.URL, Enabled: record.Enabled})
}

func (h *adminHandler) deleteProxy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := h.store.Proxies().Delete(ctx, urlParam(r, "name")); err != nil {
		h.fail(w, err)
		return
	}
	if err := h.loader.Load(ctx); err != nil {
		h.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type importProxiesRequest struct {
	// Text holds one proxy per line: a full URL, "host:port" or
	// "host:port:user:pass". Blank lines and #-comments are ignored.
	Text string `json:"text"`
	// Enabled applies to every imported proxy; omitted means enabled.
	Enabled *bool `json:"enabled"`
	// Overwrite replaces proxies whose name already exists instead of
	// skipping them.
	Overwrite bool `json:"overwrite"`
}

type importProxiesResponse struct {
	Imported []proxyResponse `json:"imported"`
	Skipped  []string        `json:"skipped"`
	Invalid  []string        `json:"invalid"`
}

// importProxies bulk-creates proxies from a pasted list, one per line. Lines
// that do not parse are reported back rather than failing the whole batch.
func (h *adminHandler) importProxies(w http.ResponseWriter, r *http.Request) {
	var req importProxiesRequest
	if !decodeBody(w, r, &req) {
		return
	}
	lines := splitProxyLines(req.Text)
	if len(lines) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request_error",
			"Field 'text' must hold at least one proxy line.")
		return
	}

	ctx := r.Context()
	enabled := req.Enabled == nil || *req.Enabled
	resp := importProxiesResponse{
		Imported: []proxyResponse{},
		Skipped:  []string{},
		Invalid:  []string{},
	}
	seen := map[string]bool{}
	for _, line := range lines {
		u, ok := parseProxyLine(line)
		if !ok {
			resp.Invalid = append(resp.Invalid, line)
			continue
		}
		name := u.Host
		if seen[name] {
			resp.Skipped = append(resp.Skipped, name)
			continue
		}
		seen[name] = true
		if !req.Overwrite {
			switch _, err := h.store.Proxies().Get(ctx, name); {
			case err == nil:
				resp.Skipped = append(resp.Skipped, name)
				continue
			case !errors.Is(err, storage.ErrNotFound):
				h.fail(w, err)
				return
			}
		}

		record := storage.Proxy{Name: name, URL: u.String(), Enabled: enabled}
		if err := h.store.Proxies().Put(ctx, record); err != nil {
			h.fail(w, err)
			return
		}
		resp.Imported = append(resp.Imported,
			proxyResponse{Name: record.Name, URL: record.URL, Enabled: record.Enabled})
	}

	if len(resp.Imported) > 0 {
		if err := h.loader.Load(ctx); err != nil {
			h.fail(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// splitProxyLines breaks pasted text into candidate lines, dropping blanks
// and #-comments.
func splitProxyLines(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// parseProxyLine understands the common proxy-list formats:
//
//	scheme://user:pass@host:port
//	host:port
//	host:port:user:pass
//
// Bare forms default to http.
func parseProxyLine(line string) (*url.URL, bool) {
	if strings.Contains(line, "://") {
		u, err := url.Parse(line)
		if err != nil || u.Host == "" || u.Port() == "" || !proxySchemes[u.Scheme] {
			return nil, false
		}
		return u, true
	}

	parts := strings.Split(line, ":")
	switch len(parts) {
	case 2: // host:port
		return buildProxyURL(parts[0], parts[1], "", "")
	case 4: // host:port:user:pass
		return buildProxyURL(parts[0], parts[1], parts[2], parts[3])
	default:
		return nil, false
	}
}

// buildProxyURL assembles and validates an http proxy URL from split fields.
func buildProxyURL(host, port, user, pass string) (*url.URL, bool) {
	if host == "" {
		return nil, false
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return nil, false
	}
	u := &url.URL{Scheme: "http", Host: host + ":" + port}
	if user != "" {
		u.User = url.UserPassword(user, pass)
	}
	return u, true
}

// --- usage ---

type usageEventResponse struct {
	ID               int64   `json:"id"`
	CreatedAt        string  `json:"created_at"`
	Alias            string  `json:"alias"`
	Provider         string  `json:"provider"`
	Model            string  `json:"model"`
	Streamed         bool    `json:"streamed"`
	Status           string  `json:"status"`
	DurationMS       int64   `json:"duration_ms"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	CachedTokens     int     `json:"cached_tokens"`
	ReasoningTokens  int     `json:"reasoning_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	CostUSD          float64 `json:"cost_usd"`
}

type usageBucketResponse struct {
	Start            string  `json:"start"`
	Requests         int     `json:"requests"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	CachedTokens     int     `json:"cached_tokens"`
	CostUSD          float64 `json:"cost_usd"`
}

type usageSummaryResponse struct {
	WindowHours int                   `json:"window_hours"`
	BucketMS    int64                 `json:"bucket_ms"`
	Totals      usageTotalsResponse   `json:"totals"`
	Series      []usageBucketResponse `json:"series"`
}

type usageTotalsResponse struct {
	Requests         int     `json:"requests"`
	Errors           int     `json:"errors"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	CachedTokens     int     `json:"cached_tokens"`
	CostUSD          float64 `json:"cost_usd"`
}

// usageWindow clamps the ?hours= parameter to a sane range and picks a bucket
// size that keeps the series around 60 points regardless of the window.
func usageWindow(r *http.Request) (time.Duration, time.Duration) {
	hours := 24
	if raw := r.URL.Query().Get("hours"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			hours = n
		}
	}
	if hours > 24*90 {
		hours = 24 * 90
	}

	window := time.Duration(hours) * time.Hour
	bucket := window / 60
	if bucket < time.Minute {
		bucket = time.Minute
	}
	return window, bucket
}

func (h *adminHandler) usageSummary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	window, bucket := usageWindow(r)
	since := time.Now().UTC().Add(-window)

	totals, err := h.store.Usage().Totals(ctx, since)
	if err != nil {
		h.fail(w, err)
		return
	}
	series, err := h.store.Usage().Series(ctx, since, bucket)
	if err != nil {
		h.fail(w, err)
		return
	}

	out := usageSummaryResponse{
		WindowHours: int(window / time.Hour),
		BucketMS:    bucket.Milliseconds(),
		Totals: usageTotalsResponse{
			Requests:         totals.Requests,
			Errors:           totals.Errors,
			PromptTokens:     totals.PromptTokens,
			CompletionTokens: totals.CompletionTokens,
			CachedTokens:     totals.CachedTokens,
			CostUSD:          totals.CostUSD,
		},
		Series: make([]usageBucketResponse, 0, len(series)),
	}

	// Emit every bucket in the window, including empty ones: a chart that
	// interpolates across idle gaps reads as sustained traffic that never
	// happened.
	filled := map[int64]storage.UsageBucket{}
	for _, b := range series {
		filled[b.Start.Unix()] = b
	}
	for t := since.Truncate(bucket); !t.After(time.Now().UTC()); t = t.Add(bucket) {
		b := filled[t.Unix()]
		out.Series = append(out.Series, usageBucketResponse{
			Start:            t.Format(time.RFC3339),
			Requests:         b.Requests,
			PromptTokens:     b.PromptTokens,
			CompletionTokens: b.CompletionTokens,
			CachedTokens:     b.CachedTokens,
			CostUSD:          b.CostUSD,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *adminHandler) usageRecent(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 200 {
		limit = 200
	}

	events, err := h.store.Usage().Recent(r.Context(), limit)
	if err != nil {
		h.fail(w, err)
		return
	}

	out := make([]usageEventResponse, 0, len(events))
	for _, e := range events {
		out = append(out, usageEventResponse{
			ID:               e.ID,
			CreatedAt:        e.CreatedAt.Format(time.RFC3339),
			Alias:            e.Alias,
			Provider:         e.Provider,
			Model:            e.Model,
			Streamed:         e.Streamed,
			Status:           e.Status,
			DurationMS:       e.Duration.Milliseconds(),
			PromptTokens:     e.PromptTokens,
			CompletionTokens: e.CompletionTokens,
			CachedTokens:     e.CachedTokens,
			ReasoningTokens:  e.ReasoningTokens,
			TotalTokens:      e.PromptTokens + e.CompletionTokens,
			CostUSD:          e.CostUSD,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

type modelUsageResponse struct {
	Alias            string  `json:"alias"`
	Requests         int     `json:"requests"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	CachedTokens     int     `json:"cached_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	CostUSD          float64 `json:"cost_usd"`
	LastUsed         string  `json:"last_used"`
}

func (h *adminHandler) usageByModel(w http.ResponseWriter, r *http.Request) {
	models, err := h.store.Usage().ByModel(r.Context())
	if err != nil {
		h.fail(w, err)
		return
	}

	out := make([]modelUsageResponse, 0, len(models))
	for _, m := range models {
		out = append(out, modelUsageResponse{
			Alias:            m.Alias,
			Requests:         m.Requests,
			PromptTokens:     m.PromptTokens,
			CompletionTokens: m.CompletionTokens,
			CachedTokens:     m.CachedTokens,
			TotalTokens:      m.PromptTokens + m.CompletionTokens,
			CostUSD:          m.CostUSD,
			LastUsed:         m.LastUsed.Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// usageLeaderboard ranks aliases inside the requested window. ?sort= picks the
// ranking metric: requests (default), tokens, or cost.
func (h *adminHandler) usageLeaderboard(w http.ResponseWriter, r *http.Request) {
	window, _ := usageWindow(r)

	limit := 10
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 50 {
			limit = n
		}
	}

	sortBy := storage.LeaderboardByRequests
	switch r.URL.Query().Get("sort") {
	case "tokens":
		sortBy = storage.LeaderboardByTokens
	case "cost":
		sortBy = storage.LeaderboardByCost
	}

	since := time.Now().Add(-window)
	models, err := h.store.Usage().Leaderboard(r.Context(), since, limit, sortBy)
	if err != nil {
		h.fail(w, err)
		return
	}

	out := make([]modelUsageResponse, 0, len(models))
	for _, m := range models {
		out = append(out, modelUsageResponse{
			Alias:            m.Alias,
			Requests:         m.Requests,
			PromptTokens:     m.PromptTokens,
			CompletionTokens: m.CompletionTokens,
			CachedTokens:     m.CachedTokens,
			TotalTokens:      m.PromptTokens + m.CompletionTokens,
			CostUSD:          m.CostUSD,
			LastUsed:         m.LastUsed.Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

type keyUsageResponse struct {
	KeyID            string  `json:"key_id"`
	Name             string  `json:"name"`
	Requests         int     `json:"requests"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	CachedTokens     int     `json:"cached_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	CostUSD          float64 `json:"cost_usd"`
	LastUsed         string  `json:"last_used"`
}

// usageByKey ranks API keys by spend inside the requested window. Events with
// no key id are reported under an empty id as unattributed traffic.
func (h *adminHandler) usageByKey(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	window, _ := usageWindow(r)

	rows, err := h.store.Usage().ByKey(ctx, time.Now().Add(-window))
	if err != nil {
		h.fail(w, err)
		return
	}

	// Names come from the key store; a deleted key keeps its usage rows.
	names := map[string]string{}
	if keys, err := h.store.APIKeys().List(ctx); err == nil {
		for _, k := range keys {
			names[k.ID] = k.Name
		}
	}

	out := make([]keyUsageResponse, 0, len(rows))
	for _, u := range rows {
		name := names[u.KeyID]
		if name == "" {
			name = "unattributed"
		}
		out = append(out, keyUsageResponse{
			KeyID:            u.KeyID,
			Name:             name,
			Requests:         u.Requests,
			PromptTokens:     u.PromptTokens,
			CompletionTokens: u.CompletionTokens,
			CachedTokens:     u.CachedTokens,
			TotalTokens:      u.PromptTokens + u.CompletionTokens,
			CostUSD:          u.CostUSD,
			LastUsed:         u.LastUsed.Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// --- settings ---

func (h *adminHandler) listSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := h.store.Settings().All(r.Context())
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (h *adminHandler) putSettings(w http.ResponseWriter, r *http.Request) {
	var req map[string]string
	if !decodeBody(w, r, &req) {
		return
	}

	ctx := r.Context()
	for key, value := range req {
		if err := h.store.Settings().Put(ctx, key, value); err != nil {
			h.fail(w, err)
			return
		}
	}
	h.listSettings(w, r)
}

// --- helpers ---

// urlParam reads a path parameter and percent-decodes it. Aliases like
// "wz/gemini-3.8-flash" contain a slash, so clients send them encoded and chi
// hands back the still-escaped segment.
func urlParam(r *http.Request, key string) string {
	raw := chi.URLParam(r, key)
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		return raw
	}
	return decoded
}

func decodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeDecodeError(w, err, "Request body is not valid JSON.")
		return false
	}
	return true
}

// fail maps a storage failure onto a response without leaking driver detail.
func (h *adminHandler) fail(w http.ResponseWriter, err error) {
	if errors.Is(err, storage.ErrNotFound) {
		writeError(w, http.StatusNotFound, "invalid_request_error", "Resource not found.")
		return
	}
	h.logger.Error("admin request failed", slog.Any("error", err))
	writeError(w, http.StatusInternalServerError, "api_error", "Internal server error.")
}
