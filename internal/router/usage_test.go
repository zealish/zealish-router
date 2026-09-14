package router

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/zealish/zealish-router/pkg/openai"
)

// usageProvider returns a response or stream carrying a chosen Usage value.
type usageProvider struct {
	name   string
	usage  *openai.Usage
	chunks []openai.StreamChunk
	text   string
}

func (u *usageProvider) Name() string { return u.name }

func (u *usageProvider) ChatCompletion(_ context.Context, req *openai.ChatCompletionRequest) (*openai.ChatCompletionResponse, error) {
	return &openai.ChatCompletionResponse{
		ID:    "cmpl-1",
		Model: req.Model,
		Choices: []openai.Choice{{
			Index:   0,
			Message: &openai.Message{Role: "assistant", Content: mustJSON(u.text)},
		}},
		Usage: u.usage,
	}, nil
}

func (u *usageProvider) ChatCompletionStream(_ context.Context, _ *openai.ChatCompletionRequest) (<-chan openai.StreamChunk, error) {
	ch := make(chan openai.StreamChunk, len(u.chunks))
	for _, c := range u.chunks {
		ch <- c
	}
	close(ch)
	return ch, nil
}

func (u *usageProvider) Embeddings(_ context.Context, req *openai.EmbeddingRequest) (*openai.EmbeddingResponse, error) {
	return &openai.EmbeddingResponse{
		Object: "list",
		Model:  req.Model,
		Data:   []openai.Embedding{{Object: "embedding", Index: 0, Embedding: json.RawMessage(`[0.5]`)}},
		Usage:  u.usage,
	}, nil
}

func mustJSON(s string) json.RawMessage {
	raw, _ := json.Marshal(s)
	return raw
}

func userRequest(alias, text string) *openai.ChatCompletionRequest {
	return &openai.ChatCompletionRequest{
		Model:    alias,
		Messages: []openai.Message{{Role: "user", Content: mustJSON(text)}},
	}
}

func TestRecordsReportedUsage(t *testing.T) {
	p := &usageProvider{
		name:  "openai",
		usage: &openai.Usage{PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18},
	}
	rec := newRecorder()
	e := newTestEngine(t, rec, p)

	if _, err := e.ChatCompletion(context.Background(), userRequest("gpt-5", "hello there")); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if got := rec.token("openai/gpt-5-upstream/prompt"); got != 11 {
		t.Errorf("prompt tokens = %d, want 11", got)
	}
	if got := rec.token("openai/gpt-5-upstream/completion"); got != 7 {
		t.Errorf("completion tokens = %d, want 7", got)
	}
}

func TestEstimatesUsageWhenUpstreamOmitsIt(t *testing.T) {
	// 16 prompt chars and 8 completion chars at ~4 chars/token.
	p := &usageProvider{name: "openai", text: "12345678"}
	rec := newRecorder()
	e := newTestEngine(t, rec, p)

	if _, err := e.ChatCompletion(context.Background(), userRequest("gpt-5", "1234567890123456")); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if got := rec.token("openai/gpt-5-upstream/prompt"); got != 4 {
		t.Errorf("estimated prompt tokens = %d, want 4", got)
	}
	if got := rec.token("openai/gpt-5-upstream/completion"); got != 2 {
		t.Errorf("estimated completion tokens = %d, want 2", got)
	}
}

func TestStreamRecordsReportedUsage(t *testing.T) {
	p := &usageProvider{name: "openai", chunks: []openai.StreamChunk{
		{ID: "c1", Choices: []openai.Choice{{Delta: &openai.Message{Content: mustJSON("hi")}}}},
		{ID: "c2", Usage: &openai.Usage{PromptTokens: 5, CompletionTokens: 3}},
	}}
	rec := newRecorder()
	e := newTestEngine(t, rec, p)

	ch, err := e.ChatCompletionStream(context.Background(), userRequest("gpt-5", "hello"))
	if err != nil {
		t.Fatalf("ChatCompletionStream: %v", err)
	}

	var n int
	for range ch {
		n++
	}
	if n != 2 {
		t.Fatalf("forwarded %d chunks, want 2", n)
	}
	if got := rec.token("openai/gpt-5-upstream/prompt"); got != 5 {
		t.Errorf("prompt tokens = %d, want 5", got)
	}
	if got := rec.token("openai/gpt-5-upstream/completion"); got != 3 {
		t.Errorf("completion tokens = %d, want 3", got)
	}
}

func TestStreamEstimatesUsageWhenOmitted(t *testing.T) {
	// Deltas total 8 chars -> 2 tokens.
	p := &usageProvider{name: "openai", chunks: []openai.StreamChunk{
		{ID: "c1", Choices: []openai.Choice{{Delta: &openai.Message{Content: mustJSON("1234")}}}},
		{ID: "c2", Choices: []openai.Choice{{Delta: &openai.Message{Content: mustJSON("5678")}}}},
	}}
	rec := newRecorder()
	e := newTestEngine(t, rec, p)

	ch, err := e.ChatCompletionStream(context.Background(), userRequest("gpt-5", "12345678"))
	if err != nil {
		t.Fatalf("ChatCompletionStream: %v", err)
	}
	drain(ch)

	if got := rec.token("openai/gpt-5-upstream/prompt"); got != 2 {
		t.Errorf("estimated prompt tokens = %d, want 2", got)
	}
	if got := rec.token("openai/gpt-5-upstream/completion"); got != 2 {
		t.Errorf("estimated completion tokens = %d, want 2", got)
	}
}

func TestNilRecorderIsSafe(t *testing.T) {
	e := newTestEngine(t, nil, &usageProvider{name: "openai"})

	if _, err := e.ChatCompletion(context.Background(), userRequest("gpt-5", "hi")); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	ch, err := e.ChatCompletionStream(context.Background(), userRequest("gpt-5", "hi"))
	if err != nil {
		t.Fatalf("ChatCompletionStream: %v", err)
	}
	drain(ch)
}

// drain consumes a stream to completion, so the metering goroutine finishes.
func drain(ch <-chan openai.StreamChunk) {
	for range ch { //nolint:revive // draining is the point
	}
}

func TestTokensFromChars(t *testing.T) {
	tests := map[int]int{0: 0, -3: 0, 1: 1, 4: 1, 5: 2, 8: 2, 9: 3}
	for chars, want := range tests {
		if got := tokensFromChars(chars); got != want {
			t.Errorf("tokensFromChars(%d) = %d, want %d", chars, got, want)
		}
	}
}

func TestContentLenHandlesPartsArray(t *testing.T) {
	parts := json.RawMessage(`[{"type":"text","text":"hello"}]`)
	if got := contentLen(parts); got != len(parts) {
		t.Errorf("contentLen(parts) = %d, want %d", got, len(parts))
	}
	if got := contentLen(nil); got != 0 {
		t.Errorf("contentLen(nil) = %d, want 0", got)
	}
	if got := contentLen(mustJSON("hello")); got != 5 {
		t.Errorf("contentLen(string) = %d, want 5", got)
	}
}
