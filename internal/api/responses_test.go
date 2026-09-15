package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zealish/zealish-router/internal/provider"
	"github.com/zealish/zealish-router/pkg/openai"
)

func postResponses(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeResponse(t *testing.T, rec *httptest.ResponseRecorder) openai.Response {
	t.Helper()
	var out openai.Response
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v (body=%q)", err, rec.Body.String())
	}
	return out
}

func TestResponsesHappyPathStringInput(t *testing.T) {
	p := &recordingProvider{stubProvider: stubProvider{name: "openai"}}
	h := newTestServer(t, p)

	rec := postResponses(t, h, `{"model":"gpt-5","instructions":"be brief","input":"hi"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}

	// Instructions become a leading system message, the string input a user turn.
	if len(p.got.Messages) != 2 || p.got.Messages[0].Role != "system" || p.got.Messages[1].Role != "user" {
		t.Fatalf("messages = %+v, want system then user", p.got.Messages)
	}
	if text, _ := p.got.Messages[1].Text(); text != "hi" {
		t.Errorf("user message = %q, want the string input", text)
	}

	resp := decodeResponse(t, rec)
	if resp.Object != "response" || resp.Status != "completed" {
		t.Errorf("envelope = %+v, want a completed response", resp)
	}
	if !strings.HasPrefix(resp.ID, "resp_") {
		t.Errorf("id = %q, want the resp_ prefix", resp.ID)
	}
	if len(resp.Output) != 1 || resp.Output[0].Type != "message" {
		t.Fatalf("output = %+v, want one message item", resp.Output)
	}
	var parts []openai.ResponseContentPart
	if err := json.Unmarshal(resp.Output[0].Content, &parts); err != nil {
		t.Fatalf("decode content: %v", err)
	}
	if len(parts) != 1 || parts[0].Type != "output_text" || parts[0].Text != "pong" {
		t.Errorf("content = %+v, want a single output_text part", parts)
	}
	if parts[0].Annotations == nil {
		t.Error("annotations = nil, want an empty array for SDK iteration")
	}
}

func TestResponsesItemArrayInput(t *testing.T) {
	p := &recordingProvider{stubProvider: stubProvider{name: "openai"}}
	h := newTestServer(t, p)

	body := `{"model":"gpt-5","input":[
		{"role":"user","content":"first"},
		{"type":"message","role":"assistant","content":[{"type":"output_text","text":"prior answer"}]},
		{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"city\":\"Jakarta\"}"},
		{"type":"function_call_output","call_id":"call_1","output":"sunny"},
		{"role":"user","content":[{"type":"input_text","text":"and now?"},{"type":"input_image","image_url":"data:image/png;base64,AAA"}]}
	]}`
	rec := postResponses(t, h, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}

	msgs := p.got.Messages
	if len(msgs) != 5 {
		t.Fatalf("messages = %d, want 5 (%+v)", len(msgs), msgs)
	}

	// A typeless {role, content} object is the EasyInputMessage shorthand.
	if text, _ := msgs[0].Text(); msgs[0].Role != "user" || text != "first" {
		t.Errorf("msg[0] = %+v, want the shorthand user turn", msgs[0])
	}
	// Output parts flatten back to the compact string form.
	if text, _ := msgs[1].Text(); msgs[1].Role != "assistant" || text != "prior answer" {
		t.Errorf("msg[1] = %+v, want the assistant text", msgs[1])
	}
	// function_call becomes an assistant message with tool_calls.
	if msgs[2].Role != "assistant" || msgs[2].Extra["tool_calls"] == nil {
		t.Errorf("msg[2] = %+v, want assistant tool_calls", msgs[2])
	}
	if !strings.Contains(string(msgs[2].Extra["tool_calls"]), `"call_1"`) {
		t.Errorf("tool_calls = %s, want call_1 carried through", msgs[2].Extra["tool_calls"])
	}
	// function_call_output becomes a tool message with tool_call_id.
	if msgs[3].Role != "tool" || string(msgs[3].Extra["tool_call_id"]) != `"call_1"` {
		t.Errorf("msg[3] = %+v, want a tool message for call_1", msgs[3])
	}
	if text, _ := msgs[3].Text(); text != "sunny" {
		t.Errorf("tool output = %q, want the flattened string", text)
	}
	// Mixed text+image keeps the array-of-parts form with image_url.
	if !strings.Contains(string(msgs[4].Content), `"image_url"`) {
		t.Errorf("msg[4] content = %s, want an image_url part", msgs[4].Content)
	}
}

func TestResponsesToolsAndChoiceTranslate(t *testing.T) {
	p := &recordingProvider{stubProvider: stubProvider{name: "openai"}}
	h := newTestServer(t, p)

	body := `{"model":"gpt-5","input":"hi","tool_choice":{"type":"function","name":"lookup"},"tools":[
		{"type":"function","name":"lookup","description":"d","parameters":{"type":"object"}},
		{"type":"web_search"}
	]}`
	rec := postResponses(t, h, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}

	tools := string(p.got.Extra["tools"])
	if !strings.Contains(tools, `"function"`) || !strings.Contains(tools, `"lookup"`) {
		t.Errorf("tools = %s, want the nested function form", tools)
	}
	if strings.Contains(tools, "web_search") {
		t.Errorf("tools = %s, hosted tool should be dropped", tools)
	}
	choice := string(p.got.Extra["tool_choice"])
	if !strings.Contains(choice, `"lookup"`) || !strings.Contains(choice, `"function":`) {
		t.Errorf("tool_choice = %s, want the nested function form", choice)
	}
}

func TestResponsesToolCallOutput(t *testing.T) {
	h := newTestServer(t, &toolCallProvider{stubProvider{name: "openai"}})

	rec := postResponses(t, h, `{"model":"gpt-5","input":"call the tool"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}

	resp := decodeResponse(t, rec)
	if len(resp.Output) != 1 || resp.Output[0].Type != "function_call" {
		t.Fatalf("output = %+v, want one function_call item", resp.Output)
	}
	item := resp.Output[0]
	if item.CallID != "call_1" || item.Name != "lookup" {
		t.Errorf("item = %+v, want call_1/lookup", item)
	}
	if item.Arguments != `{"city":"Jakarta"}` {
		t.Errorf("arguments = %q, want the JSON string form", item.Arguments)
	}
}

func TestResponsesUsageMapping(t *testing.T) {
	h := newTestServer(t, &usageProvider{stubProvider{name: "openai"}})

	rec := postResponses(t, h, `{"model":"gpt-5","input":"hi"}`)
	resp := decodeResponse(t, rec)
	u := resp.Usage
	if u == nil {
		t.Fatal("usage missing")
	}
	if u.InputTokens != 10 || u.OutputTokens != 4 || u.TotalTokens != 14 {
		t.Errorf("usage = %+v, want 10/4/14", u)
	}
	if u.InputTokensDetails == nil || u.InputTokensDetails.CachedTokens != 3 {
		t.Errorf("input details = %+v, want cached_tokens 3", u.InputTokensDetails)
	}
}

func TestResponsesRequiredFields(t *testing.T) {
	h := newTestServer(t, &stubProvider{name: "openai"})

	cases := []struct {
		name string
		body string
		want string
	}{
		{"missing model", `{"input":"hi"}`, "model"},
		{"missing input", `{"model":"gpt-5"}`, "input"},
		{"previous_response_id", `{"model":"gpt-5","input":"hi","previous_response_id":"resp_1"}`, "previous_response_id"},
		{"unmodelled input shape", `{"model":"gpt-5","input":42}`, "input"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := postResponses(t, h, tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
			}
			var env openai.ErrorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
				t.Fatalf("decode error: %v", err)
			}
			if !strings.Contains(env.Error.Message, tc.want) {
				t.Errorf("message = %q, want it to mention %q", env.Error.Message, tc.want)
			}
		})
	}
}

func TestResponsesUnknownAlias(t *testing.T) {
	h := newTestServer(t, &stubProvider{name: "openai"})

	rec := postResponses(t, h, `{"model":"does-not-exist","input":"hi"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestResponsesUpstreamFailureNoLeak(t *testing.T) {
	p := &stubProvider{name: "openai", err: &provider.Error{
		Provider: "openai", Status: 503, Kind: provider.ErrUpstream5xx, Message: "secret detail",
	}}
	h := newTestServer(t, p)

	rec := postResponses(t, h, `{"model":"gpt-5","input":"hi"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "secret detail") {
		t.Error("internal upstream detail leaked to the client")
	}
}

func TestResponsesMaxOutputTokensAndLength(t *testing.T) {
	p := &lengthProvider{stubProvider{name: "openai"}}
	h := newTestServer(t, p)

	rec := postResponses(t, h, `{"model":"gpt-5","input":"hi","max_output_tokens":5}`)
	resp := decodeResponse(t, rec)
	if resp.Status != "incomplete" {
		t.Errorf("status = %q, want incomplete on a length finish", resp.Status)
	}
	if resp.IncompleteDetails == nil || resp.IncompleteDetails.Reason != "max_output_tokens" {
		t.Errorf("incomplete_details = %+v, want max_output_tokens", resp.IncompleteDetails)
	}
	if resp.MaxOutputTokens == nil || *resp.MaxOutputTokens != 5 {
		t.Errorf("max_output_tokens = %v, want the request value echoed", resp.MaxOutputTokens)
	}
}

// lengthProvider finishes with reason "length", the truncated case.
type lengthProvider struct {
	stubProvider
}

func (p *lengthProvider) ChatCompletion(_ context.Context, req *openai.ChatCompletionRequest) (*openai.ChatCompletionResponse, error) {
	if req.MaxTokens == nil || *req.MaxTokens != 5 {
		return nil, fmt.Errorf("max_tokens = %v, want 5", req.MaxTokens)
	}
	reason := "length"
	return &openai.ChatCompletionResponse{
		ID:     "cmpl-1",
		Object: "chat.completion",
		Model:  "gpt-5-upstream",
		Choices: []openai.Choice{{
			Index:        0,
			Message:      &openai.Message{Role: "assistant", Content: json.RawMessage(`"trunc"`)},
			FinishReason: &reason,
		}},
	}, nil
}
