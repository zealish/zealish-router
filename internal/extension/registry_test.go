package extension

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/zealish/zealish-router/internal/storage"
	"github.com/zealish/zealish-router/pkg/openai"
)

func textMsg(role, text string) openai.Message {
	content, _ := json.Marshal(text)
	return openai.Message{Role: role, Content: content}
}

func TestListDefaultsEverythingDisabled(t *testing.T) {
	r := NewRegistry(storage.NewMemory().Settings())
	list := r.List()
	if len(list) != 2 {
		t.Fatalf("List() = %d entries, want 2", len(list))
	}
	for _, info := range list {
		if info.Enabled {
			t.Errorf("%s enabled by default, want disabled", info.ID)
		}
	}
}

func TestApplyNoopWhenEverythingDisabled(t *testing.T) {
	r := NewRegistry(storage.NewMemory().Settings())
	req := &openai.ChatCompletionRequest{Messages: []openai.Message{textMsg("user", "hello   \n\n\n")}}
	before := string(req.Messages[0].Content)
	r.Apply(req)
	if string(req.Messages[0].Content) != before {
		t.Errorf("Apply mutated a request with every extension disabled")
	}
}

func TestUpdateEnablesAndPersists(t *testing.T) {
	ctx := context.Background()
	settings := storage.NewMemory().Settings()
	r := NewRegistry(settings)

	if err := r.Update(ctx, IDSanitize, true, json.RawMessage(`{"trim_whitespace":true}`)); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !r.List()[1].Enabled {
		t.Error("sanitize not enabled after Update")
	}

	// A fresh registry reading the same settings store picks up the change.
	r2 := NewRegistry(settings)
	if err := r2.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !r2.List()[1].Enabled {
		t.Error("reload lost persisted enabled flag")
	}
}

func TestUpdateUnknownIDRejected(t *testing.T) {
	r := NewRegistry(storage.NewMemory().Settings())
	err := r.Update(context.Background(), "nope", true, nil)
	if err == nil {
		t.Fatal("Update(unknown id) succeeded, want error")
	}
}

func TestUpdateRejectsNegativeHistoryWindow(t *testing.T) {
	r := NewRegistry(storage.NewMemory().Settings())
	err := r.Update(context.Background(), IDSanitize, true, json.RawMessage(`{"history_window":-1}`))
	if err == nil {
		t.Fatal("Update(negative history_window) succeeded, want error")
	}
}

func TestApplySanitizeTrimsWhitespaceAndRecordsStats(t *testing.T) {
	ctx := context.Background()
	r := NewRegistry(storage.NewMemory().Settings())
	if err := r.Update(ctx, IDSanitize, true, json.RawMessage(`{"trim_whitespace":true}`)); err != nil {
		t.Fatalf("Update: %v", err)
	}

	req := &openai.ChatCompletionRequest{Messages: []openai.Message{textMsg("user", "hi   \n\n\n\n")}}
	r.Apply(req)

	got, _ := req.Messages[0].Text()
	if got != "hi" {
		t.Errorf("trimmed text = %q, want %q", got, "hi")
	}

	stats := r.List()[1].Stats
	if stats.BytesSaved == 0 {
		t.Error("BytesSaved = 0, want > 0 after a trim")
	}
	if stats.MessagesRewritten != 1 {
		t.Errorf("MessagesRewritten = %d, want 1", stats.MessagesRewritten)
	}
}

func TestApplyRTKRecordsStats(t *testing.T) {
	ctx := context.Background()
	r := NewRegistry(storage.NewMemory().Settings())
	if err := r.Update(ctx, IDRTK, true, nil); err != nil {
		t.Fatalf("Update: %v", err)
	}

	lines := ""
	for i := 0; i < 20; i++ {
		lines += "--- PASS: TestSomething\n"
	}
	req := &openai.ChatCompletionRequest{Messages: []openai.Message{textMsg("user", lines)}}
	r.Apply(req)

	stats := r.List()[0].Stats
	if stats.BytesSaved == 0 {
		t.Error("RTK BytesSaved = 0, want > 0 after compressing a test roll call")
	}
	if stats.MessagesRewritten != 1 {
		t.Errorf("RTK MessagesRewritten = %d, want 1", stats.MessagesRewritten)
	}
}

func TestApplyLeavesSystemPromptUntouched(t *testing.T) {
	ctx := context.Background()
	r := NewRegistry(storage.NewMemory().Settings())
	if err := r.Update(ctx, IDSanitize, true, json.RawMessage(`{"trim_whitespace":true}`)); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := r.Update(ctx, IDRTK, true, nil); err != nil {
		t.Fatalf("Update: %v", err)
	}

	sys := textMsg("system", "keep me   \n\n\n\n")
	req := &openai.ChatCompletionRequest{Messages: []openai.Message{sys}}
	r.Apply(req)

	if string(req.Messages[0].Content) != string(sys.Content) {
		t.Error("system prompt was rewritten")
	}
}
