package openai

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMessageTextAcceptsStringContent(t *testing.T) {
	var m Message
	if err := json.Unmarshal([]byte(`{"role":"user","content":"hello"}`), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got, ok := m.Text()
	if !ok || got != "hello" {
		t.Fatalf("Text() = %q, %v; want \"hello\", true", got, ok)
	}
}

func TestMessageTextRejectsPartsContent(t *testing.T) {
	raw := `{"role":"user","content":[{"type":"text","text":"hello"}]}`

	var m Message
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := m.Text(); ok {
		t.Error("array-of-parts content must not decode as a string")
	}

	// Array content must survive a round trip untouched so it reaches upstream.
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(out), `"type":"text"`) {
		t.Fatalf("parts content lost in round trip: %s", out)
	}
}

func TestStreamOptionsRoundTrip(t *testing.T) {
	var req ChatCompletionRequest
	raw := `{"model":"gpt-5","stream":true,"stream_options":{"include_usage":true}}`
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.StreamOptions == nil || !req.StreamOptions.IncludeUsage {
		t.Fatalf("stream_options not decoded: %+v", req.StreamOptions)
	}

	out, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(out), `"stream_options":{"include_usage":true}`) {
		t.Fatalf("stream_options not passed through: %s", out)
	}
}

func TestStreamOptionsOmittedWhenAbsent(t *testing.T) {
	out, err := json.Marshal(ChatCompletionRequest{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(out), "stream_options") {
		t.Fatalf("stream_options must be omitted when unset: %s", out)
	}
}
