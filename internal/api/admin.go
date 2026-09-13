package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/zealish/zealish-router/internal/auth"
	"github.com/zealish/zealish-router/internal/metrics"
	"github.com/zealish/zealish-router/internal/router"
	"github.com/zealish/zealish-router/internal/storage"
)

// adminHandler serves the dashboard's internal REST API. It owns the routing
// configuration: every mutation writes to storage and then republishes the
// engine's routing table, so the database stays the single source of truth.
type adminHandler struct {
	store   storage.Store
	loader  *router.Loader
	metrics *metrics.Metrics
	logger  *slog.Logger
}

func newAdminHandler(deps Dependencies) *adminHandler {
	return &adminHandler{
		store:   deps.Store,
		loader:  deps.Loader,
		metrics: deps.Metrics,
		logger:  deps.Logger,
	}
}

func (h *adminHandler) routes(r chi.Router) {
	r.Get("/overview", h.overview)

	r.Get("/keys", h.listKeys)
	r.Post("/keys", h.createKey)
	r.Delete("/keys/{id}", h.deleteKey)

	r.Get("/providers", h.listProviders)
	r.Put("/providers/{name}", h.putProvider)
	r.Delete("/providers/{name}", h.deleteProvider)

	r.Get("/models", h.listAliases)
	r.Put("/models/{alias}", h.putAlias)
	r.Delete("/models/{alias}", h.deleteAlias)

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
	APIKeys       int     `json:"api_keys"`
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
		APIKeys:       len(keys),
	}
	if snapshot.Requests > 0 {
		resp.ErrorRate = snapshot.RequestErrors / snapshot.Requests
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
}

type createKeyRequest struct {
	Name string `json:"name"`
}

type createKeyResponse struct {
	apiKeyResponse
	// Key is the raw credential. It is returned exactly once, at creation.
	Key string `json:"key"`
}

func (h *adminHandler) listKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := h.store.APIKeys().List(r.Context())
	if err != nil {
		h.fail(w, err)
		return
	}

	out := make([]apiKeyResponse, 0, len(keys))
	for _, k := range keys {
		out = append(out, toAPIKeyResponse(k))
	}
	writeJSON(w, http.StatusOK, out)
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
	if err := h.store.APIKeys().Delete(r.Context(), chi.URLParam(r, "id")); err != nil {
		h.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func toAPIKeyResponse(k storage.APIKey) apiKeyResponse {
	resp := apiKeyResponse{
		ID:        k.ID,
		Name:      k.Name,
		Enabled:   k.Enabled,
		CreatedAt: k.CreatedAt,
	}
	if !k.LastUsedAt.IsZero() {
		used := k.LastUsedAt
		resp.LastUsedAt = &used
	}
	return resp
}

// --- providers ---

type providerResponse struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	BaseURL   string `json:"base_url"`
	HasAPIKey bool   `json:"has_api_key"`
	TimeoutMS int64  `json:"timeout_ms"`
	Enabled   bool   `json:"enabled"`
}

type providerRequest struct {
	Kind      string `json:"kind"`
	BaseURL   string `json:"base_url"`
	APIKey    string `json:"api_key"`
	TimeoutMS int64  `json:"timeout_ms"`
	Enabled   bool   `json:"enabled"`
}

func (h *adminHandler) listProviders(w http.ResponseWriter, r *http.Request) {
	providers, err := h.store.Providers().List(r.Context())
	if err != nil {
		h.fail(w, err)
		return
	}

	out := make([]providerResponse, 0, len(providers))
	for _, p := range providers {
		out = append(out, providerResponse{
			Name:      p.Name,
			Kind:      p.Kind,
			BaseURL:   p.BaseURL,
			HasAPIKey: p.APIKey != "",
			TimeoutMS: p.Timeout.Milliseconds(),
			Enabled:   p.Enabled,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *adminHandler) putProvider(w http.ResponseWriter, r *http.Request) {
	var req providerRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.BaseURL == "" {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "Field 'base_url' is required.")
		return
	}

	ctx := r.Context()
	name := chi.URLParam(r, "name")

	record := storage.Provider{
		ID:      name,
		Name:    name,
		Kind:    req.Kind,
		BaseURL: req.BaseURL,
		APIKey:  req.APIKey,
		Timeout: time.Duration(req.TimeoutMS) * time.Millisecond,
		Enabled: req.Enabled,
	}
	if record.Kind == "" {
		record.Kind = "openai"
	}
	// An omitted api_key means "keep the current secret": the dashboard never
	// receives it back, so it cannot echo it on update.
	if record.APIKey == "" {
		if existing, err := h.store.Providers().Get(ctx, name); err == nil {
			record.APIKey = existing.APIKey
		}
	}

	if err := h.store.Providers().Put(ctx, record); err != nil {
		h.fail(w, err)
		return
	}
	if err := h.loader.Load(ctx); err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, providerResponse{
		Name:      record.Name,
		Kind:      record.Kind,
		BaseURL:   record.BaseURL,
		HasAPIKey: record.APIKey != "",
		TimeoutMS: record.Timeout.Milliseconds(),
		Enabled:   record.Enabled,
	})
}

func (h *adminHandler) deleteProvider(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := h.store.Providers().Delete(ctx, chi.URLParam(r, "name")); err != nil {
		h.fail(w, err)
		return
	}
	if err := h.loader.Load(ctx); err != nil {
		h.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
		Alias:    chi.URLParam(r, "alias"),
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
	if err := h.store.Models().Delete(ctx, chi.URLParam(r, "alias")); err != nil {
		h.fail(w, err)
		return
	}
	if err := h.loader.Load(ctx); err != nil {
		h.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func toModelResponse(m storage.ModelAlias) modelResponse {
	fallback := m.Fallback
	if fallback == nil {
		fallback = []string{}
	}
	return modelResponse{Alias: m.Alias, Provider: m.Provider, Model: m.Model, Fallback: fallback}
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

func decodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "Request body is not valid JSON.")
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
