package rtk

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/zealish/zealish-router/pkg/openai"
)

const (
	minCompressSize = 500
	rawCap          = 10 * 1024 * 1024
)

// Result reports what a compression pass did.
type Result struct {
	// Messages aliases the input slice when nothing changed.
	Messages []openai.Message
	// SavedBytes is the total decoded text shrinkage, excluding JSON encoding.
	SavedBytes int
	// CompressedMessages counts rewritten messages, not individual text parts.
	CompressedMessages int
}

// Compress reduces supported tool-result payloads without modifying the input.
// User prose, non-tool messages and unknown content parts pass through unchanged.
func Compress(messages []openai.Message) Result {
	result := Result{Messages: messages}
	for i, message := range messages {
		var content json.RawMessage
		var saved int
		switch message.Role {
		case "tool":
			if message.ToolResultError {
				continue
			}
			content, saved = compressContent(message.Content, true)
		case "user":
			content, saved = compressContent(message.Content, false)
		default:
			continue
		}
		if saved == 0 {
			continue
		}
		if result.CompressedMessages == 0 {
			result.Messages = append([]openai.Message(nil), messages...)
		}
		result.Messages[i].Content = content
		result.SavedBytes += saved
		result.CompressedMessages++
	}
	return result
}

// compressContent accepts tool strings and parts, or a user array containing
// Claude tool_result blocks. Every decoded container is private to this call;
// opaque fields stay RawMessage so numbers and unknown shapes are not coerced.
func compressContent(raw json.RawMessage, tool bool) (json.RawMessage, int) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return raw, 0
	}
	if trimmed[0] == '"' && tool {
		var text string
		if json.Unmarshal(trimmed, &text) != nil {
			return raw, 0
		}
		out, saved := compressText(text)
		if saved == 0 {
			return raw, 0
		}
		encoded, err := json.Marshal(out)
		if err != nil {
			return raw, 0
		}
		return encoded, saved
	}
	if trimmed[0] != '[' {
		return raw, 0
	}
	var parts []json.RawMessage
	if json.Unmarshal(trimmed, &parts) != nil {
		return raw, 0
	}
	saved := 0
	for i, part := range parts {
		var fields map[string]json.RawMessage
		if json.Unmarshal(part, &fields) != nil || fields == nil {
			continue
		}
		var kind string
		if json.Unmarshal(fields["type"], &kind) != nil {
			continue
		}
		key := ""
		switch {
		case tool && (kind == "text" || kind == "input_text"):
			key = "text"
		case !tool && kind == "tool_result":
			if bytes.Equal(bytes.TrimSpace(fields["is_error"]), []byte("true")) {
				continue
			}
			key = "content"
		default:
			continue
		}
		// Text fields must be strings; only tool_result content admits arrays.
		value := bytes.TrimSpace(fields[key])
		if key == "text" && (len(value) == 0 || value[0] != '"') {
			continue
		}
		out, delta := compressContent(value, true)
		if delta == 0 {
			continue
		}
		fields[key] = out
		encoded, err := json.Marshal(fields)
		if err != nil {
			continue
		}
		parts[i] = encoded
		saved += delta
	}
	if saved == 0 {
		return raw, 0
	}
	encoded, err := json.Marshal(parts)
	if err != nil {
		return raw, 0
	}
	return encoded, saved
}

// compressText applies byte gates and fails open for structured JSON, filter
// panics, empty output, and output that does not strictly shrink the payload.
func compressText(text string) (out string, saved int) {
	out = text
	if len(text) < minCompressSize || len(text) > rawCap {
		return out, 0
	}
	trimmed := strings.TrimSpace(text)
	if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') && json.Valid([]byte(trimmed)) {
		return out, 0
	}
	defer func() {
		if recover() != nil {
			out, saved = text, 0
		}
	}()
	filtered := filterText(text)
	if filtered == "" || len(filtered) >= len(text) {
		return out, 0
	}
	return filtered, len(text) - len(filtered)
}
