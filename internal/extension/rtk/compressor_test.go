package rtk

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/zealish/zealish-router/pkg/openai"
)

func message(role string, content any) openai.Message {
	raw, _ := json.Marshal(content)
	return openai.Message{Role: role, Content: raw}
}

func decoded(t *testing.T, raw json.RawMessage) any {
	t.Helper()
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func repetitiveToolText() string {
	return strings.Repeat("2026-09-15T10:00:00 INFO server listening on :8787\n", 40)
}

func TestCompressToolStringCopiesAndShrinks(t *testing.T) {
	original := message("tool", repetitiveToolText())
	before := append([]byte(nil), original.Content...)
	result := Compress([]openai.Message{original})
	if result.CompressedMessages != 1 || result.SavedBytes <= 0 {
		t.Fatalf("result = %+v, want one successful compression", result)
	}
	if bytes.Equal(result.Messages[0].Content, before) {
		t.Fatal("compressed content did not change")
	}
	if !bytes.Equal(original.Content, before) {
		t.Fatal("input message was mutated")
	}
	if result.Messages[0].Role != "tool" {
		t.Fatal("message role changed")
	}
}

func TestCompressToolPartsPreservesUnknownFields(t *testing.T) {
	content := []any{
		map[string]any{"type": "text", "text": repetitiveToolText(), "vendor": "keep"},
		map[string]any{"type": "image", "source": map[string]any{"x": 1}},
	}
	result := Compress([]openai.Message{message("tool", content)})
	if result.CompressedMessages != 1 {
		t.Fatalf("compressed %d messages, want 1", result.CompressedMessages)
	}
	parts, ok := decoded(t, result.Messages[0].Content).([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("parts shape changed: %#v", parts)
	}
	if parts[0].(map[string]any)["vendor"] != "keep" || parts[1].(map[string]any)["type"] != "image" {
		t.Fatal("unknown fields or parts were not preserved")
	}
}

func TestCompressClaudeToolResultsAndErrors(t *testing.T) {
	content := []any{
		map[string]any{"type": "tool_result", "tool_use_id": "ok", "content": repetitiveToolText()},
		map[string]any{"type": "tool_result", "tool_use_id": "err", "is_error": true, "content": repetitiveToolText()},
		map[string]any{"type": "text", "text": repetitiveToolText()},
	}
	original, _ := json.Marshal(content)
	result := Compress([]openai.Message{message("user", content)})
	if result.CompressedMessages != 1 {
		t.Fatalf("compressed %d messages, want 1", result.CompressedMessages)
	}
	parts := decoded(t, result.Messages[0].Content).([]any)
	if parts[1].(map[string]any)["content"] != repetitiveToolText() {
		t.Fatal("error tool result was modified")
	}
	if parts[2].(map[string]any)["text"] != repetitiveToolText() {
		t.Fatal("ordinary user prose was modified")
	}
	if !bytes.Equal(message("user", content).Content, original) {
		t.Fatal("input content changed")
	}
}

func TestCompressGatesAndProtocolPayloads(t *testing.T) {
	cases := []openai.Message{
		message("tool", strings.Repeat("x", minCompressSize-1)),
		message("tool", strings.Repeat("x", rawCap+1)),
		message("tool", map[string]any{"log": repetitiveToolText()}),
		message("system", repetitiveToolText()),
		{Role: "tool", Content: mustJSON(repetitiveToolText()), ToolResultError: true},
	}
	result := Compress(cases)
	if result.CompressedMessages != 0 || result.SavedBytes != 0 {
		t.Fatalf("unsupported payloads changed: %+v", result)
	}
	if !bytes.Equal(result.Messages[0].Content, cases[0].Content) {
		t.Fatal("no-op result changed original values")
	}
}

func TestCompressEmptyOrGrowingFilterFailsOpen(t *testing.T) {
	for _, text := range []string{"", "short", strings.Repeat("x", minCompressSize)} {
		result := Compress([]openai.Message{message("tool", text)})
		if result.CompressedMessages != 0 || result.SavedBytes != 0 {
			t.Fatalf("text %q unexpectedly changed: %+v", text, result)
		}
	}
}

func mustJSON(value any) json.RawMessage {
	raw, _ := json.Marshal(value)
	return raw
}
