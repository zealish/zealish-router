package router

import (
	"context"
	"net/http"
	"testing"

	"github.com/zealish/zealish-router/internal/provider"
	"github.com/zealish/zealish-router/pkg/openai"
)

func TestDispatchRecordsSuccess(t *testing.T) {
	e := newTestEngine(t, newRecorder(), &fakeProvider{name: "openai"})

	if _, err := e.ChatCompletion(context.Background(), &openai.ChatCompletionRequest{Model: "gpt-5"}); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	stats, ok := e.AliasStats("gpt-5")
	if !ok {
		t.Fatal("AliasStats: alias not measured")
	}
	if stats.Requests != 1 {
		t.Errorf("Requests = %d, want 1", stats.Requests)
	}
	if stats.SuccessRate != 100 {
		t.Errorf("SuccessRate = %v, want 100", stats.SuccessRate)
	}
	if stats.Confidence != ConfidenceLow {
		t.Errorf("Confidence = %q for a single sample, want low", stats.Confidence)
	}
	if stats.P95Ms != nil {
		t.Errorf("P95Ms = %d, want nil for a single sample", *stats.P95Ms)
	}
}

// Every route the chain touches is measured under its own alias, so a fallback
// that carried the request is credited and the failing one is not hidden.
func TestDispatchRecordsEachRouteInChain(t *testing.T) {
	failing := &fakeProvider{name: "openai", results: []error{upstreamErr(http.StatusInternalServerError, provider.ErrUpstream5xx)}}
	e := newTestEngine(t, newRecorder(), failing, &fakeProvider{name: "openrouter"})

	if _, err := e.ChatCompletion(context.Background(), &openai.ChatCompletionRequest{Model: "gpt-5"}); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	primary, ok := e.AliasStats("gpt-5")
	if !ok {
		t.Fatal("AliasStats(gpt-5): not measured")
	}
	if primary.SuccessRate != 0 {
		t.Errorf("gpt-5 SuccessRate = %v, want 0", primary.SuccessRate)
	}

	fallback, ok := e.AliasStats("fast")
	if !ok {
		t.Fatal("AliasStats(fast): not measured")
	}
	if fallback.SuccessRate != 100 {
		t.Errorf("fast SuccessRate = %v, want 100", fallback.SuccessRate)
	}
}

// A stream reports once it drains, not when the channel is handed back.
func TestStreamRecordsAfterDrain(t *testing.T) {
	e := newTestEngine(t, newRecorder(), &fakeProvider{name: "openai"})

	chunks, err := e.ChatCompletionStream(context.Background(), &openai.ChatCompletionRequest{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("ChatCompletionStream: %v", err)
	}
	for range chunks { //nolint:revive // draining the stream is the point
	}

	stats, ok := e.AliasStats("gpt-5")
	if !ok {
		t.Fatal("AliasStats: stream not measured")
	}
	if stats.Requests != 1 {
		t.Errorf("Requests = %d, want 1", stats.Requests)
	}
	if stats.SuccessRate != 100 {
		t.Errorf("SuccessRate = %v, want 100", stats.SuccessRate)
	}
}

func TestUnmeasuredAliasHasNoStats(t *testing.T) {
	e := newTestEngine(t, newRecorder(), &fakeProvider{name: "openai"})

	if _, ok := e.AliasStats("local"); ok {
		t.Error("AliasStats: untouched alias reported as measured")
	}
}
