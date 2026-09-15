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

	// Extra carries wire fields this gateway does not model — tool_calls,
	// tool_call_id, refusal, reasoning and provider extensions. They are
	// round-tripped verbatim.
	Extra map[string]json.RawMessage `json:"-"`
}

// UnmarshalJSON decodes a message, capturing unmodelled fields into Extra.
func (m *Message) UnmarshalJSON(data []byte) error {
	type alias Message
	ex, err := decodeExtras[Message](data, (*alias)(m))
	if err != nil {
		return err
	}
	m.Extra = ex
	return nil
}

// MarshalJSON encodes the message, folding Extra back into the object.
func (m Message) MarshalJSON() ([]byte, error) {
	type alias Message
	return encodeExtras(alias(m), m.Extra)
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

	// Extra carries request fields this gateway does not model — tools,
	// tool_choice, response_format, reasoning_effort and provider extensions.
	// Dropping them silently changes what the client asked for.
	Extra map[string]json.RawMessage `json:"-"`
}

// UnmarshalJSON decodes a request, capturing unmodelled fields into Extra.
func (r *ChatCompletionRequest) UnmarshalJSON(data []byte) error {
	type alias ChatCompletionRequest
	ex, err := decodeExtras[ChatCompletionRequest](data, (*alias)(r))
	if err != nil {
		return err
	}
	r.Extra = ex
	return nil
}

// MarshalJSON encodes the request, folding Extra back into the object.
func (r ChatCompletionRequest) MarshalJSON() ([]byte, error) {
	type alias ChatCompletionRequest
	return encodeExtras(alias(r), r.Extra)
}

// Choice is a completion alternative returned by a provider.
type Choice struct {
	Index        int      `json:"index"`
	Message      *Message `json:"message,omitempty"`
	Delta        *Message `json:"delta,omitempty"`
	FinishReason *string  `json:"finish_reason"`

	// Extra carries unmodelled choice fields such as logprobs.
	Extra map[string]json.RawMessage `json:"-"`
}

// UnmarshalJSON decodes a choice, capturing unmodelled fields into Extra.
func (c *Choice) UnmarshalJSON(data []byte) error {
	type alias Choice
	ex, err := decodeExtras[Choice](data, (*alias)(c))
	if err != nil {
		return err
	}
	c.Extra = ex
	return nil
}

// MarshalJSON encodes the choice, folding Extra back into the object.
func (c Choice) MarshalJSON() ([]byte, error) {
	type alias Choice
	return encodeExtras(alias(c), c.Extra)
}

// Usage reports token accounting for a completion. Upstreams vary in how much
// detail they report: the details structs are absent on providers that only
// return the three top-level counts.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`

	PromptTokensDetails     *PromptTokensDetails     `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *CompletionTokensDetails `json:"completion_tokens_details,omitempty"`
}

// PromptTokensDetails breaks down prompt tokens by cache disposition.
type PromptTokensDetails struct {
	CachedTokens     int `json:"cached_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
}

// CompletionTokensDetails breaks down completion tokens.
type CompletionTokensDetails struct {
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
}

// Cached returns the number of prompt tokens served from cache.
func (u *Usage) Cached() int {
	if u == nil || u.PromptTokensDetails == nil {
		return 0
	}
	return u.PromptTokensDetails.CachedTokens
}

// CacheWrite returns the number of prompt tokens written to cache.
func (u *Usage) CacheWrite() int {
	if u == nil || u.PromptTokensDetails == nil {
		return 0
	}
	return u.PromptTokensDetails.CacheWriteTokens
}

// Reasoning returns the number of reasoning tokens billed at output rates.
func (u *Usage) Reasoning() int {
	if u == nil || u.CompletionTokensDetails == nil {
		return 0
	}
	return u.CompletionTokensDetails.ReasoningTokens
}

// ChatCompletionResponse mirrors a non-streaming completion response.
type ChatCompletionResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   *Usage   `json:"usage,omitempty"`

	// Extra carries unmodelled top-level fields such as system_fingerprint.
	Extra map[string]json.RawMessage `json:"-"`
}

// UnmarshalJSON decodes a response, capturing unmodelled fields into Extra.
func (r *ChatCompletionResponse) UnmarshalJSON(data []byte) error {
	type alias ChatCompletionResponse
	ex, err := decodeExtras[ChatCompletionResponse](data, (*alias)(r))
	if err != nil {
		return err
	}
	r.Extra = ex
	return nil
}

// MarshalJSON encodes the response, folding Extra back into the object.
func (r ChatCompletionResponse) MarshalJSON() ([]byte, error) {
	type alias ChatCompletionResponse
	return encodeExtras(alias(r), r.Extra)
}

// StreamChunk is one Server-Sent Event payload of a streaming completion.
type StreamChunk struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   *Usage   `json:"usage,omitempty"`

	// Extra carries unmodelled top-level fields such as system_fingerprint.
	Extra map[string]json.RawMessage `json:"-"`
}

// UnmarshalJSON decodes a chunk, capturing unmodelled fields into Extra.
func (c *StreamChunk) UnmarshalJSON(data []byte) error {
	type alias StreamChunk
	ex, err := decodeExtras[StreamChunk](data, (*alias)(c))
	if err != nil {
		return err
	}
	c.Extra = ex
	return nil
}

// MarshalJSON encodes the chunk, folding Extra back into the object.
func (c StreamChunk) MarshalJSON() ([]byte, error) {
	type alias StreamChunk
	return encodeExtras(alias(c), c.Extra)
}

// EmbeddingRequest mirrors POST /v1/embeddings.
//
// Input stays as raw JSON: the OpenAI schema allows a string, an array of
// strings, an array of token ids, or an array of token-id arrays, and a
// gateway has no reason to re-encode any of them.
type EmbeddingRequest struct {
	Model          string          `json:"model"`
	Input          json.RawMessage `json:"input"`
	EncodingFormat string          `json:"encoding_format,omitempty"`
	Dimensions     *int            `json:"dimensions,omitempty"`
	User           string          `json:"user,omitempty"`

	// Extra carries request fields this gateway does not model.
	Extra map[string]json.RawMessage `json:"-"`
}

// UnmarshalJSON decodes a request, capturing unmodelled fields into Extra.
func (r *EmbeddingRequest) UnmarshalJSON(data []byte) error {
	type alias EmbeddingRequest
	ex, err := decodeExtras[EmbeddingRequest](data, (*alias)(r))
	if err != nil {
		return err
	}
	r.Extra = ex
	return nil
}

// MarshalJSON encodes the request, folding Extra back into the object.
func (r EmbeddingRequest) MarshalJSON() ([]byte, error) {
	type alias EmbeddingRequest
	return encodeExtras(alias(r), r.Extra)
}

// Embedding is one vector of an embeddings response. Embedding stays raw
// because encoding_format switches it between a float array and a base64
// string, and both are passed through untouched.
type Embedding struct {
	Object    string          `json:"object"`
	Index     int             `json:"index"`
	Embedding json.RawMessage `json:"embedding"`
}

// EmbeddingResponse mirrors a POST /v1/embeddings response.
type EmbeddingResponse struct {
	Object string      `json:"object"`
	Data   []Embedding `json:"data"`
	Model  string      `json:"model"`
	Usage  *Usage      `json:"usage,omitempty"`

	// Extra carries response fields this gateway does not model.
	Extra map[string]json.RawMessage `json:"-"`
}

// UnmarshalJSON decodes a response, capturing unmodelled fields into Extra.
func (r *EmbeddingResponse) UnmarshalJSON(data []byte) error {
	type alias EmbeddingResponse
	ex, err := decodeExtras[EmbeddingResponse](data, (*alias)(r))
	if err != nil {
		return err
	}
	r.Extra = ex
	return nil
}

// MarshalJSON encodes the response, folding Extra back into the object.
func (r EmbeddingResponse) MarshalJSON() ([]byte, error) {
	type alias EmbeddingResponse
	return encodeExtras(alias(r), r.Extra)
}

// Model describes an entry of GET /v1/models.
type Model struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created,omitempty"`
	OwnedBy string `json:"owned_by,omitempty"`
	// Capabilities is a gateway extension: what this route serves. It is
	// omitted for unclassified models rather than sent empty, so a client
	// cannot mistake "unknown" for "supports nothing".
	Capabilities []string `json:"capabilities,omitempty"`
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
