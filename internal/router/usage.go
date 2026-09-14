package router

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/zealish/zealish-router/internal/auth"
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

// callMeta is the per-request context the usage log needs but routing does
// not: who authenticated the call, when it started, and the trace collecting
// its attempts.
type callMeta struct {
	keyID   string
	started time.Time
	// trace is nil when the request carries no gateway id, which is what
	// keeps tracing free for callers that never enabled it.
	trace *trace
}

// beginRequest builds the per-request metadata and opens its trace. An
// unauthenticated call — auth disabled or a static key — carries no key id and
// stays unattributed.
func beginRequest(ctx context.Context, model string, streamed bool) callMeta {
	m := callMeta{started: time.Now()}
	if id, ok := auth.FromContext(ctx); ok && id.KeyID != "static" && id.KeyID != "anonymous" {
		m.keyID = id.KeyID
	}
	m.trace = newTrace(ctx, model, streamed, m)
	return m
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
func (e *Engine) recordUsage(route Route, u usage, streamed bool, meta callMeta) {
	if u.prompt == 0 && u.completion == 0 {
		return
	}
	if e.recorder != nil {
		e.recorder.RecordTokens(route.Provider, route.Model, u.prompt, u.completion)
	}
	e.persistUsage(route, u, streamed, "ok", meta)
}

// recordFailure appends a failed request to the usage log so lifetime totals
// count every hit on a model, not only the successful ones. The status column
// carries the failure class; token counts stay zero.
func (e *Engine) recordFailure(route Route, streamed bool, status string, meta callMeta) {
	e.persistUsage(route, usage{}, streamed, status, meta)
}

// persistUsage prices the request and appends it to the usage log. Metrics are
// in-process and reset on restart, so the durable log is what the dashboard
// reads. A logging failure must never fail the request that produced it.
func (e *Engine) persistUsage(route Route, u usage, streamed bool, status string, meta callMeta) {
	now := time.Now().UTC()
	cost := pricing.Cost(route.Model, pricing.Tokens{
		Prompt:     u.prompt,
		Completion: u.completion,
		Cached:     u.cached,
		CacheWrite: u.cacheWrite,
		Reasoning:  u.reasoning,
	})
	// The trace carries its own totals, so it is metered even when the usage
	// log is disabled.
	meta.trace.meter(u.prompt+u.completion, cost)

	if e.usage == nil {
		return
	}

	event := storage.UsageEvent{
		CreatedAt:        now,
		KeyID:            meta.keyID,
		Alias:            route.Alias,
		Provider:         route.Provider,
		Model:            route.Model,
		Streamed:         streamed,
		Status:           status,
		Duration:         now.Sub(meta.started),
		PromptTokens:     u.prompt,
		CompletionTokens: u.completion,
		CachedTokens:     u.cached,
		CacheWriteTokens: u.cacheWrite,
		ReasoningTokens:  u.reasoning,
		CostUSD:          cost,
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

// embeddingUsage prefers the upstream's own accounting and falls back to
// estimating from the input text. Embeddings produce no completion tokens.
func embeddingUsage(reported *openai.Usage, req *openai.EmbeddingRequest) usage {
	if reported != nil && reported.PromptTokens > 0 {
		return fromReported(reported)
	}
	return usage{prompt: estimateContent(req.Input)}
}

// meterStream wraps chunks so token usage is recorded once the stream ends.
// A chunk carrying usage wins; otherwise the deltas are estimated. A cancelled
// ctx — typically a client disconnect — ends the relay and still records what
// was streamed before the hangup.
func (e *Engine) meterStream(ctx context.Context, route Route, req *openai.ChatCompletionRequest, chunks <-chan openai.StreamChunk, meta callMeta) <-chan openai.StreamChunk {
	if e.recorder == nil && e.usage == nil && meta.trace == nil {
		return chunks
	}

	out := make(chan openai.StreamChunk)
	go func() {
		defer close(out)

		var (
			reported  *openai.Usage
			completed int
			hungUp    bool
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
				hungUp = true
				break relay
			}
		}

		if reported != nil && (reported.PromptTokens > 0 || reported.CompletionTokens > 0) {
			e.recordUsage(route, fromReported(reported), true, meta)
		} else {
			e.recordUsage(route, usage{
				prompt:     estimatePrompt(req),
				completion: tokensFromChars(completed),
			}, true, meta)
		}

		// The trace closes with the relay, not when the channel was handed
		// back: a stream's outcome is only known once its last token landed.
		status := "ok"
		if hungUp {
			status = "canceled"
		}
		e.finishTrace(meta.trace, status)
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
