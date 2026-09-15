package sanitize

import "github.com/zealish/zealish-router/pkg/openai"

// WindowHistory keeps the last keep non-system messages, preserving every
// system/developer prompt in place. keep <= 0 disables windowing. Tool-call
// pairs are kept intact: a window never starts at a "tool" message, because a
// tool result without its call would break the function-call protocol.
func WindowHistory(messages []openai.Message, keep int) ([]openai.Message, int) {
	if keep <= 0 {
		return messages, 0
	}

	var convo []int // indexes of non-system messages
	for i, m := range messages {
		if m.Role != "system" && m.Role != "developer" {
			convo = append(convo, i)
		}
	}
	if len(convo) <= keep {
		return messages, 0
	}

	start := len(convo) - keep
	// Never open the window on an orphaned tool result.
	for start < len(convo) && messages[convo[start]].Role == "tool" {
		start++
	}
	if start >= len(convo) || len(convo)-start == len(convo) {
		return messages, 0
	}

	dropped := map[int]bool{}
	for _, idx := range convo[:start] {
		dropped[idx] = true
	}

	out := make([]openai.Message, 0, len(messages)-len(dropped))
	for i, m := range messages {
		if !dropped[i] {
			out = append(out, m)
		}
	}
	return out, len(dropped)
}
