// Package metrics owns Prometheus instrumentation. Collectors are registered
// on an injected registry so no global state is used.
package metrics

import (
	"net/http"
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	dto "github.com/prometheus/client_model/go"
)

// Metrics holds every collector exposed by the gateway.
type Metrics struct {
	registry *prometheus.Registry

	RequestsTotal       *prometheus.CounterVec
	RequestDuration     *prometheus.HistogramVec
	ProviderErrorsTotal *prometheus.CounterVec
	StreamConnections   prometheus.Gauge
	TokensTotal         *prometheus.CounterVec
}

// New creates collectors and registers them on a fresh registry.
func New() *Metrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	m := &Metrics{
		registry: reg,
		RequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "router_requests_total",
			Help: "Total number of HTTP requests handled.",
		}, []string{"method", "path", "status"}),
		RequestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "router_request_duration_seconds",
			Help:    "HTTP request duration in seconds.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "path"}),
		ProviderErrorsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "router_provider_errors_total",
			Help: "Total number of upstream provider errors.",
		}, []string{"provider", "reason"}),
		StreamConnections: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "router_stream_connections",
			Help: "Number of active streaming connections.",
		}),
		TokensTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "router_tokens_total",
			Help: "Total number of tokens processed.",
		}, []string{"provider", "model", "kind"}),
	}

	reg.MustRegister(
		m.RequestsTotal,
		m.RequestDuration,
		m.ProviderErrorsTotal,
		m.StreamConnections,
		m.TokensTotal,
	)
	return m
}

// RecordProviderError counts one upstream failure for a provider.
func (m *Metrics) RecordProviderError(provider, reason string) {
	m.ProviderErrorsTotal.WithLabelValues(provider, reason).Inc()
}

// Token accounting kinds, used as the "kind" label of router_tokens_total.
const (
	TokenKindPrompt     = "prompt"
	TokenKindCompletion = "completion"
)

// RecordTokens adds prompt and completion token counts for a completion.
func (m *Metrics) RecordTokens(provider, model string, prompt, completion int) {
	if prompt > 0 {
		m.TokensTotal.WithLabelValues(provider, model, TokenKindPrompt).Add(float64(prompt))
	}
	if completion > 0 {
		m.TokensTotal.WithLabelValues(provider, model, TokenKindCompletion).Add(float64(completion))
	}
}

// Snapshot is an aggregate read of the counters the dashboard overview needs.
type Snapshot struct {
	Requests          float64
	RequestErrors     float64
	ProviderErrors    float64
	StreamConnections float64
}

// Snapshot gathers current counter totals from the registry. Prometheus is the
// authoritative store, so the overview reads it rather than duplicating state.
func (m *Metrics) Snapshot() Snapshot {
	families, err := m.registry.Gather()
	if err != nil {
		return Snapshot{}
	}

	var snap Snapshot
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			switch family.GetName() {
			case "router_requests_total":
				value := metric.GetCounter().GetValue()
				snap.Requests += value
				if isErrorStatus(metric.GetLabel()) {
					snap.RequestErrors += value
				}
			case "router_provider_errors_total":
				snap.ProviderErrors += metric.GetCounter().GetValue()
			case "router_stream_connections":
				snap.StreamConnections += metric.GetGauge().GetValue()
			}
		}
	}
	return snap
}

// isErrorStatus reports whether a router_requests_total sample carries a 4xx
// or 5xx status label.
func isErrorStatus(labels []*dto.LabelPair) bool {
	for _, label := range labels {
		if label.GetName() != "status" {
			continue
		}
		code, err := strconv.Atoi(label.GetValue())
		return err == nil && code >= 400
	}
	return false
}

// Handler returns the HTTP handler exposing the registry.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}
