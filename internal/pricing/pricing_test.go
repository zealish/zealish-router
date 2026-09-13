package pricing

import (
	"math"
	"testing"
)

func TestForMatchesVendorPrefixedAlias(t *testing.T) {
	// Aliases carry a vendor prefix the pricing table does not use.
	rate, ok := For("wz/gemini-3.7-flash")
	if !ok {
		t.Fatal("wz/gemini-3.7-flash has no rate")
	}
	if rate.Input <= 0 || rate.Output <= 0 {
		t.Errorf("rate = %+v, want non-zero input and output", rate)
	}
}

func TestForFallsBackToFamilyPattern(t *testing.T) {
	// Not an exact entry; must resolve through the "gemini-*-flash" pattern.
	if _, ok := For("wz/gemini-3.8-flash"); !ok {
		t.Error("wz/gemini-3.8-flash did not match a family pattern")
	}
	if _, ok := For("wz/deepseek-v4.1-flash"); !ok {
		t.Error("wz/deepseek-v4.1-flash did not match a family pattern")
	}
}

func TestForUnknownModelHasNoRate(t *testing.T) {
	if _, ok := For("totally-made-up-model-xyz"); ok {
		t.Error("unknown model resolved to a rate")
	}
	if got := Cost("totally-made-up-model-xyz", Tokens{Prompt: 1000, Completion: 1000}); got != 0 {
		t.Errorf("Cost = %v, want 0 for an unpriced model", got)
	}
}

func TestCostSplitsCachedFromFreshPromptTokens(t *testing.T) {
	rate := Rate{Input: 10, Output: 30, Cached: 1}

	// 1000 prompt tokens, 400 of them cached: 600 fresh at $10/M plus 400
	// cached at $1/M, and 100 completion tokens at $30/M.
	got := rate.Cost(Tokens{Prompt: 1000, Completion: 100, Cached: 400})
	want := 600*10.0/1e6 + 400*1.0/1e6 + 100*30.0/1e6

	if math.Abs(got-want) > 1e-12 {
		t.Errorf("Cost = %v, want %v", got, want)
	}
}

func TestCostFallsBackToInputRateWhenCachedUnpriced(t *testing.T) {
	rate := Rate{Input: 10, Output: 30}

	got := rate.Cost(Tokens{Prompt: 1000, Cached: 400})
	want := 1000 * 10.0 / 1e6

	if math.Abs(got-want) > 1e-12 {
		t.Errorf("Cost = %v, want %v (cached billed at input rate)", got, want)
	}
}

func TestCostChargesReasoningDeltaOnly(t *testing.T) {
	rate := Rate{Input: 10, Output: 30, Reasoning: 50}

	// Reasoning tokens are already inside the completion count, so only the
	// $20/M premium applies on top of the 500 completion tokens.
	got := rate.Cost(Tokens{Completion: 500, Reasoning: 200})
	want := 500*30.0/1e6 + 200*20.0/1e6

	if math.Abs(got-want) > 1e-12 {
		t.Errorf("Cost = %v, want %v", got, want)
	}
}

func TestCostNeverChargesNegativeFreshTokens(t *testing.T) {
	rate := Rate{Input: 10, Output: 30, Cached: 1}

	// A provider reporting more cached than prompt tokens must not produce a
	// negative fresh-token charge.
	got := rate.Cost(Tokens{Prompt: 100, Cached: 400})
	if got < 0 {
		t.Errorf("Cost = %v, want >= 0", got)
	}
}
