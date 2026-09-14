package router

import "math"

// percentile returns the p-th percentile (0–100) of values, which must already
// be sorted ascending. It uses the nearest-rank definition: the smallest value
// at or below which at least p percent of the samples fall. That keeps the
// result an observed measurement rather than an interpolation between two
// requests that never happened.
func percentile(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 100 {
		return sorted[len(sorted)-1]
	}
	rank := int(math.Ceil(p / 100 * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	return sorted[rank-1]
}

// median returns the middle of sorted, averaging the two central values when
// the sample count is even. Unlike percentile it is not a nearest-rank pick:
// the median is the conventional measure of central tendency, and rounding it
// down to an observed sample would bias every even-sized window low.
func median(sorted []int64) int64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}
