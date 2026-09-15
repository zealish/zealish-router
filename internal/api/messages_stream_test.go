package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/zealish/zealish-router/internal/provider"
	"github.com/zealish/zealish-router/pkg/openai"
)

// sseFrame is one parsed `event:`/`data:` pair of an Anthropic stream.
type sseFrame struct {
	name string
	data string
}

// parseFrames splits a recorded Anthropic SSE body into its frames. Unlike the
// OpenAI dialect every frame is named, and there is no [DONE] sentinel.
func parseFrames(t *testing.T, body string) []sseFrame {
	t.Helper()

	var out []sseFrame
	for _, block := range strings.Split(strings.TrimSpace(body), "\n\n") {
		var frame sseFrame
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				frame.name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				frame.data = strings.TrimPrefix(line, "data: ")
			}
		}
		if frame.name == "" {
			t.Fatalf("frame without an event name: %q", block)
		}
		if !json.Valid([]byte(frame.data)) {
			t.Fatalf("frame %q carries invalid JSON: %q", frame.name, frame.data)
		}
		out = append(out, frame)
	}
	return out
}

func frameNames(frames []sseFrame) []string {
	names := make([]string, len(frames))
	for i, f := range frames {
		names[i] = f.name
	}
	return names
}

// textDelta builds an OpenAI chunk carrying a text fragment.
func textDelta(text string) openai.StreamChunk {
	return openai.StreamChunk{
		ID:     "c1",
		Object: "chat.completion.chunk",
		Model:  "gpt-5-upstream",
		Choices: []openai.Choice{{
			Index: 0,
			Delta: &openai.Message{Role: "assistant", Content: mustMarshal(text)},
		}},
	}
}

func postStream(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := postMessages(t, h, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}
	return rec
}

const streamBody = `{"model":"gpt-5","max_tokens":8,"stream":true,"messages":[{"role":"user","content":"hi"}]}`

func TestMessagesStreamEventSequence(t *testing.T) {
	p := &stubProvider{name: "openai", chunks: []openai.StreamChunk{
		textDelta("Hel"),
		textDelta("lo"),
	}}
	h := newTestServer(t, p)

	rec := postStream(t, h, streamBody)
	frames := parseFrames(t, rec.Body.String())

	want := []string{
		"message_start",
		"content_block_start",
		"content_block_delta",
		"content_block_delta",
		"content_block_stop",
		"message_delta",
		"message_stop",
	}
	if got := frameNames(frames); !slices.Equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}

	// An Anthropic stream has no [DONE] sentinel; message_stop terminates it.
	if strings.Contains(rec.Body.String(), "[DONE]") {
		t.Error("Anthropic stream must not carry the OpenAI sentinel")
	}

	var text strings.Builder
	for _, f := range frames {
		if f.name != "content_block_delta" {
			continue
		}
		var ev struct {
			Index int `json:"index"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(f.data), &ev); err != nil {
			t.Fatalf("decode delta: %v", err)
		}
		if ev.Delta.Type != "text_delta" {
			t.Errorf("delta type = %q, want text_delta", ev.Delta.Type)
		}
		if ev.Index != 0 {
			t.Errorf("index = %d, want every text delta in block 0", ev.Index)
		}
		text.WriteString(ev.Delta.Text)
	}
	if text.String() != "Hello" {
		t.Errorf("reassembled text = %q, want %q", text.String(), "Hello")
	}
}

func TestMessagesStreamToolCallFragmentsReassemble(t *testing.T) {
	p := &stubProvider{name: "openai", chunks: []openai.StreamChunk{
		toolDelta(`{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":""}}`),
		toolDelta(`{"index":0,"function":{"arguments":"{\"city\":"}}`),
		toolDelta(`{"index":0,"function":{"arguments":"\"Jakarta\"}"}}`),
		finishChunk("tool_calls"),
	}}
	h := newTestServer(t, p)

	frames := parseFrames(t, postStream(t, h, streamBody).Body.String())

	var (
		args  strings.Builder
		start struct {
			Index        int `json:"index"`
			ContentBlock struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"content_block"`
		}
		sawStart bool
	)
	for _, f := range frames {
		switch f.name {
		case "content_block_start":
			if err := json.Unmarshal([]byte(f.data), &start); err != nil {
				t.Fatalf("decode start: %v", err)
			}
			sawStart = true
		case "content_block_delta":
			var ev struct {
				Delta struct {
					Type        string `json:"type"`
					PartialJSON string `json:"partial_json"`
				} `json:"delta"`
			}
			if err := json.Unmarshal([]byte(f.data), &ev); err != nil {
				t.Fatalf("decode delta: %v", err)
			}
			if ev.Delta.Type != "input_json_delta" {
				t.Errorf("delta type = %q, want input_json_delta", ev.Delta.Type)
			}
			args.WriteString(ev.Delta.PartialJSON)
		}
	}

	if !sawStart || start.ContentBlock.Type != "tool_use" {
		t.Fatalf("content_block_start = %+v, want a tool_use block", start.ContentBlock)
	}
	if start.ContentBlock.ID != "call_1" || start.ContentBlock.Name != "lookup" {
		t.Errorf("block = %+v, want the call id and name", start.ContentBlock)
	}
	if args.String() != `{"city":"Jakarta"}` {
		t.Errorf("reassembled arguments = %q", args.String())
	}

	if reason := stopReasonOf(t, frames); reason != "tool_use" {
		t.Errorf("stop_reason = %q, want tool_use", reason)
	}
}

func TestMessagesStreamTextAndToolUseOccupySeparateBlocks(t *testing.T) {
	p := &stubProvider{name: "openai", chunks: []openai.StreamChunk{
		textDelta("thinking"),
		toolDelta(`{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}`),
	}}
	h := newTestServer(t, p)

	frames := parseFrames(t, postStream(t, h, streamBody).Body.String())

	var indices []int
	for _, f := range frames {
		if f.name != "content_block_start" {
			continue
		}
		var ev struct {
			Index int `json:"index"`
		}
		if err := json.Unmarshal([]byte(f.data), &ev); err != nil {
			t.Fatalf("decode start: %v", err)
		}
		indices = append(indices, ev.Index)
	}
	// Blocks do not interleave: the text block closes before the tool block
	// opens, and the second block takes the next index.
	if len(indices) != 2 || indices[0] != 0 || indices[1] != 1 {
		t.Fatalf("block indices = %v, want [0 1]", indices)
	}

	var stops int
	for _, f := range frames {
		if f.name == "content_block_stop" {
			stops++
		}
	}
	if stops != 2 {
		t.Errorf("content_block_stop count = %d, want one per block", stops)
	}
}

func TestMessagesStreamReportsUsage(t *testing.T) {
	chunk := finishChunk("stop")
	chunk.Usage = &openai.Usage{PromptTokens: 9, CompletionTokens: 4, TotalTokens: 13}
	p := &stubProvider{name: "openai", chunks: []openai.StreamChunk{textDelta("hi"), chunk}}
	h := newTestServer(t, p)

	frames := parseFrames(t, postStream(t, h, streamBody).Body.String())

	for _, f := range frames {
		if f.name != "message_delta" {
			continue
		}
		var ev struct {
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(f.data), &ev); err != nil {
			t.Fatalf("decode message_delta: %v", err)
		}
		if ev.Usage.InputTokens != 9 || ev.Usage.OutputTokens != 4 {
			t.Errorf("usage = %+v, want 9 input / 4 output", ev.Usage)
		}
		return
	}
	t.Fatal("no message_delta frame carrying usage")
}

func TestMessagesStreamErrorBeforeHeadersUsesStatusCode(t *testing.T) {
	p := &stubProvider{name: "openai", err: &provider.Error{
		Provider: "openai", Status: 503, Kind: provider.ErrUpstream5xx, Message: "down",
	}}
	h := newTestServer(t, p)

	rec := postMessages(t, h, streamBody)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("Content-Type = %q, want JSON before any frame is written", got)
	}
	if env := decodeAnthropicError(t, rec); env.Error.Type != "api_error" {
		t.Errorf("error type = %q, want an Anthropic envelope", env.Error.Type)
	}
}

func TestMessagesStreamBypassesCache(t *testing.T) {
	p := &stubProvider{name: "openai", chunks: []openai.StreamChunk{textDelta("hi")}}
	h := newTestServer(t, p)

	rec := postStream(t, h, streamBody)
	if got := rec.Header().Get(cacheHeader); got != headerPass {
		t.Errorf("%s = %q, want %q", cacheHeader, got, headerPass)
	}
}

// toolDelta builds an OpenAI chunk carrying one tool_calls fragment.
func toolDelta(call string) openai.StreamChunk {
	return openai.StreamChunk{
		ID:     "c1",
		Object: "chat.completion.chunk",
		Model:  "gpt-5-upstream",
		Choices: []openai.Choice{{
			Index: 0,
			Delta: &openai.Message{
				Role:  "assistant",
				Extra: map[string]json.RawMessage{"tool_calls": json.RawMessage("[" + call + "]")},
			},
		}},
	}
}

// finishChunk builds the terminating chunk carrying a finish reason.
func finishChunk(reason string) openai.StreamChunk {
	return openai.StreamChunk{
		ID:      "c1",
		Object:  "chat.completion.chunk",
		Model:   "gpt-5-upstream",
		Choices: []openai.Choice{{Index: 0, FinishReason: &reason}},
	}
}

// stopReasonOf reads the stop reason off the message_delta frame.
func stopReasonOf(t *testing.T, frames []sseFrame) string {
	t.Helper()
	for _, f := range frames {
		if f.name != "message_delta" {
			continue
		}
		var ev struct {
			Delta struct {
				StopReason string `json:"stop_reason"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(f.data), &ev); err != nil {
			t.Fatalf("decode message_delta: %v", err)
		}
		return ev.Delta.StopReason
	}
	t.Fatal("no message_delta frame")
	return ""
}
