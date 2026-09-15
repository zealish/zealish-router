package api

import (
	"io"
	"log/slog"
	"net/http"

	"github.com/zealish/zealish-router/internal/cache"
	"github.com/zealish/zealish-router/internal/metrics"
)

// cacheHeader tells a client where its response came from. BYPASS means the
// request was never eligible — the cache is off, or the response streams.
const (
	cacheHeader = "X-Cache"
	headerHit   = "HIT"
	headerMiss  = "MISS"
	headerPass  = "BYPASS"
)

// Endpoint labels for cache keys and metrics. They are stable names rather
// than request paths so a label never depends on how a client spelled the URL.
const (
	endpointChat       = "chat"
	endpointEmbeddings = "embeddings"
	// The Messages endpoint keys separately from chat: the bodies are a
	// different dialect, so an identical prompt is not an identical request.
	endpointMessages = "messages"
)

// readBody buffers the raw request body. The cache keys on the exact bytes a
// client sent, so the body is read once here and decoded from memory instead
// of being streamed into the JSON decoder.
func (h *handler) readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		// limitBody wraps the body in a MaxBytesReader, so an oversized
		// payload fails here and is reported as a 413.
		writeDecodeError(w, err, "Malformed JSON body.")
		return nil, false
	}
	return raw, true
}

// lookupCache serves a cached response when one is present. The returned key
// is what a later store must use; it is empty when the cache is disabled, in
// which case storing is a no-op too.
//
// Entries are shared across API keys on purpose: the key is the request body,
// and an identical body produces an identical response whoever sent it.
func (h *handler) lookupCache(w http.ResponseWriter, endpoint, model string, body []byte) (string, bool) {
	if !h.cache.Enabled() {
		w.Header().Set(cacheHeader, headerPass)
		return "", false
	}

	key := cache.Key(endpoint, model, body)
	entry, ok := h.cache.Get(key)
	if !ok {
		w.Header().Set(cacheHeader, headerMiss)
		h.metrics.RecordCacheEvent(endpoint, model, metrics.CacheMiss)
		return key, false
	}

	h.metrics.RecordCacheEvent(endpoint, model, metrics.CacheHit)
	h.logger.Debug("served from response cache",
		slog.String("endpoint", endpoint),
		slog.String("model", model))

	w.Header().Set(cacheHeader, headerHit)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(entry.Body)
	return key, true
}

// storeCache admits a freshly produced response. An empty key means the
// lookup found the cache disabled.
func (h *handler) storeCache(endpoint, model, key string, body []byte) {
	if key == "" {
		return
	}
	h.cache.Put(key, cache.Entry{Body: body, Model: model})
	h.metrics.RecordCacheEvent(endpoint, model, metrics.CacheStore)
}
