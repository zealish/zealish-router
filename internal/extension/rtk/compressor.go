package rtk

import (
	"encoding/json"
	"strings"

	"github.com/zealish/zealish-router/pkg/openai"
)

// minRunLines is the smallest run worth summarizing. Below it, the summary
// marker would cost as much as the original text.
const minRunLines = 8

// Result reports what a compression pass did.
type Result struct {
	// Messages is the request message list after compression. It aliases the
	// input slice when nothing changed.
	Messages []openai.Message
	// SavedBytes is the total content shrinkage across all messages.
	SavedBytes int
	// CompressedMessages counts the messages that were rewritten.
	CompressedMessages int
}

// Compress applies semantic token reduction to a message list.
//
// Invariants — RTK must never change what the client asked for:
//   - system/developer messages pass through untouched;
//   - messages with structured (array-of-parts) content pass through, so
//     tool payloads and multimodal parts are never rewritten;
//   - messages carrying tool_calls or a tool_call_id (in Extra) pass through,
//     keeping the function-call protocol byte-identical;
//   - only detected verbose regions (logs, diffs, listings, traces, test
//     roll calls) are replaced; the surrounding prose is preserved verbatim.
func Compress(messages []openai.Message) Result {
	res := Result{Messages: messages}
	var out []openai.Message

	for i, m := range messages {
		if !compressible(m) {
			if out != nil {
				out = append(out, m)
			}
			continue
		}
		text, _ := m.Text()
		compressed, saved := compressText(text)
		if saved <= 0 {
			if out != nil {
				out = append(out, m)
			}
			continue
		}
		if out == nil {
			out = append([]openai.Message(nil), messages[:i]...)
		}
		nm := m
		nm.Content, _ = json.Marshal(compressed)
		out = append(out, nm)
		res.SavedBytes += saved
		res.CompressedMessages++
	}

	if out != nil {
		res.Messages = out
	}
	return res
}

// compressible reports whether RTK may rewrite this message at all.
func compressible(m openai.Message) bool {
	if m.Role == "system" || m.Role == "developer" {
		return false
	}
	if _, ok := m.Extra["tool_calls"]; ok {
		return false
	}
	if _, ok := m.Extra["tool_call_id"]; ok && m.Role != "tool" {
		return false
	}
	text, ok := m.Text()
	if !ok || len(text) < 256 {
		return false
	}
	// A body that parses as JSON is a payload, not prose; never rewrite it.
	trimmed := strings.TrimSpace(text)
	if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') && json.Valid([]byte(trimmed)) {
		return false
	}
	return true
}

// compressText walks the text line by line, replacing every detected verbose
// run with its summary. It returns the new text and the bytes saved.
func compressText(text string) (string, int) {
	lines := strings.Split(text, "\n")
	var out []string
	changed := false

	for i := 0; i < len(lines); {
		if n := detectDiff(lines, i); n >= minRunLines {
			out = append(out, summarizeDiff(lines[i:i+n]))
			i += n
			changed = true
			continue
		}
		if n := detectStackTrace(lines, i); n >= minRunLines {
			out = append(out, summarizeStackTrace(lines[i:i+n]))
			i += n
			changed = true
			continue
		}
		if n := detectTestLog(lines, i); n >= minRunLines {
			out = append(out, summarizeTestLog(lines[i:i+n]))
			i += n
			changed = true
			continue
		}
		if n := detectTree(lines, i); n >= minRunLines {
			out = append(out, summarizeTree(lines[i:i+n]))
			i += n
			changed = true
			continue
		}
		if n := detectTerminalLog(lines, i); n >= minRunLines {
			out = append(out, summarizeRun("terminal-log", lines[i:i+n]))
			i += n
			changed = true
			continue
		}
		out = append(out, lines[i])
		i++
	}

	if !changed {
		return text, 0
	}
	next := strings.Join(out, "\n")
	saved := len(text) - len(next)
	if saved <= 0 {
		return text, 0
	}
	return next, saved
}
