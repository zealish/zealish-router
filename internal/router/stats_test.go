package router

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestPercentileNearestRank(t *testing.T) {
	sorted := []int64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}

	cases := []struct {
		p    float64
		want int64
	}{
		{0, 10},
		{10, 10},
		{50, 50},
		{95, 100},
		{99, 100},
		{100, 100},
	}
	for _, c := range cases {
		if got := percentile(sorted, c.p); got != c.want {
			t.Errorf("percentile(p=%v) = %d, want %d", c.p, got, c.want)
		}
	}
}

func TestPercentileEmpty(t *testing.T) {
	if got := percentile(nil, 95); got != 0 {
		t.Errorf("percentile(nil) = %d, want 0", got)
	}
}

func TestPercentileSingleSample(t *testing.T) {
	sorted := []int64{42}
	for _, p := range []float64{0, 50, 95, 100} {
		if got := percentile(sorted, p); got != 42 {
			t.Errorf("percentile(p=%v) = %d, want 42", p, got)
		}
	}
}

// P95 over 100 samples must land on the 95th, not the last: a single outlier
// must not become the reported tail.
func TestPercentileP95IgnoresTopOutliers(t *testing.T) {
	sorted := make([]int64, 100)
	for i := range sorted {
		sorted[i] = int64(i + 1)
	}
	sorted[99] = 10_000

	if got := percentile(sorted, 95); got != 95 {
		t.Errorf("p95 = %d, want 95", got)
	}
}

func TestMedianOddDataset(t *testing.T) {
	if got := median([]int64{10, 20, 30}); got != 20 {
		t.Errorf("median = %d, want 20", got)
	}
}

// An even window has no middle sample, so the median is the mean of the two
// central values rather than an arbitrary pick.
func TestMedianEvenDataset(t *testing.T) {
	if got := median([]int64{10, 20, 30, 40}); got != 25 {
		t.Errorf("median = %d, want 25", got)
	}
}

func TestMedianEmpty(t *testing.T) {
	if got := median(nil); got != 0 {
		t.Errorf("median(nil) = %d, want 0", got)
	}
}

// P95 is index ceil(0.95*N)-1 of the sorted latencies.
func TestTailIndexing(t *testing.T) {
	cases := []struct {
		n    int
		want int64
	}{
		{10, 10},  // ceil(9.5)-1  = 9  -> 10th value
		{20, 19},  // ceil(19)-1   = 18 -> 19th value
		{21, 20},  // ceil(19.95)-1= 19 -> 20th value
		{100, 95}, // ceil(95)-1   = 94 -> 95th value
	}
	for _, c := range cases {
		sorted := make([]int64, c.n)
		for i := range sorted {
			sorted[i] = int64(i + 1)
		}
		got := tail(sorted)
		if got == nil {
			t.Fatalf("tail(n=%d) = nil, want %d", c.n, c.want)
		}
		if *got != c.want {
			t.Errorf("tail(n=%d) = %d, want %d", c.n, *got, c.want)
		}
	}
}

// Below the threshold the 95th percentile is just the slowest sample, so it is
// withheld rather than reported as a tail.
func TestTailWithheldBelowThreshold(t *testing.T) {
	for n := range minConfidentSamples {
		sorted := make([]int64, n)
		for i := range sorted {
			sorted[i] = int64(i + 1)
		}
		if got := tail(sorted); got != nil {
			t.Errorf("tail(n=%d) = %d, want nil", n, *got)
		}
	}
}

func TestSuccessRate(t *testing.T) {
	cases := []struct {
		succeeded, total int
		want             float64
	}{
		{0, 0, 0},
		{10, 10, 100},
		{19, 20, 95},
		{0, 4, 0},
	}
	for _, c := range cases {
		if got := successRate(c.succeeded, c.total); got != c.want {
			t.Errorf("successRate(%d, %d) = %v, want %v", c.succeeded, c.total, got, c.want)
		}
	}
}

func TestStatsAggregate(t *testing.T) {
	s := NewStats()
	for i := 1; i <= 20; i++ {
		s.Record(RequestMetric{
			Alias:     "node-1/sonnet",
			Success:   i != 20, // one failure in twenty
			TTFBMs:    int64(i * 10),
			LatencyMs: int64(i * 100),
		})
	}

	got, ok := s.Alias("node-1/sonnet")
	if !ok {
		t.Fatal("Alias: not found")
	}
	if got.Requests != 20 {
		t.Errorf("Requests = %d, want 20", got.Requests)
	}
	if got.SuccessRate != 95 {
		t.Errorf("SuccessRate = %v, want 95", got.SuccessRate)
	}
	if got.P50Ms != 1050 {
		t.Errorf("P50Ms = %d, want 1050 (mean of the two central samples)", got.P50Ms)
	}
	if got.P95Ms == nil || *got.P95Ms != 1900 {
		t.Errorf("P95Ms = %v, want 1900", got.P95Ms)
	}
	if got.TTFBMs != 105 {
		t.Errorf("TTFBMs = %d, want 105", got.TTFBMs)
	}
	if got.Confidence != ConfidenceHigh {
		t.Errorf("Confidence = %q, want high", got.Confidence)
	}
}

// A handful of requests still reports TTFB, median and success rate; only the
// tail is withheld.
func TestStatsLowConfidence(t *testing.T) {
	s := NewStats()
	for range minConfidentSamples - 1 {
		s.Record(RequestMetric{Alias: "a", Success: true, TTFBMs: 50, LatencyMs: 100})
	}

	got, _ := s.Alias("a")
	if got.Confidence != ConfidenceLow {
		t.Errorf("Confidence = %q for %d samples, want low", got.Confidence, got.Requests)
	}
	if got.P95Ms != nil {
		t.Errorf("P95Ms = %d, want nil below the threshold", *got.P95Ms)
	}
	if got.P50Ms != 100 || got.TTFBMs != 50 {
		t.Errorf("P50/TTFB = %d/%d, want 100/50: only the tail is withheld", got.P50Ms, got.TTFBMs)
	}
}

// The window is bounded: only the newest statsWindow samples are reported, so
// an alias that has recovered stops being judged by its old failures.
func TestStatsRollingWindowEviction(t *testing.T) {
	s := NewStats()
	for range statsWindow {
		s.Record(RequestMetric{Alias: "a", Success: false, TTFBMs: 900, LatencyMs: 5000})
	}
	for range statsWindow {
		s.Record(RequestMetric{Alias: "a", Success: true, TTFBMs: 10, LatencyMs: 100})
	}

	got, _ := s.Alias("a")
	if got.Requests != statsWindow {
		t.Errorf("Requests = %d, want %d: the window is capped", got.Requests, statsWindow)
	}
	if got.SuccessRate != 100 {
		t.Errorf("SuccessRate = %v, want 100: evicted failures must not linger", got.SuccessRate)
	}
	if got.P95Ms == nil || *got.P95Ms != 100 {
		t.Errorf("P95Ms = %v, want 100", got.P95Ms)
	}
	if got.TTFBMs != 10 {
		t.Errorf("TTFBMs = %d, want 10", got.TTFBMs)
	}
}

// Eviction is strictly oldest-first, so a partially replaced window mixes the
// surviving old samples with the new ones.
func TestStatsEvictsOldestFirst(t *testing.T) {
	s := NewStats()
	for range statsWindow {
		s.Record(RequestMetric{Alias: "a", Success: false, LatencyMs: 5000})
	}
	for range statsWindow / 2 {
		s.Record(RequestMetric{Alias: "a", Success: true, LatencyMs: 100})
	}

	got, _ := s.Alias("a")
	if got.Requests != statsWindow {
		t.Fatalf("Requests = %d, want %d", got.Requests, statsWindow)
	}
	if got.SuccessRate != 50 {
		t.Errorf("SuccessRate = %v, want 50: half the window was replaced", got.SuccessRate)
	}
}

// The summary pools every sample rather than averaging per-alias figures, so a
// low-traffic alias cannot outweigh one carrying the load.
func TestStatsSummaryPoolsAllRequests(t *testing.T) {
	s := NewStats()
	for range 99 {
		s.Record(RequestMetric{Alias: "busy", Success: true, TTFBMs: 100, LatencyMs: 100})
	}
	s.Record(RequestMetric{Alias: "quiet", Success: false, TTFBMs: 9000, LatencyMs: 9000})

	got, ok := s.Summary([]string{"busy", "quiet"})
	if !ok {
		t.Fatal("Summary: no windows pooled")
	}
	if got.Requests != 100 {
		t.Errorf("Requests = %d, want 100", got.Requests)
	}
	if got.SuccessRate != 99 {
		t.Errorf("SuccessRate = %v, want 99", got.SuccessRate)
	}
	// An unweighted average of the two aliases would give ~4550.
	if got.P50Ms != 100 {
		t.Errorf("P50Ms = %d, want 100: the summary is pooled, not averaged", got.P50Ms)
	}
}

func TestStatsSummarySkipsUnmeasured(t *testing.T) {
	s := NewStats()
	s.Record(RequestMetric{Alias: "a", Success: true, LatencyMs: 10})

	got, ok := s.Summary([]string{"a", "ghost"})
	if !ok || got.Requests != 1 {
		t.Errorf("Summary = %+v (ok=%v), want only the measured alias", got, ok)
	}
	if _, ok := s.Summary([]string{"ghost"}); ok {
		t.Error("Summary: unmeasured aliases reported as a window")
	}
}

// A failure before any response byte has no TTFB; it must not pull the median
// down to zero.
func TestStatsTTFBSkipsZeroSamples(t *testing.T) {
	s := NewStats()
	for range 10 {
		s.Record(RequestMetric{Alias: "a", Success: true, TTFBMs: 300, LatencyMs: 900})
	}
	for range 5 {
		s.Record(RequestMetric{Alias: "a", Success: false, TTFBMs: 0, LatencyMs: 30_000})
	}

	got, _ := s.Alias("a")
	if got.TTFBMs != 300 {
		t.Errorf("TTFBMs = %d, want 300", got.TTFBMs)
	}
	if got.P95Ms == nil || *got.P95Ms != 30_000 {
		t.Errorf("P95Ms = %v, want 30000: failures belong in latency", got.P95Ms)
	}
}

func TestStatsUnknownAlias(t *testing.T) {
	if _, ok := NewStats().Alias("nope"); ok {
		t.Error("Alias: unmeasured alias reported as known")
	}
}

func TestStatsConcurrentRecord(t *testing.T) {
	s := NewStats()

	var wg sync.WaitGroup
	for w := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 100 {
				s.Record(RequestMetric{
					Alias:     fmt.Sprintf("alias-%d", w%3),
					Success:   true,
					LatencyMs: int64(i),
					Timestamp: time.Now(),
				})
			}
		}()
	}
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				s.Alias("alias-0")
			}
		}()
	}
	wg.Wait()

	got, ok := s.Alias("alias-0")
	if !ok || got.Requests == 0 {
		t.Fatal("Alias: no samples after concurrent writes")
	}
}
