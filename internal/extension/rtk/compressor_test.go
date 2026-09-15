package rtk

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/zealish/zealish-router/pkg/openai"
)

func textMessage(role, text string) openai.Message {
	content, _ := json.Marshal(text)
	return openai.Message{Role: role, Content: content}
}

func mustText(t *testing.T, m openai.Message) string {
	t.Helper()
	s, ok := m.Text()
	if !ok {
		t.Fatalf("message content is not a string")
	}
	return s
}

func repeatLines(line string, n int) string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = line
	}
	return strings.Join(lines, "\n")
}

func TestCompressSummarizesTestRollCall(t *testing.T) {
	var b strings.Builder
	b.WriteString("Here is the test run:\n")
	for range 30 {
		b.WriteString("--- PASS: TestSomething/case\n")
	}
	b.WriteString("done, please review")

	res := Compress([]openai.Message{textMessage("user", b.String())})
	if res.CompressedMessages != 1 {
		t.Fatalf("compressed %d messages, want 1", res.CompressedMessages)
	}
	out := mustText(t, res.Messages[0])
	if !strings.Contains(out, "[rtk:test-summary 30 passed") {
		t.Fatalf("missing test summary: %q", out)
	}
	if !strings.Contains(out, "Here is the test run:") || !strings.Contains(out, "done, please review") {
		t.Fatalf("surrounding prose lost: %q", out)
	}
}

func TestCompressSummarizesDiff(t *testing.T) {
	var b strings.Builder
	b.WriteString("diff --git a/foo.go b/foo.go\n")
	b.WriteString("index 123..456 100644\n")
	b.WriteString("--- a/foo.go\n")
	b.WriteString("+++ b/foo.go\n")
	b.WriteString("@@ -1,10 +1,12 @@\n")
	for range 20 {
		b.WriteString("+added line\n")
	}
	for range 10 {
		b.WriteString("-removed line\n")
	}
	text := "check this change:\n" + b.String()

	res := Compress([]openai.Message{textMessage("user", text)})
	out := mustText(t, res.Messages[0])
	if !strings.Contains(out, "[rtk:diff-summary 1 file(s)]") {
		t.Fatalf("missing diff summary: %q", out)
	}
	if !strings.Contains(out, "foo.go: 1 hunk(s), +20/-10") {
		t.Fatalf("wrong diff stats: %q", out)
	}
}

func TestCompressSummarizesTerminalLog(t *testing.T) {
	log := repeatLines("2026-09-15T10:00:00 INFO server listening on :8787", 40)
	res := Compress([]openai.Message{textMessage("user", "logs:\n"+log)})
	out := mustText(t, res.Messages[0])
	if !strings.Contains(out, "[rtk:terminal-log") {
		t.Fatalf("missing log summary: %q", out)
	}
	if len(out) >= len("logs:\n")+len(log) {
		t.Fatalf("no shrinkage: %d bytes", len(out))
	}
}

func TestCompressKeepsErrorLogLines(t *testing.T) {
	lines := make([]string, 0, 41)
	for range 20 {
		lines = append(lines, "2026-09-15T10:00:00 INFO ok")
	}
	lines = append(lines, "2026-09-15T10:00:01 ERROR database connection refused")
	for range 20 {
		lines = append(lines, "2026-09-15T10:00:02 INFO ok")
	}
	res := Compress([]openai.Message{textMessage("user", strings.Join(lines, "\n")+"\npadding so the message passes the length gate")})
	out := mustText(t, res.Messages[0])
	if !strings.Contains(out, "ERROR database connection refused") {
		t.Fatalf("error line was elided: %q", out)
	}
}

func TestCompressSummarizesStackTrace(t *testing.T) {
	var b strings.Builder
	b.WriteString("it crashed:\n")
	for range 25 {
		b.WriteString("    at Object.fn (/app/node_modules/lib/index.js:10:5)\n")
	}
	res := Compress([]openai.Message{textMessage("user", b.String())})
	out := mustText(t, res.Messages[0])
	if !strings.Contains(out, "[rtk:stack-trace") {
		t.Fatalf("missing stack summary: %q", out)
	}
}

func TestCompressNeverTouchesSystemPrompt(t *testing.T) {
	log := "instructions\n" + repeatLines("--- PASS: TestX", 30)
	msg := textMessage("system", log)
	res := Compress([]openai.Message{msg})
	if res.CompressedMessages != 0 {
		t.Fatalf("system prompt was compressed")
	}
	if string(res.Messages[0].Content) != string(msg.Content) {
		t.Fatalf("system content changed")
	}
}

func TestCompressNeverTouchesJSONPayload(t *testing.T) {
	payload := map[string]any{"key": repeatLines("--- PASS: TestX", 40)}
	raw, _ := json.Marshal(payload)
	msg := textMessage("tool", string(raw))
	res := Compress([]openai.Message{msg})
	if res.CompressedMessages != 0 {
		t.Fatalf("JSON payload was compressed")
	}
}

func TestCompressNeverTouchesToolCalls(t *testing.T) {
	msg := textMessage("assistant", repeatLines("--- PASS: TestX", 30))
	msg.Extra = map[string]json.RawMessage{"tool_calls": json.RawMessage(`[]`)}
	res := Compress([]openai.Message{msg})
	if res.CompressedMessages != 0 {
		t.Fatalf("message with tool_calls was compressed")
	}
}

func TestCompressLeavesShortMessagesAlone(t *testing.T) {
	msg := textMessage("user", "hello, how are you?")
	res := Compress([]openai.Message{msg})
	if res.CompressedMessages != 0 || res.SavedBytes != 0 {
		t.Fatalf("short message was rewritten")
	}
}

func TestCompressSummarizesTree(t *testing.T) {
	var b strings.Builder
	b.WriteString("project layout:\n")
	for range 30 {
		b.WriteString("├── src/file.go\n")
	}
	res := Compress([]openai.Message{textMessage("user", b.String())})
	out := mustText(t, res.Messages[0])
	if !strings.Contains(out, "[rtk:tree") {
		t.Fatalf("missing tree summary: %q", out)
	}
}
