// Package openai contains OpenAI-compatible wire types shared by the API layer
// and provider implementations. It holds data definitions only, no behaviour.
package openai

import "encoding/json"

// Message is a single chat message in a conversation.
//
// Content stays as raw JSON: the OpenAI schema allows either a string or an
// array of typed parts, and a gateway has no reason to re-encode either form.
// Use Text to read the string variant.
type Message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content,omitempty"`
	Name    string          `json:"name,omitempty"`
}

// Text returns the message content when it is a plain string. The second
// result is false for array-of-parts content or an absent body.
func (m Message) Text() (string, bool) {
	if len(m.Content) == 0 {
		return "", false
	}
	var s string
	if err := json.Unmarshal(m.Content, &s); err != nil {
		return "", false
	}
	return s, true
}

// StreamOptions tunes streaming responses.
type StreamOptions struct {
	// IncludeUsage asks the upstream to emit a final chunk carrying token usage.
	IncludeUsage bool `json:"include_usage,omitempty"`
}

// ChatCompletionRequest mirrors POST /v1/chat/completions.
type ChatCompletionRequest struct {
	Model         string         `json:"model"`
	Messages      []Message      `json:"messages"`
	Stream        bool           `json:"stream,omitempty"`
	StreamOptions *StreamOptions `json:"stream_options,omitempty"`
	Temperature   *float64       `json:"temperature,omitempty"`
	TopP          *float64       `json:"top_p,omitempty"`
	MaxTokens     *int           `json:"max_tokens,omitempty"`
	Stop          []string       `json:"stop,omitempty"`
	User          string         `json:"user,omitempty"`
}

// Choice is a completion alternative returned by a provider.
type Choice struct {
	Index        int      `json:"index"`
	Message      *Message `json:"message,omitempty"`
	Delta        *Message `json:"delta,omitempty"`
	FinishReason *string  `json:"finish_reason"`
}

// Usage reports token accounting for a completion.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ChatCompletionResponse mirrors a non-streaming completion response.
type ChatCompletionResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   *Usage   `json:"usage,omitempty"`
}

// StreamChunk is one Server-Sent Event payload of a streaming completion.
type StreamChunk struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   *Usage   `json:"usage,omitempty"`
}

// Model describes an entry of GET /v1/models.
type Model struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created,omitempty"`
	OwnedBy string `json:"owned_by,omitempty"`
}

// ModelList is the response body of GET /v1/models.
type ModelList struct {
	Object string  `json:"object"`
	Data   []Model `json:"data"`
}

// Error is the OpenAI error envelope body.
type Error struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Param   string `json:"param,omitempty"`
	Code    string `json:"code,omitempty"`
}

// ErrorResponse wraps Error as returned to clients.
type ErrorResponse struct {
	Error Error `json:"error"`
}
