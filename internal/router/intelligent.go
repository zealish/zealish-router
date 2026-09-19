package router

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"

	"github.com/zealish/zealish-router/internal/provider"
	"github.com/zealish/zealish-router/internal/storage"
	"github.com/zealish/zealish-router/pkg/openai"
)

// RequestRequirements describes features a candidate must support.
type RequestRequirements struct {
	Vision          bool
	Tools           bool
	JSONMode        bool
	Embeddings      bool
	Audio           bool
	Streaming       bool
	RequiredContext int
}

var (
	ErrNoCompatibleModel = errors.New("NO_COMPATIBLE_MODEL")
	ErrContextExceeded   = errors.New("CONTEXT_LIMIT_EXCEEDED")
	ErrNoHealthyModel    = errors.New("NO_HEALTHY_MODEL")
)

const intelligentBaselineMs = 2000

type intelligentCandidate struct {
	alias     string
	poolOrder int
	score     float64
}

func analyzeChatRequest(req *openai.ChatCompletionRequest) RequestRequirements {
	r := RequestRequirements{Streaming: req.Stream, RequiredContext: estimateChatContext(req)}
	for _, message := range req.Messages {
		var parts []map[string]json.RawMessage
		if json.Unmarshal(message.Content, &parts) != nil {
			continue
		}
		for _, part := range parts {
			var kind string
			_ = json.Unmarshal(part["type"], &kind)
			switch kind {
			case "image", "image_url", "input_image":
				r.Vision = true
			case "audio", "input_audio", "audio_url":
				r.Audio = true
			}
		}
	}
	if raw := req.Extra["tools"]; len(raw) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		var tools []json.RawMessage
		r.Tools = json.Unmarshal(raw, &tools) == nil && len(tools) > 0
	}
	if raw := req.Extra["response_format"]; len(raw) > 0 {
		var format struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &format) == nil {
			r.JSONMode = format.Type == "json_object" || format.Type == "json_schema"
		}
	}
	return r
}

func analyzeEmbeddingRequest(_ *openai.EmbeddingRequest) RequestRequirements {
	return RequestRequirements{Embeddings: true}
}

// estimateChatContext approximates the number of tokens a provider will consume.
// It is NOT a tokenizer — tokenizers vary by model, encoding and implementation.
// This estimate counts textual request structure and reserves the requested output
// budget (max_completion_tokens or max_tokens). Binary multimodal payloads are
// deliberately not treated as prompt text; base64 data URLs do not inflate the
// estimate. For capacity planning, use actual token metrics. For routing decisions,
// prefer providers with compatible capabilities and adequate context windows.
func estimateChatContext(req *openai.ChatCompletionRequest) int {
	chars := 0
	for _, message := range req.Messages {
		chars = addContextSize(chars, len(message.Role))
		chars = addContextSize(chars, len(message.Name))
		chars = addContextSize(chars, estimateMessageContentChars(message.Content))
		for _, key := range []string{"tool_calls", "tool_call_id", "function_call", "refusal", "reasoning", "reasoning_content"} {
			chars = addContextSize(chars, len(message.Extra[key]))
		}
	}
	for _, key := range []string{"tools", "functions", "response_format"} {
		chars = addContextSize(chars, len(req.Extra[key]))
	}
	// Keep the shared rounding helper's addition away from integer overflow.
	maxChars := int(^uint(0)>>1) - (charsPerToken - 1)
	if chars > maxChars {
		chars = maxChars
	}
	return addContextSize(tokensFromChars(chars), requestedOutputTokens(req))
}

func requestedOutputTokens(req *openai.ChatCompletionRequest) int {
	if raw := req.Extra["max_completion_tokens"]; len(raw) > 0 {
		var n int
		if json.Unmarshal(raw, &n) == nil && n > 0 {
			return n
		}
	}
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		return *req.MaxTokens
	}
	return 0
}

func estimateMessageContentChars(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var parts []struct {
		Type string          `json:"type"`
		Text json.RawMessage `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return contentLen(raw)
	}
	chars := 0
	for _, part := range parts {
		switch part.Type {
		case "text", "input_text", "output_text":
			chars = addContextSize(chars, contentLen(part.Text))
		default:
			// A fixed 256-token allowance avoids counting URLs/base64 as
			// text. Actual image/audio costs depend on the provider, image
			// dimensions, audio duration and detail; this is not a guarantee
			// that an arbitrary multimodal input fits the advertised window.
			chars = addContextSize(chars, 256*charsPerToken)
		}
	}
	return chars
}

func addContextSize(total, amount int) int {
	maxInt := int(^uint(0) >> 1)
	if amount > maxInt-total {
		return maxInt
	}
	return total + amount
}

func candidateCompatible(m storage.ModelAlias, req RequestRequirements) bool {
	has := func(capability string) bool { return slices.Contains(m.Capabilities, capability) }
	return (!req.Vision || has(provider.CapVision)) &&
		(!req.Tools || has(provider.CapTools)) &&
		(!req.JSONMode || has(provider.CapJSONMode)) &&
		(!req.Embeddings || has(provider.CapEmbeddings)) &&
		(!req.Audio || has(provider.CapAudio)) &&
		(!req.Streaming || has(provider.CapStreaming)) &&
		(req.RequiredContext <= 0 || m.MaxContext <= 0 || m.MaxContext >= req.RequiredContext)
}

func capabilityCompatible(m storage.ModelAlias, req RequestRequirements) bool {
	req.RequiredContext = 0
	return candidateCompatible(m, req)
}

// intelligentOrder filters and ranks candidates. Pool order is used only for
// exact score ties.
func (rt *routes) intelligentOrder(combo storage.Combo, req RequestRequirements) ([]string, error) {
	candidates := make([]intelligentCandidate, 0, len(combo.Members))
	capable := 0
	for i, alias := range combo.Members {
		m, ok := rt.models[alias]
		if !ok || !capabilityCompatible(m, req) {
			continue
		}
		capable++
		// Zero means the context window is unknown, not zero. Keep the
		// route eligible and let the upstream enforce its actual limit.
		if req.RequiredContext > 0 && m.MaxContext > 0 && m.MaxContext < req.RequiredContext {
			continue
		}
		candidates = append(candidates, intelligentCandidate{alias: alias, poolOrder: i})
	}
	if len(candidates) == 0 {
		if capable > 0 {
			return nil, ErrContextExceeded
		}
		return nil, ErrNoCompatibleModel
	}
	if rt.engine != nil {
		rt.engine.scoreCandidates(rt, candidates)
	}
	slices.SortStableFunc(candidates, func(a, b intelligentCandidate) int {
		if a.score > b.score {
			return -1
		}
		if a.score < b.score {
			return 1
		}
		return a.poolOrder - b.poolOrder
	})
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		out = append(out, candidate.alias)
	}
	return out, nil
}

func (e *Engine) scoreCandidates(rt *routes, candidates []intelligentCandidate) {
	minLatency, maxLatency := float64(intelligentBaselineMs), float64(intelligentBaselineMs)
	minCost, maxCost := 0.0, 0.0
	for _, candidate := range candidates {
		m := rt.models[candidate.alias]
		latency := float64(intelligentBaselineMs)
		if stats, ok := e.stats.Alias(candidate.alias); ok && stats.P50Ms > 0 {
			latency = float64(stats.P50Ms)
		}
		if latency < minLatency {
			minLatency = latency
		}
		if latency > maxLatency {
			maxLatency = latency
		}
		cost := m.Pricing.Input + m.Pricing.Output
		if cost < minCost {
			minCost = cost
		}
		if cost > maxCost {
			maxCost = cost
		}
	}
	for i := range candidates {
		m := rt.models[candidates[i].alias]
		health := 50.0
		latency := float64(intelligentBaselineMs)
		if stats, ok := e.stats.Alias(candidates[i].alias); ok {
			health = stats.SuccessRate
			if stats.P50Ms > 0 {
				latency = float64(stats.P50Ms)
			}
		}
		latencyScore := normalizeLower(latency, minLatency, maxLatency)
		costScore := normalizeLower(m.Pricing.Input+m.Pricing.Output, minCost, maxCost)
		candidates[i].score = float64(m.QualityTier)*0.35 + health*0.30 + latencyScore*0.20 + costScore*0.15
	}
}

func normalizeLower(value, min, max float64) float64 {
	if max <= min {
		return 100
	}
	return (max - value) * 100 / (max - min)
}
