package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zealish/zealish-router/pkg/anthropic"
	"github.com/zealish/zealish-router/pkg/openai"
)

func newAnthropicProvider(t *testing.T, opts Options, h http.HandlerFunc) *Anthropic {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	opts.Name = "claude"
	opts.BaseURL = srv.URL
	opts.HTTPClient = srv.Client()
	return NewAnthropic(opts)
}

func anthropicRequestFixture() *openai.ChatCompletionRequest {
	maxTokens := 256
	return &openai.ChatCompletionRequest{
		Model: "claude-sonnet-4",
		Messages: []openai.Message{
			{Role: "system", Content: json.RawMessage(`"be brief"`)},
			{Role: "user", Content: json.RawMessage(`"hi"`)},
		},
		MaxTokens: &maxTokens,
	}
}

func TestAnthropicChatCompletion(t *testing.T) {
	var (
		gotPath string
		gotHdr  http.Header
		gotBody anthropic.Request
	)
	p := newAnthropicProvider(t, Options{APIKey: "sk-ant-key"}, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotHdr = r.Header.Clone()
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode upstream body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_1","model":"claude-sonnet-4","stop_reason":"end_turn",
			"content":[{"type":"text","text":"pong"}],
			"usage":{"input_tokens":5,"output_tokens":2,"cache_read_input_tokens":3}}`)
	})

	resp, err := p.ChatCompletion(context.Background(), anthropicRequestFixture())
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	if gotPath != "/messages" {
		t.Errorf("path = %q, want /messages", gotPath)
	}
	if gotHdr.Get("x-api-key") != "sk-ant-key" {
		t.Errorf("x-api-key = %q, want the provider key", gotHdr.Get("x-api-key"))
	}
	if gotHdr.Get("Authorization") != "" {
		t.Errorf("api-key providers must not send Authorization, got %q", gotHdr.Get("Authorization"))
	}
	if gotHdr.Get("anthropic-version") != anthropic.Version {
		t.Errorf("anthropic-version = %q", gotHdr.Get("anthropic-version"))
	}

	if got := anthropic.SystemText(gotBody.System); got != "be brief" {
		t.Errorf("system = %q, want the hoisted system message", got)
	}
	if len(gotBody.Messages) != 1 || gotBody.Messages[0].Role != "user" {
		t.Fatalf("messages = %+v, want the user message only", gotBody.Messages)
	}
	if gotBody.MaxTokens != 256 {
		t.Errorf("max_tokens = %d, want 256", gotBody.MaxTokens)
	}

	if len(resp.Choices) != 1 {
		t.Fatalf("choices = %+v", resp.Choices)
	}
	if text, _ := resp.Choices[0].Message.Text(); text != "pong" {
		t.Errorf("content = %q, want pong", text)
	}
	if *resp.Choices[0].FinishReason != "stop" {
		t.Errorf("finish_reason = %q, want stop", *resp.Choices[0].FinishReason)
	}

	if resp.Usage.PromptTokens != 8 || resp.Usage.CompletionTokens != 2 || resp.Usage.TotalTokens != 10 {
		t.Errorf("usage = %+v, want prompt 8 / completion 2 / total 10", resp.Usage)
	}
	if resp.Usage.Cached() != 3 {
		t.Errorf("cached tokens = %d, want 3", resp.Usage.Cached())
	}
}

func TestAnthropicDefaultsMaxTokens(t *testing.T) {
	var gotBody anthropic.Request
	p := newAnthropicProvider(t, Options{APIKey: "k"}, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = io.WriteString(w, `{"id":"m","content":[]}`)
	})

	req := anthropicRequestFixture()
	req.MaxTokens = nil
	if _, err := p.ChatCompletion(context.Background(), req); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if gotBody.MaxTokens != defaultMaxTokens {
		t.Errorf("max_tokens = %d, want the default %d", gotBody.MaxTokens, defaultMaxTokens)
	}
}

func TestAnthropicOAuthUsesBearer(t *testing.T) {
	var gotHdr http.Header
	p := newAnthropicProvider(t, Options{APIKey: "Bearer tok", AuthHeader: "Authorization"},
		func(w http.ResponseWriter, r *http.Request) {
			gotHdr = r.Header.Clone()
			_, _ = io.WriteString(w, `{"id":"m","content":[]}`)
		})

	if _, err := p.ChatCompletion(context.Background(), anthropicRequestFixture()); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if gotHdr.Get("Authorization") != "Bearer tok" {
		t.Errorf("authorization = %q, want the OAuth token", gotHdr.Get("Authorization"))
	}
	if gotHdr.Get("x-api-key") != "" {
		t.Errorf("oauth providers must not send x-api-key, got %q", gotHdr.Get("x-api-key"))
	}
}

func TestAnthropicChatCompletionStream(t *testing.T) {
	body := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_2","model":"claude-sonnet-4","usage":{"input_tokens":7}}}` + "\n\n" +
		": keepalive\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"po"}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"ng"}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"

	var gotStream bool
	p := newAnthropicProvider(t, Options{APIKey: "k"}, func(w http.ResponseWriter, r *http.Request) {
		var req anthropic.Request
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotStream = req.Stream
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body)
	})

	ch, err := p.ChatCompletionStream(context.Background(), anthropicRequestFixture())
	if err != nil {
		t.Fatalf("ChatCompletionStream: %v", err)
	}

	var (
		text   string
		last   openai.StreamChunk
		chunks int
	)
	for chunk := range ch {
		chunks++
		last = chunk
		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta != nil {
			if s, ok := chunk.Choices[0].Delta.Text(); ok {
				text += s
			}
		}
		if chunk.ID != "msg_2" {
			t.Errorf("chunk id = %q, want msg_2", chunk.ID)
		}
	}

	if !gotStream {
		t.Error("stream must be true on streaming calls")
	}
	if chunks != 3 {
		t.Fatalf("chunks = %d, want 3 (two deltas plus the final)", chunks)
	}
	if text != "pong" {
		t.Errorf("text = %q, want pong", text)
	}
	if last.Usage == nil || last.Usage.PromptTokens != 7 || last.Usage.CompletionTokens != 4 {
		t.Errorf("final usage = %+v, want prompt 7 / completion 4", last.Usage)
	}
	if last.Choices[0].FinishReason == nil || *last.Choices[0].FinishReason != "stop" {
		t.Errorf("finish reason = %v, want stop", last.Choices[0].FinishReason)
	}
}

func TestAnthropicErrorMapping(t *testing.T) {
	p := newAnthropicProvider(t, Options{APIKey: "sk-ant-secret"}, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down, key sk-ant-secret"}}`)
	})

	_, err := p.ChatCompletion(context.Background(), anthropicRequestFixture())
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
	var perr *Error
	if !errors.As(err, &perr) {
		t.Fatalf("err = %T, want *Error", err)
	}
	if perr.Status != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", perr.Status)
	}
	if strings.Contains(perr.Message, "sk-ant-secret") {
		t.Errorf("message = %q, want the key redacted", perr.Message)
	}
}
