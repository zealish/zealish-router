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

func postResponsesStream(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := postResponses(t, h, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}
	return rec
}

func TestResponsesStreamEventSequence(t *testing.T) {
	p := &stubProvider{name: "openai", chunks: []openai.StreamChunk{
		textDelta("Hel"),
		textDelta("lo"),
	}}
	h := newTestServer(t, p)

	rec := postResponsesStream(t, h, `{"model":"gpt-5","input":"hi","stream":true}`)
	frames := parseFrames(t, rec.Body.String())

	want := []string{
		"response.created",
		"response.in_progress",
		"response.output_item.added",
		"response.output_text.delta",
		"response.output_text.delta",
		"response.output_text.done",
		"response.output_item.done",
		"response.completed",
	}
	if got := frameNames(frames); !slices.Equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if strings.Contains(rec.Body.String(), "[DONE]") {
		t.Error("stream carries a [DONE] sentinel, which is not part of this dialect")
	}

	// Deltas reassemble into the full text, and the done event repeats it.
	var text strings.Builder
	for _, f := range frames {
		if f.name != "response.output_text.delta" {
			continue
		}
		var ev struct {
			Delta string `json:"delta"`
		}
		if err := json.Unmarshal([]byte(f.data), &ev); err != nil {
			t.Fatalf("decode delta: %v", err)
		}
		text.WriteString(ev.Delta)
	}
	if text.String() != "Hello" {
		t.Errorf("reassembled text = %q, want Hello", text.String())
	}

	// Every event carries a strictly increasing sequence number.
	last := 0
	for _, f := range frames {
		var ev struct {
			Seq int `json:"sequence_number"`
		}
		if err := json.Unmarshal([]byte(f.data), &ev); err != nil {
			t.Fatalf("decode %s: %v", f.name, err)
		}
		if ev.Seq <= last {
			t.Fatalf("sequence_number %d after %d on %s", ev.Seq, last, f.name)
		}
		last = ev.Seq
	}

	// response.completed carries the aggregated output.
	var completed struct {
		Response openai.Response `json:"response"`
	}
	if err := json.Unmarshal([]byte(frames[len(frames)-1].data), &completed); err != nil {
		t.Fatalf("decode completed: %v", err)
	}
	if completed.Response.Status != "completed" {
		t.Errorf("status = %q, want completed", completed.Response.Status)
	}
	if len(completed.Response.Output) != 1 || completed.Response.Output[0].Type != "message" {
		t.Fatalf("final output = %+v, want one message item", completed.Response.Output)
	}
}

func TestResponsesStreamToolCall(t *testing.T) {
	reason := "tool_calls"
	p := &stubProvider{name: "openai", chunks: []openai.StreamChunk{
		{
			ID: "c1", Object: "chat.completion.chunk", Model: "gpt-5-upstream",
			Choices: []openai.Choice{{Index: 0, Delta: &openai.Message{
				Role: "assistant",
				Extra: map[string]json.RawMessage{"tool_calls": json.RawMessage(
					`[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":""}}]`)},
			}}},
		},
		{
			ID: "c1", Object: "chat.completion.chunk", Model: "gpt-5-upstream",
			Choices: []openai.Choice{{Index: 0, Delta: &openai.Message{
				Extra: map[string]json.RawMessage{"tool_calls": json.RawMessage(
					`[{"index":0,"function":{"arguments":"{\"city\":"}}]`)},
			}}},
		},
		{
			ID: "c1", Object: "chat.completion.chunk", Model: "gpt-5-upstream",
			Choices: []openai.Choice{{
				Index: 0,
				Delta: &openai.Message{
					Extra: map[string]json.RawMessage{"tool_calls": json.RawMessage(
						`[{"index":0,"function":{"arguments":"\"Jakarta\"}"}}]`)},
				},
				FinishReason: &reason,
			}},
		},
	}}
	h := newTestServer(t, p)

	rec := postResponsesStream(t, h, `{"model":"gpt-5","input":"hi","stream":true}`)
	frames := parseFrames(t, rec.Body.String())

	want := []string{
		"response.created",
		"response.in_progress",
		"response.output_item.added",
		"response.function_call_arguments.delta",
		"response.function_call_arguments.delta",
		"response.function_call_arguments.done",
		"response.output_item.done",
		"response.completed",
	}
	if got := frameNames(frames); !slices.Equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}

	// The added event announces id and name; the done event carries the
	// reassembled arguments.
	var added struct {
		Item openai.ResponseItem `json:"item"`
	}
	if err := json.Unmarshal([]byte(frames[2].data), &added); err != nil {
		t.Fatalf("decode added: %v", err)
	}
	if added.Item.Type != "function_call" || added.Item.CallID != "call_1" || added.Item.Name != "lookup" {
		t.Errorf("added item = %+v, want the announced call", added.Item)
	}

	var done struct {
		Arguments string `json:"arguments"`
	}
	if err := json.Unmarshal([]byte(frames[5].data), &done); err != nil {
		t.Fatalf("decode arguments done: %v", err)
	}
	if done.Arguments != `{"city":"Jakarta"}` {
		t.Errorf("arguments = %q, want the reassembled object", done.Arguments)
	}
}

func TestResponsesStreamTextThenToolSeparateItems(t *testing.T) {
	p := &stubProvider{name: "openai", chunks: []openai.StreamChunk{
		textDelta("thinking"),
		{
			ID: "c1", Object: "chat.completion.chunk", Model: "gpt-5-upstream",
			Choices: []openai.Choice{{Index: 0, Delta: &openai.Message{
				Extra: map[string]json.RawMessage{"tool_calls": json.RawMessage(
					`[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]`)},
			}}},
		},
	}}
	h := newTestServer(t, p)

	rec := postResponsesStream(t, h, `{"model":"gpt-5","input":"hi","stream":true}`)
	frames := parseFrames(t, rec.Body.String())

	// The text item closes before the call item opens; output indices advance.
	var indices []int
	for _, f := range frames {
		if f.name != "response.output_item.added" {
			continue
		}
		var ev struct {
			Index int `json:"output_index"`
		}
		if err := json.Unmarshal([]byte(f.data), &ev); err != nil {
			t.Fatalf("decode added: %v", err)
		}
		indices = append(indices, ev.Index)
	}
	if !slices.Equal(indices, []int{0, 1}) {
		t.Fatalf("output indices = %v, want [0 1]", indices)
	}
	if got := frameNames(frames); slices.Index(got, "response.output_text.done") > slices.Index(got, "response.function_call_arguments.delta") {
		t.Error("text item was not closed before the function call opened")
	}
}

func TestResponsesStreamEmptyUpstream(t *testing.T) {
	p := &stubProvider{name: "openai"}
	h := newTestServer(t, p)

	rec := postResponsesStream(t, h, `{"model":"gpt-5","input":"hi","stream":true}`)
	frames := parseFrames(t, rec.Body.String())

	// A silent upstream still produces a well-formed lifecycle.
	want := []string{"response.created", "response.in_progress", "response.completed"}
	if got := frameNames(frames); !slices.Equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

func TestResponsesStreamUsageOnCompleted(t *testing.T) {
	p := &stubProvider{name: "openai", chunks: []openai.StreamChunk{
		textDelta("hi"),
		{
			ID: "c1", Object: "chat.completion.chunk", Model: "gpt-5-upstream",
			Usage: &openai.Usage{PromptTokens: 7, CompletionTokens: 2, TotalTokens: 9},
		},
	}}
	h := newTestServer(t, p)

	rec := postResponsesStream(t, h, `{"model":"gpt-5","input":"hi","stream":true}`)
	frames := parseFrames(t, rec.Body.String())

	var completed struct {
		Response openai.Response `json:"response"`
	}
	if err := json.Unmarshal([]byte(frames[len(frames)-1].data), &completed); err != nil {
		t.Fatalf("decode completed: %v", err)
	}
	u := completed.Response.Usage
	if u == nil || u.InputTokens != 7 || u.OutputTokens != 2 || u.TotalTokens != 9 {
		t.Errorf("usage = %+v, want 7/2/9", u)
	}
}

func TestResponsesStreamPreFrameFailureIsStatusCode(t *testing.T) {
	p := &stubProvider{name: "openai", err: &provider.Error{
		Provider: "openai", Status: 503, Kind: provider.ErrUpstream5xx, Message: "down",
	}}
	h := newTestServer(t, p)

	rec := postResponses(t, h, `{"model":"gpt-5","input":"hi","stream":true}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 before any frame", rec.Code)
	}
	if strings.Contains(rec.Header().Get("Content-Type"), "text/event-stream") {
		t.Error("failure committed as a stream rather than a status code")
	}
}

func TestResponsesStreamBypassesCache(t *testing.T) {
	p := &stubProvider{name: "openai", chunks: []openai.StreamChunk{textDelta("x")}}
	h := newTestServer(t, p)

	rec := postResponsesStream(t, h, `{"model":"gpt-5","input":"hi","stream":true}`)
	if got := rec.Header().Get(cacheHeader); got != headerPass {
		t.Errorf("X-Cache = %q, want %s", got, headerPass)
	}
}
