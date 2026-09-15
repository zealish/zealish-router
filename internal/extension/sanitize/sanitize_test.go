package sanitize

import (
	"encoding/json"
	"testing"

	"github.com/zealish/zealish-router/pkg/openai"
)

func textMsg(role, text string) openai.Message {
	content, _ := json.Marshal(text)
	return openai.Message{Role: role, Content: content}
}

func mustText(t *testing.T, m openai.Message) string {
	t.Helper()
	text, ok := m.Text()
	if !ok {
		t.Fatalf("message content is not a string: %s", m.Content)
	}
	return text
}

func TestTrimWhitespaceCollapsesTrailingAndBlankRuns(t *testing.T) {
	in := []openai.Message{textMsg("user", "hi there   \n\n\n\nbye")}
	out, saved := TrimWhitespace(in)
	if saved == 0 {
		t.Fatal("saved = 0, want > 0")
	}
	if got := mustText(t, out[0]); got != "hi there\n\nbye" {
		t.Errorf("trimmed = %q", got)
	}
}

func TestTrimWhitespaceSkipsSystemAndDeveloper(t *testing.T) {
	sys := textMsg("system", "keep   \n\n\n\n")
	dev := textMsg("developer", "also keep   \n\n\n\n")
	out, saved := TrimWhitespace([]openai.Message{sys, dev})
	if saved != 0 {
		t.Errorf("saved = %d, want 0", saved)
	}
	if string(out[0].Content) != string(sys.Content) || string(out[1].Content) != string(dev.Content) {
		t.Error("system/developer messages were rewritten")
	}
}

func TestTrimWhitespaceNoopReturnsSameSlice(t *testing.T) {
	in := []openai.Message{textMsg("user", "already clean")}
	out, saved := TrimWhitespace(in)
	if saved != 0 {
		t.Errorf("saved = %d, want 0", saved)
	}
	if len(out) != 1 || mustText(t, out[0]) != "already clean" {
		t.Errorf("unexpected rewrite: %+v", out)
	}
}

func TestWindowHistoryKeepsLastN(t *testing.T) {
	msgs := []openai.Message{
		textMsg("system", "sys"),
		textMsg("user", "1"),
		textMsg("assistant", "2"),
		textMsg("user", "3"),
		textMsg("assistant", "4"),
	}
	out, dropped := WindowHistory(msgs, 2)
	if dropped != 2 {
		t.Fatalf("dropped = %d, want 2", dropped)
	}
	if len(out) != 3 {
		t.Fatalf("len(out) = %d, want 3", len(out))
	}
	if mustText(t, out[0]) != "sys" {
		t.Error("system prompt was dropped by windowing")
	}
	if mustText(t, out[1]) != "3" || mustText(t, out[2]) != "4" {
		t.Errorf("window kept wrong messages: %+v", out)
	}
}

func TestWindowHistoryDisabledAtZero(t *testing.T) {
	msgs := []openai.Message{textMsg("user", "1"), textMsg("user", "2")}
	out, dropped := WindowHistory(msgs, 0)
	if dropped != 0 || len(out) != 2 {
		t.Errorf("WindowHistory(0) dropped %d, len %d, want 0 and 2", dropped, len(out))
	}
}

func TestWindowHistoryNeverOpensOnOrphanToolResult(t *testing.T) {
	msgs := []openai.Message{
		textMsg("user", "1"),
		textMsg("assistant", "2"),
		textMsg("tool", "result"),
		textMsg("user", "3"),
	}
	out, _ := WindowHistory(msgs, 1)
	// Window starting at the tool message must advance past it.
	if mustText(t, out[0]) == "result" {
		t.Error("window opened on an orphaned tool result")
	}
}

func TestDedupMessagesDropsConsecutiveDuplicates(t *testing.T) {
	msgs := []openai.Message{
		textMsg("user", "same"),
		textMsg("user", "same"),
		textMsg("user", "different"),
	}
	out, removed := DedupMessages(msgs)
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if len(out) != 2 {
		t.Fatalf("len(out) = %d, want 2", len(out))
	}
}

func TestDedupMessagesNeverDropsSystemPrompt(t *testing.T) {
	msgs := []openai.Message{textMsg("system", "same"), textMsg("system", "same")}
	out, removed := DedupMessages(msgs)
	if removed != 0 || len(out) != 2 {
		t.Errorf("removed = %d, len = %d, want 0 and 2", removed, len(out))
	}
}
func TestDedupMessagesNeverDropsToolCalls(t *testing.T) {
	m := textMsg("assistant", "same")
	m.Extra = map[string]json.RawMessage{"tool_calls": json.RawMessage(`[{"id":"1"}]`)}
	dup := m
	out, removed := DedupMessages([]openai.Message{m, dup})
	if removed != 0 || len(out) != 2 {
		t.Errorf("removed = %d, len = %d, want 0 and 2 (tool calls must survive)", removed, len(out))
	}
}
