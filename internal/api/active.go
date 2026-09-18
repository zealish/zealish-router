package api

import (
	"net/http"
	"sort"
	"sync"
	"time"
)

// activeRequest is the safe, dashboard-facing identity of a request currently
// being handled by the gateway. It deliberately contains no credential data.
type activeRequest struct {
	RequestID string    `json:"request_id"`
	APIKey    string    `json:"api_key"`
	Model     string    `json:"model"`
	StartedAt time.Time `json:"started_at"`
}

type activeRequests struct {
	mu      sync.RWMutex
	entries map[string]activeRequest
}

func newActiveRequests() *activeRequests {
	return &activeRequests{entries: make(map[string]activeRequest)}
}

func (a *activeRequests) add(req activeRequest) {
	if a == nil || req.RequestID == "" {
		return
	}
	a.mu.Lock()
	a.entries[req.RequestID] = req
	a.mu.Unlock()
}

func (a *activeRequests) remove(requestID string) {
	if a == nil || requestID == "" {
		return
	}
	a.mu.Lock()
	delete(a.entries, requestID)
	a.mu.Unlock()
}

func (a *activeRequests) list() []activeRequest {
	if a == nil {
		return []activeRequest{}
	}
	a.mu.RLock()
	entries := make([]activeRequest, 0, len(a.entries))
	for _, req := range a.entries {
		entries = append(entries, req)
	}
	a.mu.RUnlock()
	return entries
}

func (h *adminHandler) activeRequests(w http.ResponseWriter, _ *http.Request) {
	entries := h.active.list()
	sort.Slice(entries, func(i, j int) bool { return entries[i].StartedAt.Before(entries[j].StartedAt) })
	writeJSON(w, http.StatusOK, entries)
}
