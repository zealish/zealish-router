package sanitize

import "github.com/zealish/zealish-router/pkg/openai"

// DedupMessages drops a message whose role and content are byte-identical to
// the immediately preceding one. System prompts and messages carrying Extra
// fields (tool calls, tool results) are never dropped.
func DedupMessages(messages []openai.Message) ([]openai.Message, int) {
	var out []openai.Message
	removed := 0

	for i, m := range messages {
		if i > 0 && duplicate(messages[i-1], m) {
			if out == nil {
				out = append([]openai.Message(nil), messages[:i]...)
			}
			removed++
			continue
		}
		if out != nil {
			out = append(out, m)
		}
	}

	if out == nil {
		return messages, 0
	}
	return out, removed
}

func duplicate(prev, cur openai.Message) bool {
	if cur.Role == "system" || cur.Role == "developer" {
		return false
	}
	if len(cur.Extra) > 0 || len(prev.Extra) > 0 {
		return false
	}
	return prev.Role == cur.Role && string(prev.Content) == string(cur.Content)
}
