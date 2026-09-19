package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/zealish/zealish-router/internal/router"
	"github.com/zealish/zealish-router/internal/storage"
	"github.com/zealish/zealish-router/internal/stream"
	"github.com/zealish/zealish-router/pkg/anthropic"
	"github.com/zealish/zealish-router/pkg/openai"
)

// This file translates the Anthropic Messages dialect into the OpenAI shape the
// router speaks internally, and back. It is the mirror image of the translation
// in internal/provider/anthropic*.go, which runs when the *upstream* speaks
// Anthropic; here it is the *client* that does.
//
// Everything downstream of toOpenAIRequest — alias resolution, fallback, retry,
// quotas, caching, usage accounting — is unchanged, so an Anthropic client gets
// the same routing as an OpenAI one.

// messages implements POST /v1/messages, the Anthropic-dialect entry point. It
// shares every middleware, quota, allowlist and cache path with
// chatCompletions; only the wire format on either end differs.
func (h *handler) messages(w http.ResponseWriter, r *http.Request) {
	raw, ok := h.readBody(w, r)
	if !ok {
		return
	}

	var req anthropic.Request
	if err := json.Unmarshal(raw, &req); err != nil {
		writeDecodeError(w, err, "Malformed JSON body.")
		return
	}
	if req.Model == "" {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "Field 'model' is required.")
		return
	}
	if len(req.Messages) == 0 {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "Field 'messages' is required.")
		return
	}
	if req.MaxTokens <= 0 {
		// Unlike OpenAI, this dialect makes the cap mandatory.
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "Field 'max_tokens' is required.")
		return
	}
	if !allowModel(w, r, req.Model) {
		return
	}
	cleanup := h.trackActive(r, req.Model)
	defer cleanup()

	// Routing is dialect-agnostic, but the trace records which dialect the
	// client spoke so the dashboard can separate the two populations.
	r = r.WithContext(router.WithDialect(r.Context(), storage.DialectAnthropic))
	converted := toOpenAIRequest(&req)
	if req.Stream {
		w.Header().Set(cacheHeader, headerPass)
		h.streamMessages(w, r, converted)
		return
	}

	key, served := h.lookupCache(w, endpointMessages, req.Model, raw)
	if served {
		return
	}

	resp, err := h.engine.ChatCompletion(r.Context(), converted)
	if err != nil {
		h.writeEngineError(w, r, err)
		return
	}

	body, err := json.Marshal(toAnthropicResponse(resp))
	if err != nil {
		h.logger.Error("encode message", slog.Any("error", err))
		writeAnthropicError(w, http.StatusInternalServerError, "api_error", "Failed to encode the upstream response.")
		return
	}
	h.storeCache(endpointMessages, req.Model, key, body)
	writeBody(w, body)
}

// streamMessages relays a completion as an Anthropic event stream. Like the
// OpenAI path, no headers are committed until the first frame, so a fallback
// failure is still reported as a status code.
func (h *handler) streamMessages(w http.ResponseWriter, r *http.Request, req *openai.ChatCompletionRequest) {
	sse, err := stream.NewWriter(w)
	if err != nil {
		writeAnthropicError(w, http.StatusInternalServerError, "api_error", "Streaming is unsupported by the transport.")
		return
	}

	chunks, err := h.engine.ChatCompletionStream(r.Context(), req)
	if err != nil {
		h.writeEngineError(w, r, err)
		return
	}

	h.metrics.StreamConnections.Inc()
	defer h.metrics.StreamConnections.Dec()

	if err := relayMessages(r.Context(), sse, req.Model, chunks, stream.DefaultHeartbeat); err != nil {
		if errors.Is(err, context.Canceled) {
			h.logger.Debug("client disconnected mid-stream", slog.String("model", req.Model))
			return
		}
		h.logger.Error("stream aborted", slog.String("model", req.Model), slog.Any("error", err))
	}
}

// --- request translation ---

// toOpenAIRequest converts an Anthropic request. The system prompt becomes a
// leading system message, which is where the OpenAI dialect expects it.
func toOpenAIRequest(req *anthropic.Request) *openai.ChatCompletionRequest {
	out := &openai.ChatCompletionRequest{
		Model:       req.Model,
		Stream:      req.Stream,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stop:        req.StopSeqs,
		Messages:    make([]openai.Message, 0, len(req.Messages)+1),
	}
	if req.MaxTokens > 0 {
		limit := req.MaxTokens
		out.MaxTokens = &limit
	}
	if system := anthropic.SystemText(req.System); system != "" {
		out.Messages = append(out.Messages, openai.Message{
			Role:    "system",
			Content: mustMarshal(system),
		})
	}
	for _, m := range req.Messages {
		out.Messages = append(out.Messages, toOpenAIMessages(m)...)
	}

	if tools := toOpenAITools(req.Tools); tools != nil {
		out.Extra = map[string]json.RawMessage{"tools": tools}
	}
	if choice := toOpenAIToolChoice(req.ToolChoice); choice != nil && out.Extra != nil {
		out.Extra["tool_choice"] = choice
	}
	return out
}

// toOpenAIMessages converts one Anthropic turn. It returns a slice because a
// single user turn carrying tool results expands into one OpenAI tool message
// per result, which is how that dialect models them.
func toOpenAIMessages(m anthropic.Message) []openai.Message {
	blocks, ok := decodeBlocks(m.Content)
	if !ok {
		// String content, or a shape this gateway does not model: pass the body
		// through rather than corrupt it.
		return []openai.Message{{Role: m.Role, Content: m.Content}}
	}

	var (
		out   []openai.Message
		parts []json.RawMessage
		calls []json.RawMessage
		text  strings.Builder
	)
	for _, b := range blocks {
		switch b.Type {
		case "text":
			text.WriteString(b.Text)
			parts = append(parts, mustMarshal(map[string]any{"type": "text", "text": b.Text}))
		case "image":
			if url := imageURLOf(b.Source); url != "" {
				parts = append(parts, mustMarshal(map[string]any{
					"type":      "image_url",
					"image_url": map[string]string{"url": url},
				}))
			}
		case "tool_use":
			calls = append(calls, mustMarshal(map[string]any{
				"index": len(calls),
				"id":    b.ID,
				"type":  "function",
				"function": map[string]string{
					"name":      b.Name,
					"arguments": argumentsOf(b.Input),
				},
			}))
		case "tool_result":
			// Keep structured tool output intact so RTK can rewrite text
			// parts without destroying images or other typed blocks.
			out = append(out, openai.Message{
				Role:            "tool",
				Content:         toolResultContent(b.Content),
				ToolResultError: b.IsError,
				Extra:           map[string]json.RawMessage{"tool_call_id": mustMarshal(b.ToolUseID)},
			})
		}
	}

	if len(calls) > 0 {
		msg := openai.Message{
			Role:  "assistant",
			Extra: map[string]json.RawMessage{"tool_calls": mustMarshal(calls)},
		}
		// OpenAI requires content on an assistant message even when the turn is
		// only tool calls; null is the accepted empty form.
		if text.Len() > 0 {
			msg.Content = mustMarshal(text.String())
		}
		return append(out, msg)
	}
	if len(parts) > 0 {
		// A turn that is only text keeps the compact string form; mixed content
		// needs the array so the image parts survive.
		content := mustMarshal(parts)
		if len(parts) == 1 && text.Len() > 0 {
			content = mustMarshal(text.String())
		}
		return append(out, openai.Message{Role: m.Role, Content: content})
	}
	return out
}

// imageURLOf renders an Anthropic image source as the URL an OpenAI client
// sends: inline bytes become a data URL, a referenced image stays a URL.
func imageURLOf(src *anthropic.Source) string {
	if src == nil {
		return ""
	}
	switch src.Type {
	case "base64":
		if src.Data == "" {
			return ""
		}
		mediaType := src.MediaType
		if mediaType == "" {
			mediaType = "image/png"
		}
		return "data:" + mediaType + ";base64," + src.Data
	case "url":
		return src.URL
	default:
		return ""
	}
}

// toolResultContent converts Anthropic tool-result blocks into Chat
// Completions content while retaining images and other structured payloads.
func toolResultContent(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return raw
	}
	blocks, ok := decodeBlocks(raw)
	if !ok {
		return raw
	}
	parts := make([]json.RawMessage, 0, len(blocks))
	for _, b := range blocks {
		switch b.Type {
		case "text":
			parts = append(parts, mustMarshal(map[string]any{"type": "text", "text": b.Text}))
		case "image":
			if url := imageURLOf(b.Source); url != "" {
				parts = append(parts, mustMarshal(map[string]any{"type": "image_url", "image_url": map[string]string{"url": url}}))
			}
		default:
			return raw
		}
	}
	if len(parts) == 0 {
		return raw
	}
	if len(parts) == 1 {
		var p struct{ Type, Text string }
		if json.Unmarshal(parts[0], &p) == nil && p.Type == "text" {
			return mustMarshal(p.Text)
		}
	}
	return mustMarshal(parts)
}

// toolResultText flattens a tool result for compatibility with older callers.
func toolResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

// toOpenAITools converts the declared tools. The two dialects carry the same
// information; only the nesting differs.
func toOpenAITools(tools []anthropic.Tool) json.RawMessage {
	if len(tools) == 0 {
		return nil
	}
	out := make([]json.RawMessage, 0, len(tools))
	for _, t := range tools {
		if t.Name == "" {
			continue
		}
		fn := map[string]any{"name": t.Name}
		if t.Description != "" {
			fn["description"] = t.Description
		}
		if len(t.InputSchema) > 0 {
			fn["parameters"] = t.InputSchema
		}
		out = append(out, mustMarshal(map[string]any{"type": "function", "function": fn}))
	}
	if len(out) == 0 {
		return nil
	}
	return mustMarshal(out)
}

// toOpenAIToolChoice converts tool_choice. Anthropic's "any" — the model must
// call some tool — is OpenAI's "required".
func toOpenAIToolChoice(choice *anthropic.ToolChoice) json.RawMessage {
	if choice == nil {
		return nil
	}
	switch choice.Type {
	case "auto":
		return mustMarshal("auto")
	case "none":
		return mustMarshal("none")
	case "any":
		return mustMarshal("required")
	case "tool":
		if choice.Name == "" {
			return nil
		}
		return mustMarshal(map[string]any{
			"type":     "function",
			"function": map[string]string{"name": choice.Name},
		})
	default:
		return nil
	}
}

// --- response translation ---

// toAnthropicResponse converts a completed OpenAI response back to the shape an
// Anthropic client expects.
func toAnthropicResponse(resp *openai.ChatCompletionResponse) *anthropic.Response {
	out := &anthropic.Response{
		ID:      resp.ID,
		Type:    "message",
		Role:    "assistant",
		Model:   resp.Model,
		Content: []anthropic.ContentBlock{},
		Usage:   toAnthropicUsage(resp.Usage),
	}
	if len(resp.Choices) == 0 {
		return out
	}

	choice := resp.Choices[0]
	if choice.Message != nil {
		if text, ok := choice.Message.Text(); ok && text != "" {
			out.Content = append(out.Content, anthropic.ContentBlock{Type: "text", Text: text})
		}
		out.Content = append(out.Content, toolUseBlocks(choice.Message.Extra["tool_calls"])...)
	}
	if choice.FinishReason != nil && *choice.FinishReason != "" {
		reason := stopReason(*choice.FinishReason)
		out.StopReason = &reason
	}
	return out
}

// toolUseBlocks converts an assistant message's tool_calls into the tool_use
// blocks Anthropic carries inline in the content array.
func toolUseBlocks(raw json.RawMessage) []anthropic.ContentBlock {
	if len(raw) == 0 {
		return nil
	}
	var calls []struct {
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &calls); err != nil {
		return nil
	}

	out := make([]anthropic.ContentBlock, 0, len(calls))
	for _, c := range calls {
		input := json.RawMessage(c.Function.Arguments)
		if !json.Valid(input) {
			input = json.RawMessage(`{}`)
		}
		out = append(out, anthropic.ContentBlock{
			Type:  "tool_use",
			ID:    c.ID,
			Name:  c.Function.Name,
			Input: input,
		})
	}
	return out
}

// toAnthropicUsage maps OpenAI token accounting onto the Anthropic fields.
// Cached prompt tokens are reported separately there and must not be counted
// twice, so they are subtracted from the input total.
func toAnthropicUsage(u *openai.Usage) *anthropic.Usage {
	if u == nil {
		return nil
	}
	cached, written := u.Cached(), u.CacheWrite()
	input := u.PromptTokens - cached - written
	if input < 0 {
		input = 0
	}
	return &anthropic.Usage{
		InputTokens:              input,
		OutputTokens:             u.CompletionTokens,
		CacheReadInputTokens:     cached,
		CacheCreationInputTokens: written,
	}
}

// stopReason maps an OpenAI finish reason onto the Anthropic vocabulary. It is
// the inverse of provider.finishReason.
func stopReason(reason string) string {
	switch reason {
	case "length":
		return "max_tokens"
	case "tool_calls", "function_call":
		return "tool_use"
	case "":
		return ""
	default:
		return "end_turn"
	}
}

// --- shared helpers ---

// decodeBlocks decodes an Anthropic content array. The second result is false
// for string content and for any shape that is not an array of blocks.
func decodeBlocks(raw json.RawMessage) ([]anthropic.ContentBlock, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var blocks []anthropic.ContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, false
	}
	return blocks, true
}

// argumentsOf renders a tool input object as the JSON *string* OpenAI clients
// parse. An absent input becomes an empty object rather than a null.
func argumentsOf(input json.RawMessage) string {
	if len(input) == 0 || string(input) == "null" {
		return "{}"
	}
	return string(input)
}

// mustMarshal encodes a value that cannot fail to encode: every caller passes
// maps, slices, strings and structs built from already-valid JSON.
func mustMarshal(v any) json.RawMessage {
	out, err := json.Marshal(v)
	if err != nil {
		// Unreachable for the shapes above; degrade to a literal rather than
		// panicking inside a request path.
		return json.RawMessage(strconv.Quote(fmt.Sprintf("encode error: %v", err)))
	}
	return out
}
