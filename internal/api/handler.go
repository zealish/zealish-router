package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"

	"github.com/zealish/zealish-router/internal/auth"
	"github.com/zealish/zealish-router/internal/cache"
	"github.com/zealish/zealish-router/internal/metrics"
	"github.com/zealish/zealish-router/internal/provider"
	"github.com/zealish/zealish-router/internal/router"
	"github.com/zealish/zealish-router/internal/stream"
	"github.com/zealish/zealish-router/pkg/openai"
)

type handler struct {
	engine  *router.Engine
	metrics *metrics.Metrics
	cache   *cache.Cache
	logger  *slog.Logger
}

func newHandler(deps Dependencies) *handler {
	return &handler{engine: deps.Engine, metrics: deps.Metrics, cache: deps.Cache, logger: deps.Logger}
}

func (h *handler) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *handler) listModels(w http.ResponseWriter, r *http.Request) {
	// Combos are addressable model names too, so clients see one flat list.
	names := append(h.engine.Aliases(), h.engine.Combos()...)
	sort.Strings(names)

	// A key with an allowlist only discovers what it may actually call.
	identity, _ := auth.FromContext(r.Context())

	list := openai.ModelList{Object: "list", Data: make([]openai.Model, 0, len(names))}
	for _, name := range names {
		if !identity.Allows(name) {
			continue
		}
		list.Data = append(list.Data, openai.Model{
			ID:           name,
			Object:       "model",
			Capabilities: h.engine.Capabilities(name),
		})
	}
	writeJSON(w, http.StatusOK, list)
}

// allowModel rejects a request whose model is outside the key's allowlist. The
// check lives here rather than in a middleware because the model name is in
// the body, which only the handler has decoded.
func allowModel(w http.ResponseWriter, r *http.Request, model string) bool {
	identity, ok := auth.FromContext(r.Context())
	if !ok || identity.Allows(model) {
		return true
	}
	// 403, not 404: the model may well exist, this key just cannot reach it.
	writeError(w, http.StatusForbidden, "invalid_request_error",
		"Model '"+model+"' is not permitted for this API key.")
	return false
}

func (h *handler) chatCompletions(w http.ResponseWriter, r *http.Request) {
	raw, ok := h.readBody(w, r)
	if !ok {
		return
	}

	var req openai.ChatCompletionRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		writeDecodeError(w, err, "Malformed JSON body.")
		return
	}
	if req.Model == "" {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "Field 'model' is required.")
		return
	}
	if !allowModel(w, r, req.Model) {
		return
	}

	if req.Stream {
		// A stream is relayed chunk by chunk and never buffered, so it is
		// neither served from nor admitted to the cache.
		w.Header().Set(cacheHeader, headerPass)
		h.streamCompletion(w, r, &req)
		return
	}

	key, served := h.lookupCache(w, endpointChat, req.Model, raw)
	if served {
		return
	}

	resp, err := h.engine.ChatCompletion(r.Context(), &req)
	if err != nil {
		h.writeEngineError(w, err)
		return
	}

	body, err := json.Marshal(resp)
	if err != nil {
		h.logger.Error("encode completion", slog.Any("error", err))
		writeError(w, http.StatusInternalServerError, "api_error", "Failed to encode the upstream response.")
		return
	}
	h.storeCache(endpointChat, req.Model, key, body)
	writeBody(w, body)
}

func (h *handler) embeddings(w http.ResponseWriter, r *http.Request) {
	raw, ok := h.readBody(w, r)
	if !ok {
		return
	}

	var req openai.EmbeddingRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		writeDecodeError(w, err, "Malformed JSON body.")
		return
	}
	if req.Model == "" {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "Field 'model' is required.")
		return
	}
	if len(req.Input) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "Field 'input' is required.")
		return
	}
	if !allowModel(w, r, req.Model) {
		return
	}

	key, served := h.lookupCache(w, endpointEmbeddings, req.Model, raw)
	if served {
		return
	}

	resp, err := h.engine.Embeddings(r.Context(), &req)
	if err != nil {
		h.writeEngineError(w, err)
		return
	}

	body, err := json.Marshal(resp)
	if err != nil {
		h.logger.Error("encode embeddings", slog.Any("error", err))
		writeError(w, http.StatusInternalServerError, "api_error", "Failed to encode the upstream response.")
		return
	}
	h.storeCache(endpointEmbeddings, req.Model, key, body)
	writeBody(w, body)
}

func (h *handler) streamCompletion(w http.ResponseWriter, r *http.Request, req *openai.ChatCompletionRequest) {
	sse, err := stream.NewWriter(w)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api_error", "Streaming is unsupported by the transport.")
		return
	}

	// Establishing the upstream stream may still fall back across providers, so
	// no headers are committed until the first chunk is framed.
	chunks, err := h.engine.ChatCompletionStream(r.Context(), req)
	if err != nil {
		h.writeEngineError(w, err)
		return
	}

	h.metrics.StreamConnections.Inc()
	defer h.metrics.StreamConnections.Dec()

	if err := stream.Relay(r.Context(), sse, chunks, stream.DefaultHeartbeat); err != nil {
		// The response is already committed, so the status cannot change; log
		// and let the connection close.
		if errors.Is(err, context.Canceled) {
			h.logger.Debug("client disconnected mid-stream", slog.String("model", req.Model))
			return
		}
		h.logger.Error("stream aborted", slog.String("model", req.Model), slog.Any("error", err))
	}
}

func (h *handler) writeEngineError(w http.ResponseWriter, err error) {
	var upstream *provider.Error

	switch {
	case errors.Is(err, router.ErrUnknownModel):
		writeError(w, http.StatusNotFound, "invalid_request_error", err.Error())
	case errors.Is(err, provider.ErrNotFound):
		writeError(w, http.StatusNotFound, "invalid_request_error", err.Error())
	case errors.Is(err, context.Canceled):
		// Client hung up; nothing useful left to write.
		return
	case errors.Is(err, provider.ErrUnsupported):
		// The alias resolves to a dialect without this endpoint: a routing
		// mistake, but the client is the one who has to pick another model.
		h.logger.Warn("endpoint unsupported by route", slog.Any("error", err))
		writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
	case errors.As(err, &upstream) && !provider.Retryable(err) && upstream.Status > 0:
		// Terminal 4xx from upstream: surface it as-is, no fallback happened.
		h.logger.Warn("upstream rejected request",
			slog.String("provider", upstream.Provider),
			slog.Int("status", upstream.Status),
			slog.Any("error", err))
		writeError(w, upstream.Status, "invalid_request_error", upstream.Message)
	default:
		h.logger.Error("upstream failure", slog.Any("error", err))
		writeError(w, http.StatusBadGateway, "api_error", "Upstream provider request failed.")
	}
}
