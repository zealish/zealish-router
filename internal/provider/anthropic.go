package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/zealish/zealish-router/pkg/anthropic"
	"github.com/zealish/zealish-router/pkg/openai"
)

// defaultMaxTokens is sent when the client omits max_tokens: Anthropic rejects
// a request without it, while OpenAI treats it as optional.
const defaultMaxTokens = 4096

// Anthropic talks to an Anthropic-compatible upstream (Anthropic itself,
// Command Code, or any gateway exposing /messages). Requests and responses are
// translated to and from the OpenAI shape the router speaks internally.
type Anthropic struct {
	httpProvider
}

// NewAnthropic constructs an Anthropic-compatible provider. OAuth tokens keep
// the bearer header; plain API keys go into x-api-key, as the upstream expects.
func NewAnthropic(opts Options) *Anthropic {
	if opts.Name == "" {
		opts.Name = "anthropic"
	}
	if opts.AuthHeader == "" && !isBearerToken(opts.APIKey) {
		opts.AuthHeader = "x-api-key"
	}
	headers := map[string]string{"anthropic-version": anthropic.Version}
	return &Anthropic{httpProvider: newHTTPProvider(opts, headers)}
}

// isBearerToken reports whether the credential looks like an OAuth access
// token rather than an Anthropic API key.
func isBearerToken(key string) bool {
	return strings.HasPrefix(key, "sk-ant-oat") || strings.HasPrefix(key, "oauth-")
}

// --- translation ---

// toAnthropic converts an OpenAI request. System messages are hoisted into the
// top-level system field, which is where Anthropic expects them.
func toAnthropic(req *openai.ChatCompletionRequest, stream bool) *anthropic.Request {
	out := &anthropic.Request{
		Model:       req.Model,
		Stream:      stream,
		MaxTokens:   defaultMaxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		StopSeqs:    req.Stop,
		Messages:    make([]anthropic.Message, 0, len(req.Messages)),
	}
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		out.MaxTokens = *req.MaxTokens
	}

	if raw, ok := req.Extra["tools"]; ok {
		out.Tools = toAnthropicTools(raw)
	}
	if raw, ok := req.Extra["tool_choice"]; ok && len(out.Tools) > 0 {
		out.ToolChoice = toAnthropicToolChoice(raw)
	}

	var system []string
	for _, m := range req.Messages {
		switch m.Role {
		case "system", "developer":
			if text, ok := m.Text(); ok {
				system = append(system, text)
			}
		case "tool", "function":
			if msg, ok := toAnthropicToolResult(m); ok {
				out.Messages = append(out.Messages, msg)
			}
		case "assistant":
			out.Messages = append(out.Messages, anthropic.Message{
				Role:    "assistant",
				Content: toAnthropicAssistant(m),
			})
		default:
			out.Messages = append(out.Messages, anthropic.Message{
				Role:    m.Role,
				Content: toAnthropicContent(m.Content),
			})
		}
	}
	if len(system) > 0 {
		out.System = mustMarshal(strings.Join(system, "\n\n"))
	}
	return out
}

// fromAnthropic converts a completed response back to the OpenAI shape.
func fromAnthropic(resp *anthropic.Response) *openai.ChatCompletionResponse {
	var text strings.Builder
	for _, c := range resp.Content {
		if c.Type == "text" {
			text.WriteString(c.Text)
		}
	}
	content, _ := json.Marshal(text.String())
	var reason string
	if resp.StopReason != nil {
		reason = finishReason(*resp.StopReason)
	}

	msg := &openai.Message{Role: "assistant", Content: content}
	if calls := toolCallsFrom(resp.Content); calls != nil {
		msg.Extra = map[string]json.RawMessage{"tool_calls": calls}
	}

	return &openai.ChatCompletionResponse{
		ID:     resp.ID,
		Object: "chat.completion",
		Model:  resp.Model,
		Choices: []openai.Choice{{
			Index:        0,
			Message:      msg,
			FinishReason: &reason,
		}},
		Usage: fromAnthropicUsage(resp.Usage),
	}
}

// fromAnthropicUsage maps Anthropic token accounting onto the OpenAI fields,
// keeping the cache breakdown the pricing layer bills on.
func fromAnthropicUsage(u *anthropic.Usage) *openai.Usage {
	if u == nil {
		return nil
	}
	out := &openai.Usage{
		PromptTokens:     u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens,
		CompletionTokens: u.OutputTokens,
	}
	out.TotalTokens = out.PromptTokens + out.CompletionTokens
	if u.CacheReadInputTokens > 0 || u.CacheCreationInputTokens > 0 {
		out.PromptTokensDetails = &openai.PromptTokensDetails{
			CachedTokens:     u.CacheReadInputTokens,
			CacheWriteTokens: u.CacheCreationInputTokens,
		}
	}
	return out
}

// finishReason maps an Anthropic stop reason onto the OpenAI vocabulary.
func finishReason(reason string) string {
	switch reason {
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "":
		return ""
	default:
		return "stop"
	}
}

// --- Provider implementation ---

// ChatCompletion performs a non-streaming completion against /messages.
func (p *Anthropic) ChatCompletion(ctx context.Context, req *openai.ChatCompletionRequest) (*openai.ChatCompletionResponse, error) {
	payload, err := json.Marshal(toAnthropic(req, false))
	if err != nil {
		return nil, &Error{Provider: p.opts.Name, Message: fmt.Sprintf("encode request: %v", err)}
	}

	resp, err := p.do(ctx, "messages", payload, false)
	if err != nil {
		return nil, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	var out anthropic.Response
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, &Error{Provider: p.opts.Name, Message: fmt.Sprintf("decode response: %v", err)}
	}
	return fromAnthropic(&out), nil
}

// ChatCompletionStream opens an SSE stream against /messages and translates
// each event into an OpenAI chunk.
func (p *Anthropic) ChatCompletionStream(ctx context.Context, req *openai.ChatCompletionRequest) (<-chan openai.StreamChunk, error) {
	payload, err := json.Marshal(toAnthropic(req, true))
	if err != nil {
		return nil, &Error{Provider: p.opts.Name, Message: fmt.Sprintf("encode request: %v", err)}
	}

	// The body is closed by the pump goroutine below, which bodyclose cannot see.
	resp, err := p.do(ctx, "messages", payload, true) //nolint:bodyclose
	if err != nil {
		return nil, err
	}

	out := make(chan openai.StreamChunk)
	go func() {
		defer close(out)
		defer func() { _ = resp.Body.Close() }()
		pumpAnthropic(ctx, resp.Body, req.Model, out)
	}()
	return out, nil
}

// Embeddings is not part of the Anthropic API. The request is rejected rather
// than translated, so a misrouted alias fails loudly instead of hitting a
// nonexistent path.
func (p *Anthropic) Embeddings(context.Context, *openai.EmbeddingRequest) (*openai.EmbeddingResponse, error) {
	return nil, &Error{
		Provider: p.opts.Name,
		Kind:     ErrUnsupported,
		Message:  "the Anthropic API has no embeddings endpoint",
	}
}

// pumpAnthropic translates an Anthropic SSE stream into OpenAI chunks. Token
// usage is carried on the terminating chunk so the metering layer sees it in
// the same place as with an OpenAI upstream.
func pumpAnthropic(ctx context.Context, body io.Reader, model string, out chan<- openai.StreamChunk) {
	var (
		id    string
		usage = &anthropic.Usage{}
		tools = newToolCallStream()
	)

	emit := func(chunk openai.StreamChunk) bool {
		chunk.Object = "chat.completion.chunk"
		chunk.ID = id
		chunk.Model = model
		select {
		case out <- chunk:
			return true
		case <-ctx.Done():
			return false
		}
	}

	scanSSE(body, func(_, payload string) bool {
		var ev anthropic.Event
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			return true // skip malformed event, keep the stream alive
		}

		switch ev.Type {
		case "message_start":
			if ev.Message != nil {
				id = ev.Message.ID
				if ev.Message.Model != "" {
					model = ev.Message.Model
				}
				mergeAnthropicUsage(usage, ev.Message.Usage)
			}
		case "content_block_start":
			if ev.ContentBlock == nil || ev.ContentBlock.Type != "tool_use" {
				return true
			}
			calls := tools.start(ev.Index, ev.ContentBlock.ID, ev.ContentBlock.Name)
			return emit(openai.StreamChunk{Choices: []openai.Choice{{
				Index: 0,
				Delta: &openai.Message{
					Role:  "assistant",
					Extra: map[string]json.RawMessage{"tool_calls": calls},
				},
			}}})
		case "content_block_delta":
			if ev.Delta == nil {
				return true
			}
			if ev.Delta.Type == "input_json_delta" {
				calls := tools.delta(ev.Index, ev.Delta.PartialJSON)
				if calls == nil {
					return true
				}
				return emit(openai.StreamChunk{Choices: []openai.Choice{{
					Index: 0,
					Delta: &openai.Message{Extra: map[string]json.RawMessage{"tool_calls": calls}},
				}}})
			}
			if ev.Delta.Text == "" {
				return true
			}
			text, _ := json.Marshal(ev.Delta.Text)
			return emit(openai.StreamChunk{Choices: []openai.Choice{{
				Index: 0,
				Delta: &openai.Message{Role: "assistant", Content: text},
			}}})
		case "message_delta":
			mergeAnthropicUsage(usage, ev.Usage)
			reason := ""
			if ev.Delta != nil {
				reason = finishReason(ev.Delta.StopReason)
			}
			return emit(openai.StreamChunk{
				Choices: []openai.Choice{{Index: 0, Delta: &openai.Message{}, FinishReason: &reason}},
				Usage:   fromAnthropicUsage(usage),
			})
		case "message_stop":
			return false
		case "error":
			return false
		}
		return true
	})
}

// mergeAnthropicUsage folds a partial usage report into the running total;
// Anthropic reports input tokens at message_start and output at message_delta.
func mergeAnthropicUsage(dst *anthropic.Usage, src *anthropic.Usage) {
	if src == nil {
		return
	}
	if src.InputTokens > 0 {
		dst.InputTokens = src.InputTokens
	}
	if src.OutputTokens > 0 {
		dst.OutputTokens = src.OutputTokens
	}
	if src.CacheReadInputTokens > 0 {
		dst.CacheReadInputTokens = src.CacheReadInputTokens
	}
	if src.CacheCreationInputTokens > 0 {
		dst.CacheCreationInputTokens = src.CacheCreationInputTokens
	}
}
