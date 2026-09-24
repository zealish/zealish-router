// Package ponytail provides request-level context optimization for chat
// completions. It reduces token count through deduplication, ranking and
// compression while preserving semantic fidelity. All operations are
// heuristic — no external API or LLM calls.
package ponytail

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// estimateTokensFromMessages returns a rough token count for a slice of
// messages. The heuristic is ~4 characters per token plus a small per-message
// overhead for role framing and separators. Industry-standard approximation
// used by OpenAI's own tokenizer.
func estimateTokensFromMessages(messages []Message) int {
	total := 0
	for _, m := range messages {
		total += estimateTokensFromText(m.Role) + 4 // role + framing overhead
		total += estimateTokensFromText(extractText(m.Content))
		if m.Name != "" {
			total += estimateTokensFromText(m.Name) + 1
		}
	}
	return total
}

// estimateTokensFromText returns a rough token count for a string.
// ~4 characters per token, with a minimum of 1 for non-empty text.
func estimateTokensFromText(text string) int {
	if text == "" {
		return 0
	}
	n := utf8.RuneCountInString(text)
	tokens := (n + 3) / 4 // ceiling division
	if tokens < 1 {
		tokens = 1
	}
	return tokens
}

// extractText reads the text content from a json.RawMessage that may be a
// plain string or an array of typed parts. Returns "" on any parse failure.
func extractText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// Try plain string first.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// Array of parts: sum text values.
	var parts []struct {
		Type string          `json:"type"`
		Text string          `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return ""
	}
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Text)
	}
	return b.String()
}
