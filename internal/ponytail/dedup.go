package ponytail

import (
	"crypto/sha256"
	"encoding/json"
)

// Message is a lightweight alias for the openai Message type used internally
// by ponytail. It mirrors the fields ponytail operates on.
type Message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content,omitempty"`
	Name    string          `json:"name,omitempty"`
}

// dedupMessages removes redundant messages from a conversation:
//   - Exact duplicate messages (same role + content hash) are removed, keeping the first occurrence.
//   - Consecutive identical system prompts are merged (keep the first, drop repeats).
//   - Repeated assistant outputs are collapsed (keep the newest).
//   - User messages are NEVER removed.
//   - Original ordering is preserved.
func dedupMessages(messages []Message) []Message {
	if len(messages) <= 1 {
		return messages
	}

	// Pass 1: collapse consecutive identical system prompts.
	pass1 := make([]Message, 0, len(messages))
	for i, m := range messages {
		if m.Role == "system" && i > 0 && messages[i-1].Role == "system" {
			if messageHash(m) == messageHash(messages[i-1]) {
				continue // skip repeat
			}
		}
		pass1 = append(pass1, m)
	}

	// Pass 2: deduplicate by content hash, keeping the first occurrence.
	// User messages are always kept.
	seen := make(map[string]bool)
	pass2 := make([]Message, 0, len(pass1))
	for _, m := range pass1 {
		if m.Role == "user" {
			pass2 = append(pass2, m)
			continue
		}
		h := messageHash(m)
		if seen[h] {
			continue
		}
		seen[h] = true
		pass2 = append(pass2, m)
	}

	// Pass 3: collapse repeated assistant messages, keeping only the most
	// recent of each unique content. Walk backwards to find the newest, then
	// rebuild forward.
	return collapseAssistantRepeats(pass2)
}

// collapseAssistantRepeats keeps only the last occurrence of each unique
// assistant message (by content hash). Non-assistant messages and the
// relative order of other messages are preserved.
func collapseAssistantRepeats(messages []Message) []Message {
	// Build the set of assistant hashes to keep: the last occurrence of each.
	lastAssistantIdx := make(map[string]int)
	for i, m := range messages {
		if m.Role == "assistant" {
			lastAssistantIdx[messageHash(m)] = i
		}
	}

	result := make([]Message, 0, len(messages))
	for i, m := range messages {
		if m.Role == "assistant" {
			h := messageHash(m)
			if lastAssistantIdx[h] != i {
				continue // not the last occurrence
			}
		}
		result = append(result, m)
	}
	return result
}

// messageHash returns a SHA-256 hex digest of role + content. This is used
// for deduplication comparisons, not for security.
func messageHash(m Message) string {
	h := sha256.New()
	h.Write([]byte(m.Role))
	h.Write([]byte{0})
	h.Write(m.Content)
	return string(h.Sum(nil))
}
