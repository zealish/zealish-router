package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/zealish/zealish-router/internal/router"
	"github.com/zealish/zealish-router/internal/storage"
	"github.com/zealish/zealish-router/internal/stream"
	"github.com/zealish/zealish-router/pkg/openai"
)

// This file translates the OpenAI Responses dialect into the Chat Completions
// shape the router speaks internally, and back. Codex CLI and the current
// OpenAI SDKs default to this endpoint, so without it they cannot reach the
// gateway at all.
//
// Everything downstream of responsesToChatRequest — alias resolution,
// fallback, retry, quotas, caching, usage accounting — is unchanged, so a
// Responses client gets the same routing as a Chat Completions one.

// responses implements POST /v1/responses. It shares every middleware, quota,
// allowlist and cache path with chatCompletions; only the wire format on
// either end differs.
func (h *handler) responses(w http.ResponseWriter, r *http.Request) {
	raw, ok := h.readBody(w, r)
	if !ok {
		return
	}

	var req openai.ResponseRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		writeDecodeError(w, err, "Malformed JSON body.")
		return
	}
	if req.Model == "" {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "Field 'model' is required.")
		return
	}
	if len(req.Input) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "Field 'input' is required.")
		return
	}
	if req.PreviousResponseID != "" {
		// The gateway stores no responses, so the id cannot be resolved.
		// Rejecting is honest; accepting would silently drop the entire
		// conversation history the id stands for.
		writeError(w, http.StatusBadRequest, "invalid_request_error",
			"'previous_response_id' is not supported by this gateway; send the full conversation in 'input'.")
		return
	}
	if !allowModel(w, r, req.Model) {
		return
	}
	cleanup := h.trackActive(r, req.Model)
	defer cleanup()

	converted, err := responsesToChatRequest(&req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}

	// Routing is dialect-agnostic, but the trace records which dialect the
	// client spoke so the dashboard can separate the populations.
	r = r.WithContext(router.WithDialect(r.Context(), storage.DialectResponses))

	if req.Stream {
		w.Header().Set(cacheHeader, headerPass)
		h.streamResponses(w, r, &req, converted)
		return
	}

	key, served := h.lookupCache(w, endpointResponses, req.Model, raw)
	if served {
		return
	}

	resp, err := h.engine.ChatCompletion(r.Context(), converted)
	if err != nil {
		h.writeEngineError(w, r, err)
		return
	}

	body, err := json.Marshal(chatToResponse(&req, resp))
	if err != nil {
		h.logger.Error("encode response", slog.Any("error", err))
		writeError(w, http.StatusInternalServerError, "api_error", "Failed to encode the upstream response.")
		return
	}
	h.storeCache(endpointResponses, req.Model, key, body)
	writeBody(w, body)
}

// --- request translation ---

// responsesToChatRequest converts a Responses request. Instructions become a
// leading system message; input items become chat messages.
func responsesToChatRequest(req *openai.ResponseRequest) (*openai.ChatCompletionRequest, error) {
	items, ok := req.InputItems()
	if !ok {
		return nil, fmt.Errorf("field 'input' must be a string or an array of input items")
	}

	out := &openai.ChatCompletionRequest{
		Model:       req.Model,
		Stream:      req.Stream,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		MaxTokens:   req.MaxOutputTokens,
		User:        req.User,
		Messages:    make([]openai.Message, 0, len(items)+1),
	}
	if req.Instructions != "" {
		out.Messages = append(out.Messages, openai.Message{
			Role:    "system",
			Content: mustMarshal(req.Instructions),
		})
	}
	for _, item := range items {
		msg, ok := itemToMessage(item)
		if !ok {
			continue
		}
		out.Messages = append(out.Messages, msg)
	}

	if tools := responseToolsToChat(req.Tools); tools != nil {
		out.Extra = map[string]json.RawMessage{"tools": tools}
	}
	if choice := responseToolChoiceToChat(req.ToolChoice); choice != nil {
		if out.Extra == nil {
			out.Extra = map[string]json.RawMessage{}
		}
		out.Extra["tool_choice"] = choice
	}
	return out, nil
}

// itemToMessage converts one input item. Unmodelled item kinds (reasoning,
// web_search_call, …) are dropped rather than forwarded into a rejection.
func itemToMessage(item openai.ResponseItem) (openai.Message, bool) {
	switch item.Type {
	case "message", "":
		// A bare {role, content} object without a type is the EasyInputMessage
		// shorthand every SDK emits.
		if item.Role == "" {
			return openai.Message{}, false
		}
		return openai.Message{Role: item.Role, Content: contentToChat(item.Content)}, true

	case "function_call":
		call := map[string]any{
			"id":   item.CallID,
			"type": "function",
			"function": map[string]string{
				"name":      item.Name,
				"arguments": argumentsOrEmpty(item.Arguments),
			},
		}
		return openai.Message{
			Role:  "assistant",
			Extra: map[string]json.RawMessage{"tool_calls": mustMarshal([]any{call})},
		}, true

	case "function_call_output":
		return openai.Message{
			Role:            "tool",
			Content:         contentToChat(item.Output),
			ToolResultError: item.Status == "failed",
			Extra:           map[string]json.RawMessage{"tool_call_id": mustMarshal(item.CallID)},
		}, true

	default:
		return openai.Message{}, false
	}
}

// contentToChat converts message content. String content passes through; the
// typed input/output parts of this dialect become the Chat Completions parts.
func contentToChat(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return raw
	}

	var parts []openai.ResponseContentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return raw
	}

	var (
		converted []json.RawMessage
		text      strings.Builder
		textOnly  = true
	)
	for _, p := range parts {
		switch p.Type {
		case "input_text", "output_text", "text":
			text.WriteString(p.Text)
			converted = append(converted, mustMarshal(map[string]any{"type": "text", "text": p.Text}))
		case "input_image":
			if p.ImageURL == "" {
				return raw
			}
			textOnly = false
			img := map[string]string{"url": p.ImageURL}
			if p.Detail != "" {
				img["detail"] = p.Detail
			}
			converted = append(converted, mustMarshal(map[string]any{"type": "image_url", "image_url": img}))
		default:
			return raw
		}
	}
	if len(converted) == 0 {
		return nil
	}
	// A text-only turn keeps the compact string form.
	if textOnly {
		return mustMarshal(text.String())
	}
	return mustMarshal(converted)
}

// outputText flattens a function_call_output body into the string Chat
// Completions carries as the tool message content.
func outputText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return string(raw)
}

// responseToolsToChat converts the declared tools. This dialect flattens what
// Chat Completions nests under "function".
func responseToolsToChat(tools []openai.ResponseTool) json.RawMessage {
	if len(tools) == 0 {
		return nil
	}
	out := make([]json.RawMessage, 0, len(tools))
	for _, t := range tools {
		if t.Type != "function" || t.Name == "" {
			// Hosted tools (web_search, file_search, …) have no Chat
			// Completions equivalent; dropping beats a guaranteed rejection.
			continue
		}
		fn := map[string]any{"name": t.Name}
		if t.Description != "" {
			fn["description"] = t.Description
		}
		if len(t.Parameters) > 0 {
			fn["parameters"] = t.Parameters
		}
		if t.Strict != nil {
			fn["strict"] = *t.Strict
		}
		out = append(out, mustMarshal(map[string]any{"type": "function", "function": fn}))
	}
	if len(out) == 0 {
		return nil
	}
	return mustMarshal(out)
}

// responseToolChoiceToChat converts tool_choice. The string forms are shared;
// the object form names the function one level higher than Chat Completions.
func responseToolChoiceToChat(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		switch s {
		case "auto", "none", "required":
			return raw
		default:
			return nil
		}
	}
	var obj struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil || obj.Type != "function" || obj.Name == "" {
		return nil
	}
	return mustMarshal(map[string]any{
		"type":     "function",
		"function": map[string]string{"name": obj.Name},
	})
}

// argumentsOrEmpty keeps tool-call arguments a valid JSON string.
func argumentsOrEmpty(args string) string {
	if args == "" {
		return "{}"
	}
	return args
}

// --- response translation ---

// chatToResponse converts a completed Chat Completions response into the
// Responses envelope. The request is threaded through because the envelope
// echoes request parameters the internal shape does not carry.
func chatToResponse(req *openai.ResponseRequest, resp *openai.ChatCompletionResponse) *openai.Response {
	out := &openai.Response{
		ID:              responseID(resp.ID),
		Object:          "response",
		CreatedAt:       resp.Created,
		Status:          "completed",
		Model:           resp.Model,
		Output:          []openai.ResponseItem{},
		Usage:           toResponseUsage(resp.Usage),
		Instructions:    req.Instructions,
		MaxOutputTokens: req.MaxOutputTokens,
		Temperature:     req.Temperature,
		TopP:            req.TopP,
		Tools:           req.Tools,
		ToolChoice:      req.ToolChoice,
	}
	if out.Tools == nil {
		out.Tools = []openai.ResponseTool{}
	}
	if len(resp.Choices) == 0 {
		return out
	}

	choice := resp.Choices[0]
	if choice.Message != nil {
		out.Output = append(out.Output, outputItems(resp.ID, choice.Message)...)
	}
	if choice.FinishReason != nil && *choice.FinishReason == "length" {
		out.Status = "incomplete"
		out.IncompleteDetails = &openai.IncompleteDetails{Reason: "max_output_tokens"}
	}
	return out
}

// outputItems converts an assistant message into output items: at most one
// message item holding the text, then one function_call item per tool call.
func outputItems(id string, msg *openai.Message) []openai.ResponseItem {
	var out []openai.ResponseItem

	if text, ok := msg.Text(); ok && text != "" {
		out = append(out, openai.ResponseItem{
			Type:   "message",
			ID:     "msg_" + id,
			Role:   "assistant",
			Status: "completed",
			Content: mustMarshal([]openai.ResponseContentPart{{
				Type:        "output_text",
				Text:        text,
				Annotations: []json.RawMessage{},
			}}),
		})
	}

	for i, call := range decodeToolCalls(msg.Extra["tool_calls"]) {
		out = append(out, openai.ResponseItem{
			Type:      "function_call",
			ID:        fmt.Sprintf("fc_%s_%d", id, i),
			CallID:    call.ID,
			Name:      call.Function.Name,
			Arguments: argumentsOrEmpty(call.Function.Arguments),
			Status:    "completed",
		})
	}
	return out
}

// chatToolCall is the wire shape of one Chat Completions tool call.
type chatToolCall struct {
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func decodeToolCalls(raw json.RawMessage) []chatToolCall {
	if len(raw) == 0 {
		return nil
	}
	var calls []chatToolCall
	if err := json.Unmarshal(raw, &calls); err != nil {
		return nil
	}
	return calls
}

// toResponseUsage maps Chat Completions token accounting onto the Responses
// spelling. The counts are identical; only the field names differ.
func toResponseUsage(u *openai.Usage) *openai.ResponseUsage {
	if u == nil {
		return nil
	}
	out := &openai.ResponseUsage{
		InputTokens:  u.PromptTokens,
		OutputTokens: u.CompletionTokens,
		TotalTokens:  u.TotalTokens,
	}
	if u.PromptTokensDetails != nil {
		out.InputTokensDetails = &openai.InputTokensDetails{
			CachedTokens:     u.PromptTokensDetails.CachedTokens,
			CacheWriteTokens: u.PromptTokensDetails.CacheWriteTokens,
		}
	}
	if u.CompletionTokensDetails != nil {
		out.OutputTokensDetails = &openai.OutputTokensDetails{
			ReasoningTokens: u.CompletionTokensDetails.ReasoningTokens,
		}
	}
	return out
}

// responseID rewrites an upstream completion id into the resp_ namespace the
// dialect uses, so a client-side prefix check does not trip over cmpl-/chatcmpl-.
func responseID(id string) string {
	if id == "" {
		return "resp_unknown"
	}
	if strings.HasPrefix(id, "resp_") {
		return id
	}
	return "resp_" + id
}

// --- streaming ---

// streamResponses relays a completion as a Responses event stream. Like the
// other dialects, no headers are committed until the first frame, so a
// fallback failure is still reported as a status code.
func (h *handler) streamResponses(w http.ResponseWriter, r *http.Request, req *openai.ResponseRequest, converted *openai.ChatCompletionRequest) {
	sse, err := stream.NewWriter(w)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api_error", "Streaming is unsupported by the transport.")
		return
	}

	chunks, err := h.engine.ChatCompletionStream(r.Context(), converted)
	if err != nil {
		h.writeEngineError(w, r, err)
		return
	}

	h.metrics.StreamConnections.Inc()
	defer h.metrics.StreamConnections.Dec()

	if err := relayResponses(r.Context(), sse, req, chunks, stream.DefaultHeartbeat); err != nil {
		if errors.Is(err, context.Canceled) {
			h.logger.Debug("client disconnected mid-stream", slog.String("model", req.Model))
			return
		}
		h.logger.Error("stream aborted", slog.String("model", req.Model), slog.Any("error", err))
	}
}
