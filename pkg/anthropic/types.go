// Package anthropic contains Anthropic Messages API wire types shared by the
// provider that calls an Anthropic upstream and the endpoint that accepts an
// Anthropic client. It holds data definitions only, no behaviour.
//
// The router speaks the OpenAI shape internally, so both directions translate
// through it: an Anthropic-dialect provider converts OpenAI to these types on
// the way out, and the /v1/messages endpoint converts these types to OpenAI on
// the way in.
package anthropic

import (
	"encoding/json"
	"strings"
)

// Version is the wire version every Anthropic-compatible endpoint requires in
// the anthropic-version header.
const Version = "2023-06-01"

// Request mirrors POST /v1/messages.
//
// System stays raw because the API accepts both a plain string and an array of
// text blocks; use SystemText to read either form.
type Request struct {
	Model       string          `json:"model"`
	Messages    []Message       `json:"messages"`
	System      json.RawMessage `json:"system,omitempty"`
	MaxTokens   int             `json:"max_tokens"`
	Stream      bool            `json:"stream,omitempty"`
	Temperature *float64        `json:"temperature,omitempty"`
	TopP        *float64        `json:"top_p,omitempty"`
	StopSeqs    []string        `json:"stop_sequences,omitempty"`
	Tools       []Tool          `json:"tools,omitempty"`
	ToolChoice  *ToolChoice     `json:"tool_choice,omitempty"`
}

// Message is one turn of the conversation. Content is either a string or an
// array of typed blocks, so it stays raw.
type Message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// Response mirrors a non-streaming POST /v1/messages response.
type Response struct {
	ID      string         `json:"id"`
	Type    string         `json:"type,omitempty"`
	Role    string         `json:"role,omitempty"`
	Model   string         `json:"model"`
	Content []ContentBlock `json:"content"`
	// StopReason is null while a message is still streaming, so it is a
	// pointer: an SDK distinguishes "not finished" from a reason of "".
	StopReason *string `json:"stop_reason"`
	StopSeq    *string `json:"stop_sequence,omitempty"`
	Usage      *Usage  `json:"usage,omitempty"`
}

// ContentBlock is one entry of a message's content array. The API models every
// block kind as a discriminated union on Type, so the union is flattened here
// and the fields irrelevant to a given kind stay empty.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`

	// Tool-use blocks carry the call the model wants made.
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// Tool-result blocks carry the outcome sent back by the client. Content is
	// raw because the API accepts a string or an array of blocks.
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`

	// Image blocks carry the bytes inline or reference them by URL.
	Source *Source `json:"source,omitempty"`
}

// Source carries inline or referenced binary content of an image block.
// Anthropic takes base64 payloads inline and remote images by URL.
type Source struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

// Tool is one entry of the request's tools array.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// ToolChoice selects how the model may use the declared tools.
type ToolChoice struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

// Usage reports token accounting. Cache fields are absent on upstreams that do
// not implement prompt caching.
type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// Event is one decoded SSE payload of a streaming response. Like ContentBlock
// it flattens a union, since a stream interleaves several event shapes.
type Event struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
	Delta *Delta `json:"delta,omitempty"`

	ContentBlock *ContentBlock `json:"content_block,omitempty"`
	Message      *Response     `json:"message,omitempty"`
	Usage        *Usage        `json:"usage,omitempty"`
}

// Delta carries the incremental part of a streaming event: text and tool
// arguments on content_block_delta, the stop reason on message_delta.
type Delta struct {
	Type        string `json:"type,omitempty"`
	Text        string `json:"text,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
	StopReason  string `json:"stop_reason,omitempty"`
}

// SystemText flattens a system prompt into a single string, joining the text
// blocks of the array form. Non-text blocks carry no prompt and are skipped.
func SystemText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}

	var blocks []ContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// ErrorResponse is the error envelope an Anthropic client expects. It differs
// from the OpenAI envelope in carrying a type discriminator at both levels.
type ErrorResponse struct {
	Type  string `json:"type"`
	Error Error  `json:"error"`
}

// Error is the body of an ErrorResponse.
type Error struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}
