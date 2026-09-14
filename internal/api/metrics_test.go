package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/zealish/zealish-router/internal/router"
)

func TestAdminProviderMetricsEmptyUntilMeasured(t *testing.T) {
	h, _, _ := newAdminServer(t)

	adminRequest(t, h, http.MethodPut, "/api/v1/providers/acme",
		`{"base_url":"http://localhost:1","timeout_ms":5000,"enabled":true}`)
	adminRequest(t, h, http.MethodPut, "/api/v1/models/acme%2Fgpt-5",
		`{"provider":"acme","model":"gpt-5"}`)

	rec := adminRequest(t, h, http.MethodGet, "/api/v1/providers/acme/metrics", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%q)", rec.Code, rec.Body.String())
	}
	got := decodeJSON[providerMetricsResponse](t, rec)
	if len(got.Aliases) != 0 {
		t.Errorf("aliases = %+v, want none before any request is measured", got.Aliases)
	}
}

func TestAdminProviderMetricsReportsWindow(t *testing.T) {
	h, _, engine := newAdminServer(t)

	adminRequest(t, h, http.MethodPut, "/api/v1/providers/acme",
		`{"base_url":"http://localhost:1","timeout_ms":5000,"enabled":true}`)
	adminRequest(t, h, http.MethodPut, "/api/v1/models/acme%2Fgpt-5",
		`{"provider":"acme","model":"gpt-5"}`)
	adminRequest(t, h, http.MethodPut, "/api/v1/providers/other",
		`{"base_url":"http://localhost:2","timeout_ms":5000,"enabled":true}`)
	adminRequest(t, h, http.MethodPut, "/api/v1/models/other%2Fgpt-5",
		`{"provider":"other","model":"gpt-5"}`)

	for i := range 20 {
		engine.Stats().Record(router.RequestMetric{
			Alias:     "acme/gpt-5",
			Success:   i != 0,
			TTFBMs:    300,
			LatencyMs: int64(100 * (i + 1)),
		})
	}
	engine.Stats().Record(router.RequestMetric{Alias: "other/gpt-5", Success: true, LatencyMs: 50})

	got := decodeJSON[providerMetricsResponse](t,
		adminRequest(t, h, http.MethodGet, "/api/v1/providers/acme/metrics", ""))
	if len(got.Aliases) != 1 {
		t.Fatalf("aliases = %+v, want only the aliases of this provider", got.Aliases)
	}

	row := got.Aliases[0]
	if row.Alias != "acme/gpt-5" {
		t.Errorf("alias = %q, want acme/gpt-5", row.Alias)
	}
	if row.Requests != 20 {
		t.Errorf("requests = %d, want 20", row.Requests)
	}
	if row.SuccessRate != 95 {
		t.Errorf("success_rate = %v, want 95", row.SuccessRate)
	}
	if row.TTFBMS != 300 {
		t.Errorf("ttfb_ms = %d, want 300", row.TTFBMS)
	}
	if row.P50MS != 1050 {
		t.Errorf("p50_ms = %d, want 1050", row.P50MS)
	}
	if row.P95MS == nil || *row.P95MS != 1900 {
		t.Errorf("p95_ms = %v, want 1900", row.P95MS)
	}
	if row.Confidence != router.ConfidenceHigh {
		t.Errorf("confidence = %q for 20 samples, want high", row.Confidence)
	}

	// The summary covers this provider only, and pools its samples.
	if got.Summary == nil {
		t.Fatal("summary = nil, want the provider aggregate")
	}
	if got.Summary.Requests != 20 {
		t.Errorf("summary.requests = %d, want 20: other providers are excluded", got.Summary.Requests)
	}
}

// A window under the threshold reports every statistic except the tail, which
// serialises as null rather than a misleading number.
func TestAdminProviderMetricsWithholdsTailOnLowSample(t *testing.T) {
	h, _, engine := newAdminServer(t)

	adminRequest(t, h, http.MethodPut, "/api/v1/providers/acme",
		`{"base_url":"http://localhost:1","timeout_ms":5000,"enabled":true}`)
	adminRequest(t, h, http.MethodPut, "/api/v1/models/gpt-5",
		`{"provider":"acme","model":"gpt-5"}`)

	for range 3 {
		engine.Stats().Record(router.RequestMetric{Alias: "gpt-5", Success: true, TTFBMs: 312, LatencyMs: 842})
	}

	rec := adminRequest(t, h, http.MethodGet, "/api/v1/providers/acme/metrics", "")
	if !strings.Contains(rec.Body.String(), `"p95_ms":null`) {
		t.Errorf("body = %s, want a null p95_ms", rec.Body.String())
	}

	got := decodeJSON[providerMetricsResponse](t, rec)
	row := got.Aliases[0]
	if row.P95MS != nil {
		t.Errorf("p95_ms = %d, want null below the threshold", *row.P95MS)
	}
	if row.Confidence != router.ConfidenceLow {
		t.Errorf("confidence = %q, want low", row.Confidence)
	}
	if row.P50MS != 842 || row.TTFBMS != 312 || row.Requests != 3 {
		t.Errorf("row = %+v, want median, ttfb and count still reported", row)
	}
}

// A probe is a real request: it joins the window rather than replacing it.
func TestAdminTestAliasAppendsToWindow(t *testing.T) {
	h, _, engine := newAdminServer(t)

	adminRequest(t, h, http.MethodPut, "/api/v1/providers/acme",
		`{"base_url":"http://127.0.0.1:1","timeout_ms":1000,"enabled":true}`)
	adminRequest(t, h, http.MethodPut, "/api/v1/models/gpt-5",
		`{"provider":"acme","model":"gpt-5"}`)

	engine.Stats().Record(router.RequestMetric{Alias: "gpt-5", Success: true, TTFBMs: 100, LatencyMs: 200})

	// The upstream is unreachable, so the probe fails and must count against
	// the success rate rather than be dropped.
	adminRequest(t, h, http.MethodPost, "/api/v1/models/gpt-5/test", `{}`)

	stats, ok := engine.AliasStats("gpt-5")
	if !ok {
		t.Fatal("AliasStats: window missing")
	}
	if stats.Requests != 2 {
		t.Errorf("Requests = %d, want 2: the probe appends to history", stats.Requests)
	}
	if stats.SuccessRate != 50 {
		t.Errorf("SuccessRate = %v, want 50: a failed probe counts", stats.SuccessRate)
	}
}
