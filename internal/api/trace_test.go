package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/zealish/zealish-router/internal/provider"
	"github.com/zealish/zealish-router/internal/storage"
)

func seedTrace(t *testing.T, store storage.Store, id string, at time.Time, model, provider, status string) {
	t.Helper()

	trace := storage.RequestTrace{
		RequestID:     id,
		CreatedAt:     at,
		KeyID:         "key-1",
		Model:         model,
		TotalLatency:  900 * time.Millisecond,
		TotalTokens:   120,
		TotalCostUSD:  0.0015,
		FinalProvider: provider,
		FinalAlias:    model,
		FinalStatus:   status,
		Attempts: []storage.RequestAttempt{{
			Seq:       1,
			StartedAt: at,
			Alias:     model,
			Provider:  provider,
			Model:     model + "-upstream",
			Latency:   900 * time.Millisecond,
			Status:    status,
		}},
	}
	if err := store.Traces().Record(context.Background(), trace); err != nil {
		t.Fatalf("seed trace %s: %v", id, err)
	}
}

// A request through /v1/messages is traced as Anthropic, one through
// /v1/chat/completions as OpenAI, so the dashboard can tell the two client
// populations apart even though both resolve the same alias.
func TestGatewayRecordsClientDialect(t *testing.T) {
	h, store, engine := newAdminServer(t)
	engine.SetTraceStore(store.Traces())
	engine.Reload([]storage.ModelAlias{
		{Alias: "gpt-5", Provider: "openai", Model: "gpt-5-upstream"},
	}, nil, provider.NewRegistry(&stubProvider{name: "openai"}))

	if rec := postMessages(t, h, `{"model":"gpt-5","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`); rec.Code != http.StatusOK {
		t.Fatalf("messages status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if rec := post(t, h, `{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`); rec.Code != http.StatusOK {
		t.Fatalf("chat status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}

	traces, _, err := store.Traces().List(context.Background(), storage.TraceFilter{})
	if err != nil {
		t.Fatalf("list traces: %v", err)
	}
	if len(traces) != 2 {
		t.Fatalf("traces = %d, want one per request", len(traces))
	}

	got := map[string]int{}
	for _, tr := range traces {
		got[tr.Dialect]++
	}
	if got[storage.DialectAnthropic] != 1 || got[storage.DialectOpenAI] != 1 {
		t.Fatalf("dialects = %v, want one of each", got)
	}

	// The admin API serves the dialect and filters on it.
	rec := adminRequest(t, h, http.MethodGet, "/api/v1/requests?dialect=anthropic", "")
	list := decodeJSON[requestListResponse](t, rec)
	if list.Total != 1 || len(list.Items) != 1 {
		t.Fatalf("filtered list = %+v, want a single Anthropic trace", list)
	}
	if list.Items[0].Dialect != storage.DialectAnthropic {
		t.Errorf("dialect = %q, want %q", list.Items[0].Dialect, storage.DialectAnthropic)
	}
}

func TestListRequestsIsNewestFirst(t *testing.T) {
	h, store, _ := newAdminServer(t)
	now := time.Now().UTC().Truncate(time.Second)

	seedTrace(t, store, "req-old", now.Add(-time.Hour), "gpt-5", "openai", "ok")
	seedTrace(t, store, "req-new", now, "claude", "anthropic", "timeout")

	rec := adminRequest(t, h, http.MethodGet, "/api/v1/requests", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	body := decodeJSON[requestListResponse](t, rec)
	if body.Total != 2 {
		t.Errorf("total = %d, want 2", body.Total)
	}
	if body.Limit != defaultTraceLimit {
		t.Errorf("limit = %d, want the default %d", body.Limit, defaultTraceLimit)
	}
	if len(body.Items) != 2 {
		t.Fatalf("len(items) = %d, want 2", len(body.Items))
	}
	if body.Items[0].RequestID != "req-new" {
		t.Errorf("first item = %s, want the newest req-new", body.Items[0].RequestID)
	}
	if body.Items[0].FinalStatus != "timeout" {
		t.Errorf("final_status = %q, want timeout", body.Items[0].FinalStatus)
	}
	if body.Items[0].AttemptCount != 1 {
		t.Errorf("attempt_count = %d, want 1", body.Items[0].AttemptCount)
	}
	// The list stays a summary: attempts belong to the detail endpoint.
	if len(body.Items[0].Attempts) != 0 {
		t.Errorf("list returned %d attempts, want none", len(body.Items[0].Attempts))
	}
}

func TestListRequestsFilters(t *testing.T) {
	h, store, _ := newAdminServer(t)
	now := time.Now().UTC()

	seedTrace(t, store, "req-ok", now, "gpt-5", "openai", "ok")
	seedTrace(t, store, "req-fail", now.Add(time.Minute), "claude", "anthropic", "timeout")

	cases := []struct {
		name  string
		query string
		want  string
	}{
		{"status", "?status=timeout", "req-fail"},
		{"model", "?model=gpt-5", "req-ok"},
		{"provider", "?provider=anthropic", "req-fail"},
		{"api key", "?api_key=key-1&status=ok", "req-ok"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := adminRequest(t, h, http.MethodGet, "/api/v1/requests"+tc.query, "")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}

			body := decodeJSON[requestListResponse](t, rec)
			if body.Total != 1 {
				t.Fatalf("total = %d, want 1", body.Total)
			}
			if body.Items[0].RequestID != tc.want {
				t.Errorf("item = %s, want %s", body.Items[0].RequestID, tc.want)
			}
		})
	}
}

func TestListRequestsPaginates(t *testing.T) {
	h, store, _ := newAdminServer(t)
	now := time.Now().UTC()

	for i, id := range []string{"a", "b", "c"} {
		seedTrace(t, store, id, now.Add(time.Duration(i)*time.Minute), "gpt-5", "openai", "ok")
	}

	rec := adminRequest(t, h, http.MethodGet, "/api/v1/requests?limit=2&offset=2", "")
	body := decodeJSON[requestListResponse](t, rec)

	if body.Total != 3 {
		t.Errorf("total = %d, want 3", body.Total)
	}
	if body.Offset != 2 || body.Limit != 2 {
		t.Errorf("limit/offset = %d/%d, want 2/2", body.Limit, body.Offset)
	}
	if len(body.Items) != 1 || body.Items[0].RequestID != "a" {
		t.Fatalf("page = %v, want the oldest trace only", body.Items)
	}
}

func TestListRequestsCapsLimit(t *testing.T) {
	h, _, _ := newAdminServer(t)

	rec := adminRequest(t, h, http.MethodGet, "/api/v1/requests?limit=100000", "")
	body := decodeJSON[requestListResponse](t, rec)

	if body.Limit != maxTraceLimit {
		t.Errorf("limit = %d, want it capped at %d", body.Limit, maxTraceLimit)
	}
}

func TestGetRequestReturnsAttempts(t *testing.T) {
	h, store, _ := newAdminServer(t)
	now := time.Now().UTC().Truncate(time.Second)

	trace := storage.RequestTrace{
		RequestID:     "req-1",
		CreatedAt:     now,
		Model:         "gpt-5",
		TotalLatency:  1500 * time.Millisecond,
		TotalTokens:   200,
		FinalProvider: "openrouter",
		FinalAlias:    "fast",
		FinalStatus:   "ok",
		Attempts: []storage.RequestAttempt{
			{
				Seq: 1, StartedAt: now, Alias: "gpt-5", Provider: "openai",
				Model: "gpt-5-upstream", Latency: 500 * time.Millisecond,
				Status: "upstream_5xx", Error: "boom",
			},
			{
				Seq: 2, StartedAt: now.Add(time.Second), Alias: "fast", Provider: "openrouter",
				Model: "gpt-5-mini", Latency: 900 * time.Millisecond,
				Status: "ok", Fallback: true,
			},
		},
	}
	if err := store.Traces().Record(context.Background(), trace); err != nil {
		t.Fatalf("Record: %v", err)
	}

	rec := adminRequest(t, h, http.MethodGet, "/api/v1/requests/req-1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	body := decodeJSON[requestTraceResponse](t, rec)
	if body.RequestID != "req-1" {
		t.Errorf("request_id = %q, want req-1", body.RequestID)
	}
	if body.TotalLatency != 1500 {
		t.Errorf("total_latency_ms = %d, want 1500", body.TotalLatency)
	}
	if len(body.Attempts) != 2 {
		t.Fatalf("len(attempts) = %d, want 2", len(body.Attempts))
	}
	if body.Attempts[0].Error != "boom" {
		t.Errorf("attempt 1 error = %q, want boom", body.Attempts[0].Error)
	}
	if !body.Attempts[1].Fallback {
		t.Error("attempt 2 fallback = false, want true")
	}
}

func TestGetRequestMissingIsNotFound(t *testing.T) {
	h, _, _ := newAdminServer(t)

	rec := adminRequest(t, h, http.MethodGet, "/api/v1/requests/absent", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// Every gateway request gets an id it can be traced by, echoed to the caller.
func TestGatewayRequestCarriesRequestID(t *testing.T) {
	h, _, _ := newAdminServer(t)

	rec := adminRequest(t, h, http.MethodGet, "/v1/models", "")
	id := rec.Header().Get("X-Request-Id")
	if id == "" {
		t.Fatal("X-Request-Id header is missing from a gateway response")
	}
	if len(id) != requestIDBytes*2 {
		t.Errorf("request id %q has length %d, want %d hex chars", id, len(id), requestIDBytes*2)
	}

	second := adminRequest(t, h, http.MethodGet, "/v1/models", "")
	if second.Header().Get("X-Request-Id") == id {
		t.Error("two requests shared one id, want a unique id per request")
	}
}
