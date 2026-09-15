package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zealish/zealish-router/internal/provider"
	"github.com/zealish/zealish-router/pkg/anthropic"
	"github.com/zealish/zealish-router/pkg/openai"
)

// recordingProvider captures the OpenAI request the translation produced, so a
// test can assert on what the routing engine was actually handed.
type recordingProvider struct {
	stubProvider
	got *openai.ChatCompletionRequest
}

func (p *recordingProvider) ChatCompletion(ctx context.Context, req *openai.ChatCompletionRequest) (*openai.ChatCompletionResponse, error) {
	p.got = req
	return p.stubProvider.ChatCompletion(ctx, req)
}

func (p *recordingProvider) ChatCompletionStream(ctx context.Context, req *openai.ChatCompletionRequest) (<-chan openai.StreamChunk, error) {
	p.got = req
	return p.stubProvider.ChatCompletionStream(ctx, req)
}

// toolCallProvider answers with a tool call rather than text, so the response
// translation has tool_calls to convert back into tool_use blocks.
type toolCallProvider struct {
	stubProvider
}

func (p *toolCallProvider) ChatCompletion(context.Context, *openai.ChatCompletionRequest) (*openai.ChatCompletionResponse, error) {
	reason := "tool_calls"
	return &openai.ChatCompletionResponse{
		ID:     "cmpl-1",
		Object: "chat.completion",
		Model:  "gpt-5-upstream",
		Choices: []openai.Choice{{
			Index: 0,
			Message: &openai.Message{
				Role: "assistant",
				Extra: map[string]json.RawMessage{
					"tool_calls": json.RawMessage(
						`[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"city\":\"Jakarta\"}"}}]`),
				},
			},
			FinishReason: &reason,
		}},
	}, nil
}

// usageProvider reports a prompt split across fresh, cached and written
// tokens, which the two dialects account for differently.
type usageProvider struct {
	stubProvider
}

func (p *usageProvider) ChatCompletion(context.Context, *openai.ChatCompletionRequest) (*openai.ChatCompletionResponse, error) {
	return &openai.ChatCompletionResponse{
		ID:      "cmpl-1",
		Object:  "chat.completion",
		Model:   "gpt-5-upstream",
		Choices: []openai.Choice{{Index: 0, Message: &openai.Message{Role: "assistant", Content: json.RawMessage(`"pong"`)}}},
		Usage: &openai.Usage{
			PromptTokens:        10,
			CompletionTokens:    4,
			TotalTokens:         14,
			PromptTokensDetails: &openai.PromptTokensDetails{CachedTokens: 3, CacheWriteTokens: 2},
		},
	}, nil
}

func postMessages(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, messagesPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeAnthropicError(t *testing.T, rec *httptest.ResponseRecorder) anthropic.ErrorResponse {
	t.Helper()
	var out anthropic.ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode error envelope: %v (body=%q)", err, rec.Body.String())
	}
	return out
}

func decodeMessage(t *testing.T, rec *httptest.ResponseRecorder) anthropic.Response {
	t.Helper()
	var out anthropic.Response
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode message: %v (body=%q)", err, rec.Body.String())
	}
	return out
}

func TestMessagesHappyPath(t *testing.T) {
	p := &recordingProvider{stubProvider: stubProvider{name: "openai"}}
	h := newTestServer(t, p)

	rec := postMessages(t, h, `{"model":"gpt-5","max_tokens":64,"system":"be brief","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}

	// The system prompt becomes a leading system message.
	if len(p.got.Messages) != 2 || p.got.Messages[0].Role != "system" {
		t.Fatalf("messages = %+v, want a system message ahead of the user turn", p.got.Messages)
	}
	if text, _ := p.got.Messages[0].Text(); text != "be brief" {
		t.Errorf("system message = %q, want the hoisted system prompt", text)
	}
	if p.got.MaxTokens == nil || *p.got.MaxTokens != 64 {
		t.Errorf("max_tokens = %v, want 64", p.got.MaxTokens)
	}

	resp := decodeMessage(t, rec)
	if resp.Type != "message" || resp.Role != "assistant" {
		t.Errorf("envelope = %+v, want an assistant message", resp)
	}
	if resp.Model != "gpt-5-upstream" {
		t.Errorf("model = %q, want the resolved upstream name", resp.Model)
	}
	if len(resp.Content) != 1 || resp.Content[0].Type != "text" || resp.Content[0].Text != "pong" {
		t.Errorf("content = %+v, want a single text block", resp.Content)
	}
}

func TestMessagesRequiredFields(t *testing.T) {
	h := newTestServer(t, &stubProvider{name: "openai"})

	cases := []struct {
		name string
		body string
		want string
	}{
		{"missing model", `{"max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`, "model"},
		{"missing messages", `{"model":"gpt-5","max_tokens":8}`, "messages"},
		{"missing max_tokens", `{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`, "max_tokens"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := postMessages(t, h, tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
			env := decodeAnthropicError(t, rec)
			if env.Type != "error" || env.Error.Type != "invalid_request_error" {
				t.Errorf("envelope = %+v, want an Anthropic invalid_request_error", env)
			}
			if !strings.Contains(env.Error.Message, tc.want) {
				t.Errorf("message = %q, want it to mention %q", env.Error.Message, tc.want)
			}
		})
	}
}

func TestMessagesErrorsUseAnthropicEnvelope(t *testing.T) {
	p := &stubProvider{name: "openai", err: &provider.Error{
		Provider: "openai", Status: 503, Kind: provider.ErrUpstream5xx, Message: "down",
	}}
	h := newTestServer(t, p)

	rec := postMessages(t, h, `{"model":"gpt-5","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	env := decodeAnthropicError(t, rec)
	if env.Type != "error" || env.Error.Type != "api_error" {
		t.Errorf("envelope = %+v, want an Anthropic api_error", env)
	}
	if strings.Contains(env.Error.Message, "down") {
		t.Error("internal upstream detail leaked to the client")
	}
}

func TestMessagesUnknownAlias(t *testing.T) {
	h := newTestServer(t, &stubProvider{name: "openai"})

	rec := postMessages(t, h, `{"model":"does-not-exist","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if got := decodeAnthropicError(t, rec).Error.Type; got != "invalid_request_error" {
		t.Errorf("error type = %q", got)
	}
}

func TestMessagesToolCallRoundTrip(t *testing.T) {
	p := &recordingProvider{stubProvider: stubProvider{name: "openai"}}
	h := newTestServer(t, p)

	// A full tool cycle: declaration, the assistant's call, and the result.
	body := `{
	  "model":"gpt-5","max_tokens":8,
	  "tools":[{"name":"lookup","description":"find it","input_schema":{"type":"object"}}],
	  "tool_choice":{"type":"tool","name":"lookup"},
	  "messages":[
	    {"role":"user","content":"weather?"},
	    {"role":"assistant","content":[{"type":"tool_use","id":"tu_1","name":"lookup","input":{"city":"Jakarta"}}]},
	    {"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_1","content":"31C"}]}
	  ]
	}`
	if rec := postMessages(t, h, body); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}

	var tools []struct {
		Type     string `json:"type"`
		Function struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
		} `json:"function"`
	}
	if err := json.Unmarshal(p.got.Extra["tools"], &tools); err != nil {
		t.Fatalf("decode tools: %v", err)
	}
	if len(tools) != 1 || tools[0].Type != "function" || tools[0].Function.Name != "lookup" {
		t.Fatalf("tools = %+v, want one function tool named lookup", tools)
	}
	if string(tools[0].Function.Parameters) != `{"type":"object"}` {
		t.Errorf("parameters = %s, want the input schema verbatim", tools[0].Function.Parameters)
	}

	var choice struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(p.got.Extra["tool_choice"], &choice); err != nil {
		t.Fatalf("decode tool_choice: %v", err)
	}
	if choice.Type != "function" || choice.Function.Name != "lookup" {
		t.Errorf("tool_choice = %+v, want the named function form", choice)
	}

	// The assistant's tool_use became tool_calls, and the tool_result became
	// its own tool message carrying the originating call id.
	if len(p.got.Messages) != 3 {
		t.Fatalf("messages = %d, want user + assistant + tool", len(p.got.Messages))
	}
	assistant := p.got.Messages[1]
	if assistant.Role != "assistant" {
		t.Fatalf("second message role = %q, want assistant", assistant.Role)
	}
	var calls []struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	if err := json.Unmarshal(assistant.Extra["tool_calls"], &calls); err != nil {
		t.Fatalf("decode tool_calls: %v", err)
	}
	if len(calls) != 1 || calls[0].ID != "tu_1" || calls[0].Function.Name != "lookup" {
		t.Fatalf("tool_calls = %+v, want the translated call", calls)
	}
	if calls[0].Function.Arguments != `{"city":"Jakarta"}` {
		t.Errorf("arguments = %q, want the input rendered as a JSON string", calls[0].Function.Arguments)
	}

	result := p.got.Messages[2]
	if result.Role != "tool" {
		t.Fatalf("third message role = %q, want tool", result.Role)
	}
	var id string
	if err := json.Unmarshal(result.Extra["tool_call_id"], &id); err != nil || id != "tu_1" {
		t.Errorf("tool_call_id = %q (err=%v), want tu_1", id, err)
	}
	if text, _ := result.Text(); text != "31C" {
		t.Errorf("tool content = %q, want the flattened result", text)
	}
}

func TestMessagesToolCallsReturnedAsToolUse(t *testing.T) {
	p := &toolCallProvider{stubProvider{name: "openai"}}
	h := newTestServer(t, p)

	rec := postMessages(t, h, `{"model":"gpt-5","max_tokens":8,"messages":[{"role":"user","content":"weather?"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}

	resp := decodeMessage(t, rec)
	if resp.StopReason == nil || *resp.StopReason != "tool_use" {
		t.Errorf("stop_reason = %v, want tool_use", resp.StopReason)
	}
	if len(resp.Content) != 1 || resp.Content[0].Type != "tool_use" {
		t.Fatalf("content = %+v, want a single tool_use block", resp.Content)
	}
	block := resp.Content[0]
	if block.ID != "call_1" || block.Name != "lookup" {
		t.Errorf("block = %+v, want the call id and name", block)
	}
	if string(block.Input) != `{"city":"Jakarta"}` {
		t.Errorf("input = %s, want the arguments parsed back into an object", block.Input)
	}
}

func TestMessagesImageContentBecomesDataURL(t *testing.T) {
	p := &recordingProvider{stubProvider: stubProvider{name: "openai"}}
	h := newTestServer(t, p)

	body := `{"model":"gpt-5","max_tokens":8,"messages":[{"role":"user","content":[
	  {"type":"text","text":"what is this"},
	  {"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}
	]}]}`
	if rec := postMessages(t, h, body); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}

	var parts []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL *struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if err := json.Unmarshal(p.got.Messages[0].Content, &parts); err != nil {
		t.Fatalf("decode parts: %v", err)
	}
	if len(parts) != 2 || parts[0].Type != "text" || parts[1].Type != "image_url" {
		t.Fatalf("parts = %+v, want text followed by image_url", parts)
	}
	if parts[1].ImageURL == nil || parts[1].ImageURL.URL != "data:image/png;base64,AAAA" {
		t.Errorf("image_url = %+v, want inline bytes as a data URL", parts[1].ImageURL)
	}
}

func TestMessagesUsageTranslatedWithoutDoubleCounting(t *testing.T) {
	p := &usageProvider{stubProvider{name: "openai"}}
	h := newTestServer(t, p)

	rec := postMessages(t, h, `{"model":"gpt-5","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	usage := decodeMessage(t, rec).Usage
	if usage == nil {
		t.Fatal("usage missing")
	}
	// prompt_tokens is the total; the cached and written portions are reported
	// on their own fields and must not be counted twice.
	if usage.InputTokens != 5 {
		t.Errorf("input_tokens = %d, want 5 (10 total less 3 cached and 2 written)", usage.InputTokens)
	}
	if usage.CacheReadInputTokens != 3 || usage.CacheCreationInputTokens != 2 {
		t.Errorf("cache usage = %+v, want 3 read / 2 written", usage)
	}
	if usage.OutputTokens != 4 {
		t.Errorf("output_tokens = %d, want 4", usage.OutputTokens)
	}
}
