package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/zealish/zealish-router/pkg/anthropic"
	"github.com/zealish/zealish-router/pkg/openai"
)

// decodeRequest parses an OpenAI request from its JSON form, so tests exercise
// the same Extra capture the gateway performs on a real request body.
func decodeRequest(t *testing.T, body string) *openai.ChatCompletionRequest {
	t.Helper()
	var req openai.ChatCompletionRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return &req
}

func TestToAnthropicTools(t *testing.T) {
	req := decodeRequest(t, `{
		"model": "claude",
		"messages": [{"role": "user", "content": "weather in Jakarta?"}],
		"tools": [
			{"type": "function", "function": {"name": "get_weather", "description": "look up weather",
			 "parameters": {"type": "object", "properties": {"city": {"type": "string"}}}}},
			{"type": "function", "function": {"name": "now"}},
			{"type": "web_search_preview"}
		],
		"tool_choice": "required"
	}`)

	out := toAnthropic(req, false)

	if len(out.Tools) != 2 {
		t.Fatalf("tools = %+v, want the two function tools only", out.Tools)
	}
	if out.Tools[0].Name != "get_weather" || out.Tools[0].Description != "look up weather" {
		t.Errorf("tool[0] = %+v", out.Tools[0])
	}
	var schema struct {
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(out.Tools[0].InputSchema, &schema); err != nil {
		t.Fatalf("input_schema: %v", err)
	}
	if _, ok := schema.Properties["city"]; !ok {
		t.Errorf("input_schema = %s, want the parameters forwarded", out.Tools[0].InputSchema)
	}
	if string(out.Tools[1].InputSchema) != string(emptySchema) {
		t.Errorf("tool without parameters = %s, want the empty object schema", out.Tools[1].InputSchema)
	}
	if out.ToolChoice == nil || out.ToolChoice.Type != "any" {
		t.Errorf("tool_choice = %+v, want type any", out.ToolChoice)
	}
}

func TestToAnthropicToolChoice(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		wantType string
		wantName string
		wantNil  bool
	}{
		{name: "auto", raw: `"auto"`, wantType: "auto"},
		{name: "none", raw: `"none"`, wantType: "none"},
		{name: "required", raw: `"required"`, wantType: "any"},
		{name: "named function", raw: `{"type":"function","function":{"name":"get_weather"}}`, wantType: "tool", wantName: "get_weather"},
		{name: "unknown string", raw: `"whatever"`, wantNil: true},
		{name: "malformed", raw: `{"type":"function"}`, wantNil: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := toAnthropicToolChoice(json.RawMessage(tc.raw))
			if tc.wantNil {
				if got != nil {
					t.Fatalf("tool_choice = %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("tool_choice = nil, want type %q", tc.wantType)
			}
			if got.Type != tc.wantType || got.Name != tc.wantName {
				t.Errorf("tool_choice = %+v, want type %q name %q", got, tc.wantType, tc.wantName)
			}
		})
	}
}

func TestToAnthropicToolChoiceNeedsTools(t *testing.T) {
	req := decodeRequest(t, `{
		"model": "claude",
		"messages": [{"role": "user", "content": "hi"}],
		"tool_choice": "required"
	}`)

	if out := toAnthropic(req, false); out.ToolChoice != nil {
		t.Errorf("tool_choice = %+v, want nil without a tools array", out.ToolChoice)
	}
}

func TestToAnthropicToolConversation(t *testing.T) {
	req := decodeRequest(t, `{
		"model": "claude",
		"messages": [
			{"role": "user", "content": "weather in Jakarta?"},
			{"role": "assistant", "content": "checking",
			 "tool_calls": [{"id": "call_1", "type": "function",
			   "function": {"name": "get_weather", "arguments": "{\"city\":\"Jakarta\"}"}}]},
			{"role": "tool", "tool_call_id": "call_1", "content": "32C"}
		]
	}`)

	out := toAnthropic(req, false)

	if len(out.Messages) != 3 {
		t.Fatalf("messages = %d, want 3", len(out.Messages))
	}

	var assistant []map[string]json.RawMessage
	if err := json.Unmarshal(out.Messages[1].Content, &assistant); err != nil {
		t.Fatalf("assistant content: %v", err)
	}
	if len(assistant) != 2 {
		t.Fatalf("assistant blocks = %d, want text plus tool_use", len(assistant))
	}
	if string(assistant[0]["type"]) != `"text"` {
		t.Errorf("block[0] type = %s, want text", assistant[0]["type"])
	}
	if string(assistant[1]["type"]) != `"tool_use"` || string(assistant[1]["id"]) != `"call_1"` {
		t.Errorf("block[1] = %v, want the tool_use call", assistant[1])
	}
	if string(assistant[1]["input"]) != `{"city":"Jakarta"}` {
		t.Errorf("tool input = %s, want the parsed arguments object", assistant[1]["input"])
	}

	if out.Messages[2].Role != "user" {
		t.Errorf("tool result role = %q, want user", out.Messages[2].Role)
	}
	var result []map[string]json.RawMessage
	if err := json.Unmarshal(out.Messages[2].Content, &result); err != nil {
		t.Fatalf("tool result content: %v", err)
	}
	if string(result[0]["type"]) != `"tool_result"` || string(result[0]["tool_use_id"]) != `"call_1"` {
		t.Errorf("tool result = %v", result[0])
	}
	if string(result[0]["content"]) != `"32C"` {
		t.Errorf("tool result content = %s, want 32C", result[0]["content"])
	}
}

func TestToAnthropicDropsOrphanToolResult(t *testing.T) {
	req := decodeRequest(t, `{
		"model": "claude",
		"messages": [
			{"role": "user", "content": "hi"},
			{"role": "tool", "content": "stray"}
		]
	}`)

	out := toAnthropic(req, false)
	if len(out.Messages) != 1 {
		t.Fatalf("messages = %+v, want the tool message without an id dropped", out.Messages)
	}
}

func TestToAnthropicMultimodalContent(t *testing.T) {
	req := decodeRequest(t, `{
		"model": "claude",
		"messages": [{"role": "user", "content": [
			{"type": "text", "text": "what is this?"},
			{"type": "image_url", "image_url": {"url": "data:image/png;base64,AAAB"}},
			{"type": "image_url", "image_url": {"url": "https://example.com/cat.png"}}
		]}]
	}`)

	out := toAnthropic(req, false)

	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal(out.Messages[0].Content, &blocks); err != nil {
		t.Fatalf("content: %v", err)
	}
	if len(blocks) != 3 {
		t.Fatalf("blocks = %d, want text plus two images", len(blocks))
	}
	if string(blocks[0]["text"]) != `"what is this?"` {
		t.Errorf("block[0] = %v", blocks[0])
	}

	var inline anthropic.Source
	if err := json.Unmarshal(blocks[1]["source"], &inline); err != nil {
		t.Fatalf("inline source: %v", err)
	}
	if inline.Type != "base64" || inline.MediaType != "image/png" || inline.Data != "AAAB" {
		t.Errorf("inline source = %+v, want the decoded data URL", inline)
	}

	var remote anthropic.Source
	if err := json.Unmarshal(blocks[2]["source"], &remote); err != nil {
		t.Fatalf("remote source: %v", err)
	}
	if remote.Type != "url" || remote.URL != "https://example.com/cat.png" {
		t.Errorf("remote source = %+v, want a url source", remote)
	}
}

func TestToAnthropicKeepsStringContent(t *testing.T) {
	req := decodeRequest(t, `{"model":"claude","messages":[{"role":"user","content":"plain"}]}`)

	out := toAnthropic(req, false)
	if string(out.Messages[0].Content) != `"plain"` {
		t.Errorf("content = %s, want the compact string form preserved", out.Messages[0].Content)
	}
}

func TestAnthropicToolCallResponse(t *testing.T) {
	p := newAnthropicProvider(t, Options{APIKey: "k"}, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id":"msg_1","model":"claude-sonnet-4","stop_reason":"tool_use",
			"content":[
				{"type":"text","text":"let me check"},
				{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"city":"Jakarta"}}
			],
			"usage":{"input_tokens":9,"output_tokens":5}}`)
	})

	resp, err := p.ChatCompletion(context.Background(), anthropicRequestFixture())
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	if *resp.Choices[0].FinishReason != "tool_calls" {
		t.Errorf("finish_reason = %q, want tool_calls", *resp.Choices[0].FinishReason)
	}

	raw, ok := resp.Choices[0].Message.Extra["tool_calls"]
	if !ok {
		t.Fatalf("message extras = %v, want tool_calls", resp.Choices[0].Message.Extra)
	}
	var calls []openAIToolCall
	if err := json.Unmarshal(raw, &calls); err != nil {
		t.Fatalf("tool_calls: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("tool_calls = %+v, want one", calls)
	}
	if calls[0].ID != "toolu_1" || calls[0].Type != "function" || calls[0].Function.Name != "get_weather" {
		t.Errorf("tool call = %+v", calls[0])
	}
	if calls[0].Function.Arguments != `{"city":"Jakarta"}` {
		t.Errorf("arguments = %q, want the input rendered as a JSON string", calls[0].Function.Arguments)
	}

	// The serialised message must carry tool_calls next to the content, which is
	// what an OpenAI SDK reads.
	encoded, err := json.Marshal(resp.Choices[0].Message)
	if err != nil {
		t.Fatalf("marshal message: %v", err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatalf("decode message: %v", err)
	}
	if _, ok := wire["tool_calls"]; !ok {
		t.Errorf("serialised message = %s, want tool_calls on the wire", encoded)
	}
}

func TestAnthropicToolCallStream(t *testing.T) {
	body := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_3","model":"claude-sonnet-4","usage":{"input_tokens":6}}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_9","name":"get_weather"}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"Jakarta\"}"}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":11}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"

	p := newAnthropicProvider(t, Options{APIKey: "k"}, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body)
	})

	ch, err := p.ChatCompletionStream(context.Background(), anthropicRequestFixture())
	if err != nil {
		t.Fatalf("ChatCompletionStream: %v", err)
	}

	var (
		name string
		id   string
		args string
		last openai.StreamChunk
	)
	for chunk := range ch {
		last = chunk
		if len(chunk.Choices) == 0 || chunk.Choices[0].Delta == nil {
			continue
		}
		raw, ok := chunk.Choices[0].Delta.Extra["tool_calls"]
		if !ok {
			continue
		}
		var calls []openAIToolCall
		if err := json.Unmarshal(raw, &calls); err != nil {
			t.Fatalf("tool_calls delta: %v", err)
		}
		for _, c := range calls {
			if c.Index == nil || *c.Index != 0 {
				t.Errorf("tool call index = %v, want 0", c.Index)
			}
			if c.ID != "" {
				id = c.ID
			}
			if c.Function.Name != "" {
				name = c.Function.Name
			}
			args += c.Function.Arguments
		}
	}

	if id != "toolu_9" || name != "get_weather" {
		t.Errorf("tool call id/name = %q/%q, want toolu_9/get_weather", id, name)
	}
	if args != `{"city":"Jakarta"}` {
		t.Errorf("accumulated arguments = %q", args)
	}
	if last.Choices[0].FinishReason == nil || *last.Choices[0].FinishReason != "tool_calls" {
		t.Errorf("finish reason = %v, want tool_calls", last.Choices[0].FinishReason)
	}
}
