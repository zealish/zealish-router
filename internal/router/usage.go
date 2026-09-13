package router

import (
	"encoding/json"

	"github.com/zealish/zealish-router/pkg/openai"
)

// charsPerToken is the rough ratio used when an upstream omits usage. It is a
// deliberate approximation: token metrics are for capacity trends, not billing.
const charsPerToken = 4

// usage is a token count attributed to one completed request.
type usage struct {
	prompt     int
	completion int
}

// recordUsage reports token counts for a route. Zero counts are skipped.
func (e *Engine) recordUsage(route Route, u usage) {
	if e.recorder == nil || (u.prompt == 0 && u.completion == 0) {
		return
	}
	e.recorder.RecordTokens(route.Provider, route.Model, u.prompt, u.completion)
}

// usageOf prefers the upstream's own accounting and falls back to estimating
// from message and choice text when it is absent.
func usageOf(reported *openai.Usage, req *openai.ChatCompletionRequest, choices []openai.Choice) usage {
	if reported != nil && (reported.PromptTokens > 0 || reported.CompletionTokens > 0) {
		return usage{prompt: reported.PromptTokens, completion: reported.CompletionTokens}
	}

	u := usage{prompt: estimatePrompt(req)}
	for _, c := range choices {
		if c.Message != nil {
			u.completion += estimateContent(c.Message.Content)
		}
	}
	return u
}

// meterStream wraps chunks so token usage is recorded once the stream ends.
// A chunk carrying usage wins; otherwise the deltas are estimated.
func (e *Engine) meterStream(route Route, req *openai.ChatCompletionRequest, chunks <-chan openai.StreamChunk) <-chan openai.StreamChunk {
	if e.recorder == nil {
		return chunks
	}

	out := make(chan openai.StreamChunk)
	go func() {
		defer close(out)

		var (
			reported  *openai.Usage
			completed int
		)
		for chunk := range chunks {
			if chunk.Usage != nil {
				reported = chunk.Usage
			}
			for _, c := range chunk.Choices {
				if c.Delta != nil {
					completed += contentLen(c.Delta.Content)
				}
			}
			out <- chunk
		}

		if reported != nil && (reported.PromptTokens > 0 || reported.CompletionTokens > 0) {
			e.recordUsage(route, usage{prompt: reported.PromptTokens, completion: reported.CompletionTokens})
			return
		}
		e.recordUsage(route, usage{
			prompt:     estimatePrompt(req),
			completion: tokensFromChars(completed),
		})
	}()
	return out
}

func estimatePrompt(req *openai.ChatCompletionRequest) int {
	var chars int
	for _, m := range req.Messages {
		chars += contentLen(m.Content)
	}
	return tokensFromChars(chars)
}

func estimateContent(raw json.RawMessage) int {
	return tokensFromChars(contentLen(raw))
}

// contentLen measures the text of a message body. String content is measured
// exactly; array-of-parts falls back to the raw JSON length, which overcounts
// slightly but stays proportional.
func contentLen(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return len(s)
	}
	return len(raw)
}

func tokensFromChars(chars int) int {
	if chars <= 0 {
		return 0
	}
	// Round up: any content at all is at least one token.
	return (chars + charsPerToken - 1) / charsPerToken
}
