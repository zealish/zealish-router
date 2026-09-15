package router

import (
	"slices"

	"github.com/zealish/zealish-router/internal/storage"
)

// Member ranks for the intelligent strategy. A rank always outweighs a score:
// no amount of historical speed makes a provider that is currently down worth
// trying before one that is up.
const (
	// rankReady is a provider with a closed circuit that passed its last probe.
	rankReady = iota
	// rankProbing is a provider admitting a recovery probe: worth trying, but
	// only after every member known to be up.
	rankProbing
	// rankDown is an open circuit or a failed health probe. Such a member is
	// skipped by dispatch anyway; it stays in the chain as a last resort.
	rankDown
)

// intelligentBaselineMs anchors the latency term: a member answering at the
// baseline scores half of it, one twice as slow a third. It is deliberately
// generous — the point is to separate a healthy route from a struggling one,
// not to chase tens of milliseconds.
const intelligentBaselineMs = 2000

// unknownScore is what a member with no usable window scores. It is optimistic
// on purpose — above anything a measured member can reach — so a route the
// router has never tried is explored rather than starved by whichever member
// happened to answer first.
const unknownScore = 1.0

// neutralScore is where a thin window is pulled towards: real evidence, but
// too little of it to demote or promote a route on its own.
const neutralScore = 0.75

// intelligentOrder ranks a pool by how it is behaving right now: provider
// health first, then success rate and latency over the rolling window. Members
// that score the same keep the rotation order, so equally good routes still
// share the traffic.
func (rt *routes) intelligentOrder(combo storage.Combo, tick uint64) []string {
	ordered := rotate(combo.Members, int(tick%uint64(len(combo.Members))))
	if rt.engine == nil {
		return ordered
	}

	type ranked struct {
		alias string
		rank  int
		score float64
	}

	pool := make([]ranked, 0, len(ordered))
	for _, alias := range ordered {
		entry := ranked{alias: alias, rank: rankDown, score: unknownScore}
		if m, ok := rt.models[alias]; ok {
			entry.rank = rt.engine.rankOf(m.Provider)
			entry.score = rt.engine.scoreOf(alias)
		}
		pool = append(pool, entry)
	}

	slices.SortStableFunc(pool, func(a, b ranked) int {
		if a.rank != b.rank {
			return a.rank - b.rank
		}
		// Higher score first.
		switch {
		case a.score > b.score:
			return -1
		case a.score < b.score:
			return 1
		default:
			return 0
		}
	})

	out := make([]string, 0, len(pool))
	for _, entry := range pool {
		out = append(out, entry.alias)
	}
	return out
}

// rankOf classifies a provider by current availability.
func (e *Engine) rankOf(providerName string) int {
	if !e.probes.healthy(providerName) {
		return rankDown
	}
	switch e.breakers.phase(providerName) {
	case CircuitClosed:
		return rankReady
	case CircuitHalfOpen:
		return rankProbing
	default:
		return rankDown
	}
}

// scoreOf reduces an alias's rolling window to a number in [0, 1]: mostly how
// often it answers, partly how fast. An unmeasured alias scores as unknown, and
// a window too thin to trust is pulled towards neutral, so one unlucky request
// cannot demote a route for long.
func (e *Engine) scoreOf(alias string) float64 {
	stats, ok := e.stats.Alias(alias)
	if !ok || stats.Requests == 0 {
		return unknownScore
	}

	latency := float64(intelligentBaselineMs) / float64(intelligentBaselineMs+stats.P50Ms)
	score := 0.7*(stats.SuccessRate/100) + 0.3*latency
	if stats.Confidence == ConfidenceLow {
		// Blend towards neutral: the window is real but thin, so it nudges the
		// order instead of dictating it.
		score = (score + neutralScore) / 2
	}
	return score
}
