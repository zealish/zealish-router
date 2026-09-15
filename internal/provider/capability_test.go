package provider

import (
	"slices"
	"testing"
)

func TestNormalizeCapabilities(t *testing.T) {
	// Out of order, duplicated, mixed case, padded, and one unknown entry.
	got := NormalizeCapabilities([]string{"tools", "chat", " VISION ", "chat", "telepathy"})
	want := []string{CapChat, CapVision, CapTools}
	if !slices.Equal(got, want) {
		t.Fatalf("NormalizeCapabilities = %v, want %v", got, want)
	}
}

func TestNormalizeCapabilitiesEmpty(t *testing.T) {
	if got := NormalizeCapabilities(nil); len(got) != 0 {
		t.Fatalf("NormalizeCapabilities(nil) = %v, want empty", got)
	}
}

func TestInferCapabilities(t *testing.T) {
	tests := []struct {
		model string
		want  []string
	}{
		{"gpt-4o", []string{CapChat, CapVision, CapTools, CapStreaming, CapJSONMode}},
		{"gpt-4o-audio-preview", []string{CapChat, CapVision, CapTools, CapStreaming, CapAudio, CapJSONMode}},
		{"text-embedding-3-large", []string{CapEmbeddings}},
		{"whisper-1", []string{CapAudio}},
		{"deepseek-r1", []string{CapChat, CapTools, CapReasoning, CapStreaming, CapJSONMode}},
		// An unknown upstream gets the conservative baseline, never a guess
		// that would advertise a feature the route cannot serve.
		{"some-local-model", []string{CapChat, CapStreaming}},
		{"", nil},
	}

	for _, tc := range tests {
		got := InferCapabilities(tc.model)
		if !slices.Equal(got, tc.want) {
			t.Errorf("InferCapabilities(%q) = %v, want %v", tc.model, got, tc.want)
		}
	}
}

func TestInferCapabilitiesIsCanonicallyOrdered(t *testing.T) {
	got := InferCapabilities("claude-opus-4")
	if !slices.Equal(got, NormalizeCapabilities(got)) {
		t.Fatalf("InferCapabilities = %v, want canonical order", got)
	}
}
