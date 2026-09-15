package openai

import (
	"encoding/json"
	"strconv"
)

// The Responses API is OpenAI's newer dialect. It carries the same
// conversation as Chat Completions but models it as a list of typed *items*
// rather than messages with a content union: a turn, a tool call and a tool
// result are all items of the same array, discriminated on Type.
//
// The router speaks Chat Completions internally, so these types exist only at
// the edge. internal/api/responses.go translates in both directions.

// ResponseRequest mirrors POST /v1/responses.
//
// Input stays raw because the API accepts a bare string as well as an array of
// input items; use InputItems to read either form.
type ResponseRequest struct {
	Model           string          `json:"model"`
	Input           json.RawMessage `json:"input"`
	Instructions    string          `json:"instructions,omitempty"`
	MaxOutputTokens *int            `json:"max_output_tokens,omitempty"`
	Stream          bool            `json:"stream,omitempty"`
	Temperature     *float64        `json:"temperature,omitempty"`
	TopP            *float64        `json:"top_p,omitempty"`
	Tools           []ResponseTool  `json:"tools,omitempty"`
	ToolChoice      json.RawMessage `json:"tool_choice,omitempty"`
	User            string          `json:"user,omitempty"`

	// PreviousResponseID chains a request onto a stored response. The gateway
	// keeps no response state, so it is read only to reject the request
	// rather than silently dropping the conversation history it stands for.
	PreviousResponseID string `json:"previous_response_id,omitempty"`
}

// ResponseTool is one entry of a Responses request's tools array. The dialect
// flattens what Chat Completions nests under "function".
type ResponseTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

// ResponseItem is one entry of the input or output array. The API models every
// item kind as a union on Type, so the union is flattened here and the fields
// irrelevant to a given kind stay empty.
type ResponseItem struct {
	Type string `json:"type,omitempty"`
	ID   string `json:"id,omitempty"`

	// Message items carry a conversational turn. Content is raw because it is
	// either a string or an array of typed parts.
	Role    string          `json:"role,omitempty"`
	Content json.RawMessage `json:"content,omitempty"`

	// Function-call items carry the call the model wants made. Arguments is a
	// JSON *string*, exactly as in Chat Completions.
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`

	// Function-call-output items carry the result the client sends back.
	// Output is raw because the API accepts a string or a structured body.
	Output json.RawMessage `json:"output,omitempty"`

	Status string `json:"status,omitempty"`
}

// ResponseContentPart is one entry of a message item's content array.
type ResponseContentPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`

	// Image parts reference the image by URL, a data URL included.
	ImageURL string `json:"image_url,omitempty"`
	Detail   string `json:"detail,omitempty"`

	// Annotations is always present on an output_text part: an SDK iterates
	// it without a nil check, so the empty array is serialised rather than
	// omitted.
	Annotations []json.RawMessage `json:"annotations"`
}

// Response mirrors a non-streaming POST /v1/responses response.
type Response struct {
	ID        string         `json:"id"`
	Object    string         `json:"object"`
	CreatedAt int64          `json:"created_at"`
	Status    string         `json:"status"`
	Model     string         `json:"model"`
	Output    []ResponseItem `json:"output"`
	Usage     *ResponseUsage `json:"usage,omitempty"`

	// IncompleteDetails explains a status of "incomplete"; it is null on a
	// response that ran to completion, which SDKs check for explicitly.
	IncompleteDetails *IncompleteDetails `json:"incomplete_details"`
	Error             *Error             `json:"error"`

	Instructions    string          `json:"instructions,omitempty"`
	MaxOutputTokens *int            `json:"max_output_tokens,omitempty"`
	Temperature     *float64        `json:"temperature,omitempty"`
	TopP            *float64        `json:"top_p,omitempty"`
	Tools           []ResponseTool  `json:"tools"`
	ToolChoice      json.RawMessage `json:"tool_choice,omitempty"`
}

// IncompleteDetails names why a response stopped short of a natural end.
type IncompleteDetails struct {
	Reason string `json:"reason"`
}

// ResponseUsage is the Responses spelling of token accounting: inputs and
// outputs rather than prompt and completion.
type ResponseUsage struct {
	InputTokens         int                  `json:"input_tokens"`
	InputTokensDetails  *InputTokensDetails  `json:"input_tokens_details,omitempty"`
	OutputTokens        int                  `json:"output_tokens"`
	OutputTokensDetails *OutputTokensDetails `json:"output_tokens_details,omitempty"`
	TotalTokens         int                  `json:"total_tokens"`
}

// InputTokensDetails breaks down input tokens by cache disposition.
type InputTokensDetails struct {
	CachedTokens     int `json:"cached_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
}

// OutputTokensDetails breaks down output tokens.
type OutputTokensDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

// InputItems normalises the request input into items. A bare string is the
// shorthand for a single user message, which is how the API documents it.
// The second result is false for a shape this gateway does not model.
func (r *ResponseRequest) InputItems() ([]ResponseItem, bool) {
	if len(r.Input) == 0 {
		return nil, false
	}

	var text string
	if err := json.Unmarshal(r.Input, &text); err == nil {
		return []ResponseItem{{
			Type:    "message",
			Role:    "user",
			Content: json.RawMessage(strconv.Quote(text)),
		}}, true
	}

	var items []ResponseItem
	if err := json.Unmarshal(r.Input, &items); err != nil {
		return nil, false
	}
	return items, true
}
