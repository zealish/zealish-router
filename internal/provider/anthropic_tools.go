package provider

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/zealish/zealish-router/pkg/openai"
)

// --- OpenAI-side wire shapes ---
//
// These fields live in Message.Extra and ChatCompletionRequest.Extra, because
// the gateway passes them through untouched for OpenAI-dialect upstreams. The
// Anthropic dialect has to understand them, so it decodes them here.

// openAITool is one entry of the request's tools array.
type openAITool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters,omitempty"`
	} `json:"function"`
}

// openAIToolCall is one entry of an assistant message's tool_calls array.
type openAIToolCall struct {
	Index    *int   `json:"index,omitempty"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// openAIContentPart is one entry of array-of-parts message content.
type openAIContentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL *struct {
		URL string `json:"url"`
	} `json:"image_url,omitempty"`
}

// --- Anthropic-side wire shapes ---

// anthropicTool is one entry of the request's tools array.
type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// anthropicToolChoice selects how the model may use the declared tools.
type anthropicToolChoice struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

// anthropicSource carries inline or referenced binary content of an image
// block. Anthropic takes base64 payloads inline and remote images by URL.
type anthropicSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

// emptySchema is sent when a tool declares no parameters: Anthropic requires
// input_schema, while OpenAI treats parameters as optional.
var emptySchema = json.RawMessage(`{"type":"object","properties":{}}`)

// --- request translation ---

// toAnthropicTools converts the OpenAI tools array. Entries that are not
// function tools are dropped: Anthropic's server-side tool types are declared
// differently and a passthrough would be rejected upstream.
func toAnthropicTools(raw json.RawMessage) []anthropicTool {
	var in []openAITool
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil
	}
	out := make([]anthropicTool, 0, len(in))
	for _, t := range in {
		if t.Type != "" && t.Type != "function" {
			continue
		}
		if t.Function.Name == "" {
			continue
		}
		schema := t.Function.Parameters
		if len(schema) == 0 {
			schema = emptySchema
		}
		out = append(out, anthropicTool{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			InputSchema: schema,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// toAnthropicToolChoice converts tool_choice, which OpenAI expresses either as
// a string ("auto", "none", "required") or as an object naming one function.
func toAnthropicToolChoice(raw json.RawMessage) *anthropicToolChoice {
	var name string
	if err := json.Unmarshal(raw, &name); err == nil {
		switch name {
		case "auto":
			return &anthropicToolChoice{Type: "auto"}
		case "none":
			return &anthropicToolChoice{Type: "none"}
		case "required", "any":
			return &anthropicToolChoice{Type: "any"}
		default:
			return nil
		}
	}

	var obj struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil || obj.Function.Name == "" {
		return nil
	}
	return &anthropicToolChoice{Type: "tool", Name: obj.Function.Name}
}

// toAnthropicContent converts one OpenAI message body into Anthropic content
// blocks. A plain string stays a string, so simple conversations keep the
// compact wire form; array-of-parts content becomes typed blocks.
func toAnthropicContent(content json.RawMessage) json.RawMessage {
	if len(content) == 0 {
		return json.RawMessage(`""`)
	}
	var text string
	if err := json.Unmarshal(content, &text); err == nil {
		return content
	}

	var parts []openAIContentPart
	if err := json.Unmarshal(content, &parts); err != nil {
		return content // unknown shape: pass through rather than corrupt it
	}

	blocks := make([]json.RawMessage, 0, len(parts))
	for _, p := range parts {
		switch p.Type {
		case "text":
			blocks = append(blocks, mustMarshal(map[string]any{"type": "text", "text": p.Text}))
		case "image_url":
			if p.ImageURL == nil {
				continue
			}
			src := toAnthropicSource(p.ImageURL.URL)
			if src == nil {
				continue
			}
			blocks = append(blocks, mustMarshal(map[string]any{"type": "image", "source": src}))
		}
	}
	if len(blocks) == 0 {
		return json.RawMessage(`""`)
	}
	return mustMarshal(blocks)
}

// toAnthropicSource converts an OpenAI image URL. Data URLs carry the bytes
// inline and become a base64 source; anything else is passed as a URL source.
func toAnthropicSource(url string) *anthropicSource {
	if url == "" {
		return nil
	}
	if rest, ok := strings.CutPrefix(url, "data:"); ok {
		meta, data, found := strings.Cut(rest, ",")
		if !found {
			return nil
		}
		mediaType, isBase64 := strings.CutSuffix(meta, ";base64")
		if !isBase64 {
			return nil
		}
		return &anthropicSource{Type: "base64", MediaType: mediaType, Data: data}
	}
	return &anthropicSource{Type: "url", URL: url}
}

// toAnthropicAssistant converts an assistant message that carries tool calls.
// Anthropic models the calls as tool_use blocks alongside the text, rather
// than as a separate field.
func toAnthropicAssistant(m openai.Message) json.RawMessage {
	calls := decodeToolCalls(m.Extra["tool_calls"])
	if len(calls) == 0 {
		return toAnthropicContent(m.Content)
	}

	blocks := make([]json.RawMessage, 0, len(calls)+1)
	if text, ok := m.Text(); ok && text != "" {
		blocks = append(blocks, mustMarshal(map[string]any{"type": "text", "text": text}))
	}
	for _, c := range calls {
		input := json.RawMessage(c.Function.Arguments)
		if !json.Valid(input) {
			input = json.RawMessage(`{}`)
		}
		blocks = append(blocks, mustMarshal(map[string]any{
			"type":  "tool_use",
			"id":    c.ID,
			"name":  c.Function.Name,
			"input": input,
		}))
	}
	return mustMarshal(blocks)
}

// toAnthropicToolResult converts an OpenAI tool message into the user-role
// tool_result block Anthropic expects.
func toAnthropicToolResult(m openai.Message) (anthropicMessage, bool) {
	var id string
	if raw, ok := m.Extra["tool_call_id"]; ok {
		_ = json.Unmarshal(raw, &id)
	}
	if id == "" {
		return anthropicMessage{}, false
	}
	content, ok := m.Text()
	if !ok {
		content = string(m.Content)
	}
	block := mustMarshal(map[string]any{
		"type":        "tool_result",
		"tool_use_id": id,
		"content":     content,
	})
	return anthropicMessage{
		Role:    "user",
		Content: mustMarshal([]json.RawMessage{block}),
	}, true
}

// decodeToolCalls decodes an assistant message's tool_calls array.
func decodeToolCalls(raw json.RawMessage) []openAIToolCall {
	if len(raw) == 0 {
		return nil
	}
	var calls []openAIToolCall
	if err := json.Unmarshal(raw, &calls); err != nil {
		return nil
	}
	return calls
}

// --- response translation ---

// toolCallsFrom converts the tool_use blocks of a completed response into the
// OpenAI tool_calls array carried on the assistant message.
func toolCallsFrom(content []anthropicContent) json.RawMessage {
	calls := make([]openAIToolCall, 0, len(content))
	for _, c := range content {
		if c.Type != "tool_use" {
			continue
		}
		var call openAIToolCall
		idx := len(calls)
		call.Index = &idx
		call.ID = c.ID
		call.Type = "function"
		call.Function.Name = c.Name
		call.Function.Arguments = argumentsOf(c.Input)
		calls = append(calls, call)
	}
	if len(calls) == 0 {
		return nil
	}
	return mustMarshal(calls)
}

// argumentsOf renders a tool input object as the JSON *string* OpenAI clients
// parse. An absent input becomes an empty object rather than a null.
func argumentsOf(input json.RawMessage) string {
	if len(input) == 0 || string(input) == "null" {
		return "{}"
	}
	return string(input)
}

// --- streaming translation ---

// toolCallStream accumulates the tool_use blocks of a streaming response.
// Anthropic announces a block with its id and name, then streams the arguments
// as partial JSON; OpenAI expects the same information as indexed tool_calls
// deltas, so the index is tracked per content block.
type toolCallStream struct {
	index map[int]int // Anthropic content block index -> OpenAI tool call index
	next  int
}

func newToolCallStream() *toolCallStream {
	return &toolCallStream{index: make(map[int]int)}
}

// start registers a tool_use block and returns the opening delta, which
// carries the call id and function name with empty arguments.
func (s *toolCallStream) start(block int, id, name string) json.RawMessage {
	idx := s.next
	s.next++
	s.index[block] = idx

	call := openAIToolCall{ID: id, Type: "function"}
	call.Index = &idx
	call.Function.Name = name
	call.Function.Arguments = ""
	return mustMarshal([]openAIToolCall{call})
}

// delta returns the argument fragment of an in-flight tool call, or nil when
// the block is not a tool_use block this stream started.
func (s *toolCallStream) delta(block int, partial string) json.RawMessage {
	idx, ok := s.index[block]
	if !ok {
		return nil
	}
	var call openAIToolCall
	call.Index = &idx
	call.Function.Arguments = partial
	return mustMarshal([]openAIToolCall{call})
}

// mustMarshal encodes a value that cannot fail to encode: every caller passes
// maps, slices and structs built from already-valid JSON.
func mustMarshal(v any) json.RawMessage {
	out, err := json.Marshal(v)
	if err != nil {
		// Unreachable for the shapes above; degrade to a literal rather than
		// panicking inside a request path.
		return json.RawMessage(strconv.Quote(fmt.Sprintf("encode error: %v", err)))
	}
	return out
}
