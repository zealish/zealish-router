package router

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/zealish/zealish-router/internal/pricing"
	"github.com/zealish/zealish-router/internal/storage"
	"github.com/zealish/zealish-router/pkg/openai"
)

// charsPerToken is the rough ratio used when an upstream omits usage. It is a
// deliberate approximation: token metrics are for capacity trends, not billing.
const charsPerToken = 4

// usage is a token count attributed to one completed request. Cached, cacheWrite
// and reasoning are reported only by upstreams that break usage down; they are
// subsets of prompt (cached, cacheWrite) and completion (reasoning).
type usage struct {
	prompt     int
	completion int
	cached     int
	cacheWrite int
	reasoning  int
}

// fromReported converts an upstream usage report into internal counts.
func fromReported(r *openai.Usage) usage {
	return usage{
		prompt:     r.PromptTokens,
		completion: r.CompletionTokens,
		cached:     r.Cached(),
		cacheWrite: r.CacheWrite(),
		reasoning:  r.Reasoning(),
	}
}

// recordUsage reports token counts for a route. Zero counts are skipped.
func (e *Engine) recordUsage(route Route, u usage, streamed bool, started time.Time) {
	if u.prompt == 0 && u.completion == 0 {
		return
	}
	if e.recorder != nil {
		e.recorder.RecordTokens(route.Provider, route.Model, u.prompt, u.completion)
	}
	e.persistUsage(route, u, streamed, "ok", started)
}

// recordFailure appends a failed request to the usage log so lifetime totals
// count every hit on a model, not only the successful ones. The status column
// carries the failure class; token counts stay zero.
func (e *Engine) recordFailure(route Route, streamed bool, status string, started time.Time) {
	e.persistUsage(route, usage{}, streamed, status, started)
}

// persistUsage prices the request and appends it to the usage log. Metrics are
// in-process and reset on restart, so the durable log is what the dashboard
// reads. A logging failure must never fail the request that produced it.
func (e *Engine) persistUsage(route Route, u usage, streamed bool, status string, started time.Time) {
	if e.usage == nil {
		return
	}

	now := time.Now().UTC()
	event := storage.UsageEvent{
		CreatedAt:        now,
		Alias:            route.Alias,
		Provider:         route.Provider,
		Model:            route.Model,
		Streamed:         streamed,
		Status:           status,
		Duration:         now.Sub(started),
		PromptTokens:     u.prompt,
		CompletionTokens: u.completion,
		CachedTokens:     u.cached,
		CacheWriteTokens: u.cacheWrite,
		ReasoningTokens:  u.reasoning,
		CostUSD: pricing.Cost(route.Model, pricing.Tokens{
			Prompt:     u.prompt,
			Completion: u.completion,
			Cached:     u.cached,
			CacheWrite: u.cacheWrite,
			Reasoning:  u.reasoning,
		}),
	}

	// Detached context: the caller's request may already be cancelled by the
	// time a stream finishes, and the row must still land.
	ctx, cancel := context.WithTimeout(context.Background(), usageWriteTimeout)
	defer cancel()

	if err := e.usage.Record(ctx, event); err != nil && e.logger != nil {
		e.logger.Warn("usage log write failed",
			slog.String("alias", route.Alias), slog.Any("error", err))
	}
}

// usageOf prefers the upstream's own accounting and falls back to estimating
// from message and choice text when it is absent.
func usageOf(reported *openai.Usage, req *openai.ChatCompletionRequest, choices []openai.Choice) usage {
	if reported != nil && (reported.PromptTokens > 0 || reported.CompletionTokens > 0) {
		return fromReported(reported)
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
// A chunk carrying usage wins; otherwise the deltas are estimated. A cancelled
// ctx — typically a client disconnect — ends the relay and still records what
// was streamed before the hangup.
func (e *Engine) meterStream(ctx context.Context, route Route, req *openai.ChatCompletionRequest, chunks <-chan openai.StreamChunk, started time.Time) <-chan openai.StreamChunk {
	if e.recorder == nil && e.usage == nil {
		return chunks
	}

	out := make(chan openai.StreamChunk)
	go func() {
		defer close(out)

		var (
			reported  *openai.Usage
			completed int
		)
	relay:
		for chunk := range chunks {
			if chunk.Usage != nil {
				reported = chunk.Usage
			}
			for _, c := range chunk.Choices {
				if c.Delta != nil {
					completed += contentLen(c.Delta.Content)
				}
			}
			select {
			case out <- chunk:
			case <-ctx.Done():
				// The consumer is gone; stop forwarding but still meter.
				break relay
			}
		}

		if reported != nil && (reported.PromptTokens > 0 || reported.CompletionTokens > 0) {
			e.recordUsage(route, fromReported(reported), true, started)
			return
		}
		e.recordUsage(route, usage{
			prompt:     estimatePrompt(req),
			completion: tokensFromChars(completed),
		}, true, started)
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
