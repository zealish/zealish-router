package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/zealish/zealish-router/pkg/openai"
)

func testRequest() *openai.ChatCompletionRequest {
	return &openai.ChatCompletionRequest{
		Model:    "gpt-5",
		Messages: []openai.Message{{Role: "user", Content: json.RawMessage(`"hi"`)}},
	}
}

func newTestProvider(t *testing.T, name string, h http.HandlerFunc) (Provider, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	opts := Options{Name: name, BaseURL: srv.URL, APIKey: "sk-test", HTTPClient: srv.Client()}
	switch name {
	case "openrouter":
		return NewOpenRouter(opts), srv
	case "ollama":
		return NewOllama(opts), srv
	default:
		return NewOpenAI(opts), srv
	}
}

func TestChatCompletionHappyPath(t *testing.T) {
	for _, name := range []string{"openai", "openrouter", "ollama"} {
		t.Run(name, func(t *testing.T) {
			var gotPath string
			var gotHeaders http.Header
			var gotBody openai.ChatCompletionRequest

			p, _ := newTestProvider(t, name, func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotHeaders = r.Header.Clone()
				if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
					t.Errorf("decode upstream body: %v", err)
				}
				writeJSON(t, w, openai.ChatCompletionResponse{
					ID:      "cmpl-1",
					Object:  "chat.completion",
					Model:   "gpt-5",
					Choices: []openai.Choice{{Index: 0, Message: &openai.Message{Role: "assistant", Content: json.RawMessage(`"pong"`)}}},
					Usage:   &openai.Usage{PromptTokens: 3, CompletionTokens: 1, TotalTokens: 4},
				})
			})

			resp, err := p.ChatCompletion(context.Background(), testRequest())
			if err != nil {
				t.Fatalf("ChatCompletion: %v", err)
			}
			if resp.ID != "cmpl-1" || len(resp.Choices) != 1 {
				t.Fatalf("unexpected response: %+v", resp)
			}
			if resp.Usage == nil || resp.Usage.TotalTokens != 4 {
				t.Fatalf("usage not decoded: %+v", resp.Usage)
			}
			if gotPath != "/chat/completions" {
				t.Errorf("path = %q, want /chat/completions", gotPath)
			}
			if gotBody.Stream {
				t.Error("stream must be false on non-streaming calls")
			}

			auth := gotHeaders.Get("Authorization")
			if name == "ollama" {
				if auth != "" {
					t.Errorf("ollama must not send Authorization, got %q", auth)
				}
			} else if auth != "Bearer sk-test" {
				t.Errorf("Authorization = %q", auth)
			}
			if name == "openrouter" {
				if gotHeaders.Get("HTTP-Referer") == "" || gotHeaders.Get("X-Title") == "" {
					t.Error("openrouter attribution headers missing")
				}
			}
		})
	}
}

func TestChatCompletionStream(t *testing.T) {
	body := "data: " + chunkJSON(t, "a") + "\n\n" +
		": keepalive\n\n" +
		"data: " + chunkJSON(t, "b") + "\n\n" +
		"data: [DONE]\n\n" +
		"data: " + chunkJSON(t, "never") + "\n\n"

	var gotStream bool
	p, _ := newTestProvider(t, "openai", func(w http.ResponseWriter, r *http.Request) {
		var req openai.ChatCompletionRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotStream = req.Stream

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body)
	})

	ch, err := p.ChatCompletionStream(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("ChatCompletionStream: %v", err)
	}

	var ids []string
	for chunk := range ch {
		ids = append(ids, chunk.ID)
	}
	if !gotStream {
		t.Error("stream must be true on streaming calls")
	}
	if len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Fatalf("chunks = %v, want [a b]", ids)
	}
}

func TestChatCompletionStreamContextCancel(t *testing.T) {
	release := make(chan struct{})
	p, _ := newTestProvider(t, "openai", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+chunkJSON(t, "a")+"\n\n")
		w.(http.Flusher).Flush()
		<-release
	})
	t.Cleanup(func() { close(release) })

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := p.ChatCompletionStream(ctx, testRequest())
	if err != nil {
		t.Fatalf("ChatCompletionStream: %v", err)
	}
	if got := <-ch; got.ID != "a" {
		t.Fatalf("first chunk = %q", got.ID)
	}
	cancel()

	select {
	case _, open := <-ch:
		if open {
			// Drain until closed; the channel must not stay open.
			for range ch {
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("channel not closed after context cancellation")
	}
}

func TestErrorMapping(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   error
	}{
		{"rate limited", http.StatusTooManyRequests, ErrRateLimited},
		{"server error", http.StatusServiceUnavailable, ErrUpstream5xx},
		{"internal error", http.StatusInternalServerError, ErrUpstream5xx},
		{"gateway timeout", http.StatusGatewayTimeout, ErrTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, _ := newTestProvider(t, "openai", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				writeJSON(t, w, openai.ErrorResponse{Error: openai.Error{Message: "boom", Type: "api_error"}})
			})

			_, err := p.ChatCompletion(context.Background(), testRequest())
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if !Retryable(err) {
				t.Error("error should be retryable")
			}

			var perr *Error
			if !errors.As(err, &perr) {
				t.Fatal("error is not *provider.Error")
			}
			if perr.Status != tt.status || perr.Message != "boom" {
				t.Errorf("got status=%d message=%q", perr.Status, perr.Message)
			}
		})
	}
}

func TestClientErrorNotRetryable(t *testing.T) {
	p, _ := newTestProvider(t, "openai", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(t, w, openai.ErrorResponse{Error: openai.Error{Message: "bad model"}})
	})

	_, err := p.ChatCompletion(context.Background(), testRequest())
	if err == nil {
		t.Fatal("expected an error")
	}
	if Retryable(err) {
		t.Fatalf("400 must not be retryable: %v", err)
	}
}

func TestTimeoutMapping(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		<-block
	}))
	defer srv.Close()
	defer close(block)

	client := srv.Client()
	client.Timeout = 50 * time.Millisecond
	p := NewOpenAI(Options{Name: "openai", BaseURL: srv.URL, HTTPClient: client})

	_, err := p.ChatCompletion(context.Background(), testRequest())
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
}

func TestConnectionErrorMapping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing is listening any more

	p := NewOpenAI(Options{Name: "openai", BaseURL: url, HTTPClient: &http.Client{}})
	_, err := p.ChatCompletion(context.Background(), testRequest())
	if !errors.Is(err, ErrConnection) {
		t.Fatalf("err = %v, want ErrConnection", err)
	}
}

func chunkJSON(t *testing.T, id string) string {
	t.Helper()
	raw, err := json.Marshal(openai.StreamChunk{
		ID:      id,
		Object:  "chat.completion.chunk",
		Model:   "gpt-5",
		Choices: []openai.Choice{{Index: 0, Delta: &openai.Message{Role: "assistant", Content: json.RawMessage(`"x"`)}}},
	})
	if err != nil {
		t.Fatalf("marshal chunk: %v", err)
	}
	return string(raw)
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("encode: %v", err)
	}
}
