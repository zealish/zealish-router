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

func TestRequestPreservesUnmodelledFields(t *testing.T) {
	raw := `{"model":"gpt-5","messages":[],` +
		`"tools":[{"type":"function","function":{"name":"read_file"}}],` +
		`"tool_choice":"auto","parallel_tool_calls":false,` +
		`"response_format":{"type":"json_object"}}`

	var req ChatCompletionRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	out, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{`"read_file"`, `"tool_choice":"auto"`, `"parallel_tool_calls":false`, `"type":"json_object"`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("dropped %s from request: %s", want, out)
		}
	}
}

func TestDeclaredFieldsWinOverExtras(t *testing.T) {
	var req ChatCompletionRequest
	if err := json.Unmarshal([]byte(`{"model":"alias","stream":true}`), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// The router rewrites the alias to the upstream model name; the extras
	// merge must never resurrect the original value.
	req.Model = "gpt-5"

	out, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(out), `"alias"`) {
		t.Fatalf("extras shadowed a rewritten field: %s", out)
	}
}

func TestStreamChunkPreservesToolCallDeltas(t *testing.T) {
	raw := `{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,` +
		`"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1",` +
		`"function":{"name":"read_file","arguments":"{\"p\":1}"}}]},` +
		`"finish_reason":null,"logprobs":null}],"system_fingerprint":"fp_1"}`

	var chunk StreamChunk
	if err := json.Unmarshal([]byte(raw), &chunk); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	out, err := json.Marshal(chunk)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{`"tool_calls"`, `"call_1"`, `"read_file"`, `"system_fingerprint":"fp_1"`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("dropped %s from chunk: %s", want, out)
		}
	}
}
