package router

import (
	"slices"
	"sync"
	"time"
)

// statsWindow is how many recent requests are kept per alias. The window is
// deliberately small: these numbers describe current behaviour, and a longer
// tail would only make a provider's recovery invisible.
const statsWindow = 200

// minConfidentSamples is the sample count below which percentiles are too
// noisy to act on. They are still reported, flagged as low confidence.
const minConfidentSamples = 10

// RequestMetric is one measured routed request.
type RequestMetric struct {
	Alias     string
	Success   bool
	TTFBMs    int64
	LatencyMs int64
	Timestamp time.Time
}

// Confidence reports whether a window holds enough samples for its tail
// percentile to mean anything.
type Confidence string

// Reported confidence levels.
const (
	ConfidenceLow  Confidence = "low"
	ConfidenceHigh Confidence = "high"
)

// AliasStats is the aggregate of an alias's rolling window.
type AliasStats struct {
	Alias string
	// TTFBMs is the median time to first byte (first streamed token for a
	// stream), over the samples that produced one.
	TTFBMs int64
	// P50Ms is the median total latency over the whole window, failures
	// included: a timeout is part of what callers experience.
	P50Ms int64
	// P95Ms is the 95th-percentile latency, or nil when the window is too
	// small for a tail estimate. With a handful of samples the 95th percentile
	// is just the slowest one, which reads as a tail but is noise.
	P95Ms *int64
	// SuccessRate is a percentage in [0, 100].
	SuccessRate float64
	Requests    int
	Confidence  Confidence
	UpdatedAt   time.Time
}

// ring is a fixed-capacity buffer of the latest samples for one alias,
// overwriting the oldest entry once full.
type ring struct {
	mu      sync.Mutex
	samples []RequestMetric
	next    int
	full    bool
}

func newRing(capacity int) *ring {
	return &ring{samples: make([]RequestMetric, capacity)}
}

func (r *ring) add(m RequestMetric) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.samples[r.next] = m
	r.next = (r.next + 1) % len(r.samples)
	if r.next == 0 {
		r.full = true
	}
}

// snapshot copies the live samples, oldest first.
func (r *ring) snapshot() []RequestMetric {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.full {
		return slices.Clone(r.samples[:r.next])
	}
	out := make([]RequestMetric, 0, len(r.samples))
	out = append(out, r.samples[r.next:]...)
	return append(out, r.samples[:r.next]...)
}

// Stats aggregates per-alias request measurements over a rolling window. It is
// in-memory only: the durable usage log answers historical questions, these
// numbers answer "how is this route behaving right now".
type Stats struct {
	mu      sync.RWMutex
	windows map[string]*ring
}

// NewStats returns an empty collector.
func NewStats() *Stats {
	return &Stats{windows: make(map[string]*ring)}
}

// Record appends one measurement to the alias's window.
func (s *Stats) Record(m RequestMetric) {
	if m.Alias == "" {
		return
	}
	if m.Timestamp.IsZero() {
		m.Timestamp = time.Now()
	}
	s.window(m.Alias).add(m)
}

// window returns the alias's ring, creating it on first use.
func (s *Stats) window(alias string) *ring {
	s.mu.RLock()
	r, ok := s.windows[alias]
	s.mu.RUnlock()
	if ok {
		return r
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.windows[alias]; ok {
		return r
	}
	r = newRing(statsWindow)
	s.windows[alias] = r
	return r
}

// Alias returns the aggregate for one alias. The second result is false when
// the alias has never been measured.
func (s *Stats) Alias(alias string) (AliasStats, bool) {
	s.mu.RLock()
	r, ok := s.windows[alias]
	s.mu.RUnlock()
	if !ok {
		return AliasStats{Alias: alias}, false
	}

	samples := r.snapshot()
	if len(samples) == 0 {
		return AliasStats{Alias: alias}, false
	}
	return aggregate(alias, samples), true
}

// aggregate reduces a window to its reported statistics.
func aggregate(alias string, samples []RequestMetric) AliasStats {
	latencies, ttfbs, succeeded, updated := split(samples)

	slices.Sort(latencies)
	slices.Sort(ttfbs)

	return AliasStats{
		Alias:       alias,
		TTFBMs:      median(ttfbs),
		P50Ms:       median(latencies),
		P95Ms:       tail(latencies),
		SuccessRate: successRate(succeeded, len(samples)),
		Requests:    len(samples),
		Confidence:  confidenceOf(len(samples)),
		UpdatedAt:   updated,
	}
}

// Summary pools the given aliases' windows into one aggregate. It reduces over
// every sample rather than averaging per-alias figures, so a low-traffic alias
// cannot weigh as much as one serving the bulk of the requests. Aliases with
// no window are skipped.
func (s *Stats) Summary(aliases []string) (AliasStats, bool) {
	var pooled []RequestMetric
	for _, alias := range aliases {
		s.mu.RLock()
		r, ok := s.windows[alias]
		s.mu.RUnlock()
		if !ok {
			continue
		}
		pooled = append(pooled, r.snapshot()...)
	}
	if len(pooled) == 0 {
		return AliasStats{}, false
	}
	return aggregate("", pooled), true
}

// split separates a window into the series each statistic needs.
func split(samples []RequestMetric) (latencies, ttfbs []int64, succeeded int, updated time.Time) {
	latencies = make([]int64, 0, len(samples))
	ttfbs = make([]int64, 0, len(samples))

	for _, m := range samples {
		latencies = append(latencies, m.LatencyMs)
		// A request that failed before any byte arrived has no TTFB to report;
		// including a zero would drag the median toward a latency nobody saw.
		if m.TTFBMs > 0 {
			ttfbs = append(ttfbs, m.TTFBMs)
		}
		if m.Success {
			succeeded++
		}
		if m.Timestamp.After(updated) {
			updated = m.Timestamp
		}
	}
	return latencies, ttfbs, succeeded, updated
}

// tail returns the 95th-percentile latency, or nil when there are too few
// samples for it to describe anything but the slowest single request.
func tail(sortedLatencies []int64) *int64 {
	if len(sortedLatencies) < minConfidentSamples {
		return nil
	}
	p95 := percentile(sortedLatencies, 95)
	return &p95
}

func successRate(succeeded, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(succeeded) / float64(total) * 100
}

func confidenceOf(samples int) Confidence {
	if samples < minConfidentSamples {
		return ConfidenceLow
	}
	return ConfidenceHigh
}
