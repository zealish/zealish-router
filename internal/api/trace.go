package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/zealish/zealish-router/internal/storage"
)

// Trace listings are paginated. The default keeps one screen of the dashboard
// table; the cap stops a client asking for the whole retention window at once.
const (
	defaultTraceLimit = 25
	maxTraceLimit     = 200
)

// requestTraceResponse is one row of the request list. Attempts are omitted
// here and served by the detail endpoint, so the list stays a single scan.
type requestTraceResponse struct {
	RequestID     string  `json:"request_id"`
	CreatedAt     string  `json:"created_at"`
	KeyID         string  `json:"api_key"`
	Model         string  `json:"model"`
	Streamed      bool    `json:"streamed"`
	TotalLatency  int64   `json:"total_latency_ms"`
	TotalTokens   int     `json:"total_tokens"`
	TotalCostUSD  float64 `json:"total_cost_usd"`
	FinalProvider string  `json:"final_provider"`
	FinalAlias    string  `json:"final_alias"`
	FinalStatus   string  `json:"final_status"`
	AttemptCount  int     `json:"attempt_count"`

	// Attempts is the full timeline, served only by the detail endpoint.
	Attempts []requestAttemptResponse `json:"attempts,omitempty"`
}

// requestAttemptResponse is one upstream call in a trace timeline.
type requestAttemptResponse struct {
	Seq       int    `json:"seq"`
	StartedAt string `json:"started_at"`
	Alias     string `json:"alias"`
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	LatencyMS int64  `json:"latency_ms"`
	Status    string `json:"status"`
	Retry     bool   `json:"retry"`
	Fallback  bool   `json:"fallback"`
	Error     string `json:"error,omitempty"`
}

// requestListResponse wraps the page with the totals the table needs to render
// its pagination controls.
type requestListResponse struct {
	Items  []requestTraceResponse `json:"items"`
	Total  int                    `json:"total"`
	Limit  int                    `json:"limit"`
	Offset int                    `json:"offset"`
}

// listRequests serves the newest-first request log, filtered and paginated.
func (h *adminHandler) listRequests(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := storage.TraceFilter{
		Status:   q.Get("status"),
		Model:    q.Get("model"),
		Provider: q.Get("provider"),
		KeyID:    q.Get("api_key"),
		Limit:    queryInt(q.Get("limit"), defaultTraceLimit, maxTraceLimit),
		Offset:   queryInt(q.Get("offset"), 0, 0),
	}

	traces, total, err := h.store.Traces().List(r.Context(), filter)
	if err != nil {
		h.fail(w, err)
		return
	}

	items := make([]requestTraceResponse, 0, len(traces))
	for _, t := range traces {
		items = append(items, toTraceResponse(t))
	}
	writeJSON(w, http.StatusOK, requestListResponse{
		Items:  items,
		Total:  total,
		Limit:  filter.Limit,
		Offset: filter.Offset,
	})
}

// getRequest serves one trace with its full attempt timeline.
func (h *adminHandler) getRequest(w http.ResponseWriter, r *http.Request) {
	trace, err := h.store.Traces().Get(r.Context(), urlParam(r, "request_id"))
	if err != nil {
		h.fail(w, err)
		return
	}

	resp := toTraceResponse(trace)
	resp.Attempts = make([]requestAttemptResponse, 0, len(trace.Attempts))
	for _, a := range trace.Attempts {
		resp.Attempts = append(resp.Attempts, requestAttemptResponse{
			Seq:       a.Seq,
			StartedAt: a.StartedAt.Format(time.RFC3339),
			Alias:     a.Alias,
			Provider:  a.Provider,
			Model:     a.Model,
			LatencyMS: a.Latency.Milliseconds(),
			Status:    a.Status,
			Retry:     a.Retry,
			Fallback:  a.Fallback,
			Error:     a.Error,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

func toTraceResponse(t storage.RequestTrace) requestTraceResponse {
	return requestTraceResponse{
		RequestID:     t.RequestID,
		CreatedAt:     t.CreatedAt.Format(time.RFC3339),
		KeyID:         t.KeyID,
		Model:         t.Model,
		Streamed:      t.Streamed,
		TotalLatency:  t.TotalLatency.Milliseconds(),
		TotalTokens:   t.TotalTokens,
		TotalCostUSD:  t.TotalCostUSD,
		FinalProvider: t.FinalProvider,
		FinalAlias:    t.FinalAlias,
		FinalStatus:   t.FinalStatus,
		AttemptCount:  t.AttemptCount,
	}
}

// queryInt parses a non-negative query parameter, falling back to fallbackTo
// when it is absent or unparseable. A limit of zero means unbounded.
func queryInt(raw string, fallbackTo, limit int) int {
	if raw == "" {
		return fallbackTo
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return fallbackTo
	}
	if limit > 0 && n > limit {
		return limit
	}
	return n
}
