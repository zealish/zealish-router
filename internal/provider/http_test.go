package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
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
	if name == "commandcode" {
		return NewCommandCode(opts), srv
	}
	return NewOpenAI(opts), srv
}

func TestChatCompletionHappyPath(t *testing.T) {
	var gotPath string
	var gotHeaders http.Header
	var gotBody openai.ChatCompletionRequest

	p, _ := newTestProvider(t, "openai", func(w http.ResponseWriter, r *http.Request) {
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
	if auth := gotHeaders.Get("Authorization"); auth != "Bearer sk-test" {
		t.Errorf("Authorization = %q", auth)
	}
}

func TestAPIKeyRoundRobin(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("Authorization"))
		writeJSON(t, w, openai.ChatCompletionResponse{ID: "ok", Choices: []openai.Choice{{Index: 0, Message: &openai.Message{Role: "assistant", Content: json.RawMessage(`"ok"`)}}}})
	}))
	t.Cleanup(srv.Close)
	p := NewOpenAI(Options{Name: "openai", BaseURL: srv.URL, APIKeys: []string{"key-a", "key-b"}, APIKeyMethod: "round_robin", HTTPClient: srv.Client()})
	for i := 0; i < 4; i++ {
		if _, err := p.ChatCompletion(context.Background(), testRequest()); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"Bearer key-a", "Bearer key-b", "Bearer key-a", "Bearer key-b"}
	if !slices.Equal(got, want) {
		t.Fatalf("authorization = %v, want %v", got, want)
	}
}
func TestCommandCodeAPIKeyConnection(t *testing.T) {
	var gotPath string
	var gotHeaders http.Header
	p, srv := newTestProvider(t, "commandcode", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = io.WriteString(w, `{"type":"text-delta","text":"pong"}`+"\n"+`{"type":"finish"}`+"\n")
	})

	// Use the real provider-compatible base path, then verify the OpenAI route.
	_ = srv
	resp, err := p.ChatCompletion(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("CommandCode ChatCompletion: %v", err)
	}
	if resp.ID == "" || !strings.HasPrefix(resp.ID, "chatcmpl-") {
		t.Fatalf("response id = %q", resp.ID)
	}
	if gotPath != "/alpha/generate" {
		t.Errorf("path = %q, want /alpha/generate", gotPath)
	}
	if got := gotHeaders.Get("x-session-id"); got == "" {
		t.Error("x-session-id header is missing")
	}
	if got := gotHeaders.Get("Authorization"); got != "Bearer sk-test" {
		t.Errorf("Authorization = %q, want Bearer sk-test", got)
	}
	if got := gotHeaders.Get("x-api-key"); got != "" {
		t.Errorf("x-api-key = %q, want empty", got)
	}
}

func TestCommandCodeRequestSchema(t *testing.T) {
	var payload map[string]any
	p, _ := newTestProvider(t, "commandcode", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = io.WriteString(w, `{"type":"finish"}`+"\n")
	})
	req := testRequest()
	req.Messages = []openai.Message{
		{Role: "system", Content: json.RawMessage(`"rules"`)},
		{Role: "user", Content: json.RawMessage(`"hello"`)},
		{Role: "assistant", Content: json.RawMessage(`"checking"`), Extra: map[string]json.RawMessage{"tool_calls": json.RawMessage(`[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"city\":\"Jakarta\"}"}}]`)}},
		{Role: "assistant", Content: json.RawMessage(`null`), Extra: map[string]json.RawMessage{"tool_calls": json.RawMessage(`[{"id":"call_2","type":"function","function":{"name":"lookup","arguments":"{}"}}]`)}},
		{Role: "tool", Name: "lookup", Content: json.RawMessage(`"31C"`), Extra: map[string]json.RawMessage{"tool_call_id": json.RawMessage(`"call_1"`)}},
	}
	req.Extra = map[string]json.RawMessage{"tools": json.RawMessage(`[{"type":"function","function":{"name":"lookup","description":"find","parameters":{"type":"object"}}}]`)}
	if _, err := p.ChatCompletion(context.Background(), req); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	params, ok := payload["params"].(map[string]any)
	if !ok {
		t.Fatalf("payload = %#v", payload)
	}
	if params["system"] != "rules" {
		t.Fatalf("system = %#v", params["system"])
	}
	msgs := params["messages"].([]any)
	toolOnly := msgs[2].(map[string]any)["content"].([]any)
	if len(toolOnly) != 1 || toolOnly[0].(map[string]any)["type"] != "tool-call" {
		t.Fatalf("tool-only assistant content = %#v, want only tool-call block", toolOnly)
	}
	if got := msgs[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"]; got != "hello" {
		t.Fatalf("content text = %#v", got)
	}
	if got := msgs[1].(map[string]any)["content"].([]any)[1].(map[string]any)["type"]; got != "tool-call" {
		t.Fatalf("assistant block type = %#v", got)
	}
	newlineReq := testRequest()
	newlineReq.Messages = []openai.Message{{Role: "system", Content: json.RawMessage(`[{"type":"text","text":"one"},{"type":"text","text":"two"}]`)}, {Role: "user", Content: json.RawMessage(`"hello"`)}}
	if _, err := p.ChatCompletion(context.Background(), newlineReq); err != nil {
		t.Fatalf("newline ChatCompletion: %v", err)
	}
	newlineParams := payload["params"].(map[string]any)
	if got := newlineParams["system"]; got != "one\ntwo" {
		t.Fatalf("flattened text = %#v, want newline-separated text", got)
	}
	tools := params["tools"].([]any)[0].(map[string]any)
	if tools["name"] != "lookup" || tools["input_schema"] == nil || tools["description"] != "find" {
		t.Fatalf("tools = %#v", tools)
	}
	var emptyPayload map[string]any
	pEmpty, _ := newTestProvider(t, "commandcode", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&emptyPayload); err != nil {
			t.Errorf("decode empty payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = io.WriteString(w, `{"type":"finish"}`+"\n")
	})
	emptyReq := testRequest()
	emptyReq.Messages = []openai.Message{{Role: "user", Content: json.RawMessage(`[]`)}}
	if _, err := pEmpty.ChatCompletion(context.Background(), emptyReq); err != nil {
		t.Fatalf("empty array ChatCompletion: %v", err)
	}
	emptyMsgs := emptyPayload["params"].(map[string]any)["messages"].([]any)
	emptyBlock := emptyMsgs[0].(map[string]any)["content"].([]any)
	if len(emptyBlock) != 1 || emptyBlock[0].(map[string]any)["text"] != "" {
		t.Fatalf("empty content blocks = %#v", emptyBlock)
	}
	var noDescPayload map[string]any
	pNoDesc, _ := newTestProvider(t, "commandcode", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&noDescPayload); err != nil {
			t.Errorf("decode no-description payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = io.WriteString(w, `{"type":"finish"}`+"\n")
	})
	noDescReq := testRequest()
	noDescReq.Extra = map[string]json.RawMessage{"tools": json.RawMessage(`[{"type":"function","function":{"name":"ping","parameters":{"type":"object"}}}]`)}
	if _, err := pNoDesc.ChatCompletion(context.Background(), noDescReq); err != nil {
		t.Fatalf("no-description ChatCompletion: %v", err)
	}
	noDescTool := noDescPayload["params"].(map[string]any)["tools"].([]any)[0].(map[string]any)
	if _, ok := noDescTool["description"]; ok {
		t.Fatalf("tool unexpectedly contains description: %#v", noDescTool)
	}
}

func TestCommandCodePlainToolObjects(t *testing.T) {
	var payload map[string]any
	p, _ := newTestProvider(t, "commandcode", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = io.WriteString(w, `{"type":"finish"}`+"\n")
	})
	req := testRequest()
	req.Extra = map[string]json.RawMessage{"tools": json.RawMessage(`[{"name":"lookup","description":"find","input_schema":{"type":"object"}},{"name":"ping","parameters":{"type":"object"}}]`)}
	if _, err := p.ChatCompletion(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	tools := payload["params"].(map[string]any)["tools"].([]any)
	if len(tools) != 2 || tools[0].(map[string]any)["name"] != "lookup" || tools[1].(map[string]any)["name"] != "ping" {
		t.Fatalf("tools = %#v", tools)
	}
}

func TestCommandCodeObjectErrorSerialization(t *testing.T) {
	p, _ := newTestProvider(t, "commandcode", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = io.WriteString(w, `{"type":"text-delta","text":"before"}`+"\n"+`{"type":"error","error":{"message":"nested","status":429}}`+"\n")
	})
	resp, err := p.ChatCompletion(context.Background(), testRequest())
	if err != nil {
		t.Fatal(err)
	}
	text, _ := resp.Choices[0].Message.Text()
	if !strings.Contains(text, `{"message":"nested","status":429}`) {
		t.Fatalf("text = %q", text)
	}
}

func TestCommandCodeErrorFieldPrecedesMessage(t *testing.T) {
	p, _ := newTestProvider(t, "commandcode", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = io.WriteString(w, `{"type":"text-delta","text":"before"}`+"\n"+`{"type":"error","error":{"code":"E","detail":"bad"},"message":"fallback"}`+"\n")
	})
	resp, err := p.ChatCompletion(context.Background(), testRequest())
	if err != nil {
		t.Fatal(err)
	}
	text, _ := resp.Choices[0].Message.Text()
	if !strings.Contains(text, `{"code":"E","detail":"bad"}`) || strings.Contains(text, "fallback") {
		t.Fatalf("text = %q", text)
	}
}

func TestCommandCodeObjectContentUsesJSString(t *testing.T) {
	var payload map[string]any
	p, _ := newTestProvider(t, "commandcode", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&payload)
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = io.WriteString(w, `{"type":"finish"}`+"\n")
	})
	req := testRequest()
	req.Messages = []openai.Message{{Role: "user", Content: json.RawMessage(`{"foo":"bar"}`)}}
	if _, err := p.ChatCompletion(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	content := payload["params"].(map[string]any)["messages"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"]
	if content != "[object Object]" {
		t.Fatalf("content = %#v", content)
	}
}

func TestCommandCodePreservesExplicitEmptyToolDescription(t *testing.T) {
	var payload map[string]any
	p, _ := newTestProvider(t, "commandcode", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&payload)
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = io.WriteString(w, `{"type":"finish"}`+"\n")
	})
	req := testRequest()
	req.Extra = map[string]json.RawMessage{"tools": json.RawMessage(`[{"type":"function","function":{"name":"empty","description":"","parameters":{"type":"object"}}},{"name":"absent","input_schema":{"type":"object"}}]`)}
	if _, err := p.ChatCompletion(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	tools := payload["params"].(map[string]any)["tools"].([]any)
	if _, ok := tools[0].(map[string]any)["description"]; !ok {
		t.Fatal("explicit empty description was omitted")
	}
	if _, ok := tools[1].(map[string]any)["description"]; ok {
		t.Fatal("absent description was added")
	}
}

func TestCommandCodeEmptyErrorWinsOverMessage(t *testing.T) {
	p, _ := newTestProvider(t, "commandcode", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = io.WriteString(w, `{"type":"text-delta","text":"before"}`+"\n"+`{"type":"error","error":"","message":"fallback"}`+"\n")
	})
	resp, err := p.ChatCompletion(context.Background(), testRequest())
	if err != nil {
		t.Fatal(err)
	}
	text, _ := resp.Choices[0].Message.Text()
	if !strings.Contains(text, "[CommandCode error: ]") || strings.Contains(text, "fallback") {
		t.Fatalf("text = %q", text)
	}
}

func TestCommandCodeNonStreamingToolCalls(t *testing.T) {
	p, _ := newTestProvider(t, "commandcode", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = io.WriteString(w, `{"type":"tool-input-start","id":"call_1","toolName":"lookup"}`+"\n"+`{"type":"tool-input-delta","id":"call_1","delta":"{\"city\":"}`+"\n"+`{"type":"tool-input-delta","id":"call_1","delta":"\"Jakarta\"}"}`+"\n"+`{"type":"finish-step","finishReason":"tool-calls"}`+"\n"+`{"type":"finish"}`+"\n")
	})
	resp, err := p.ChatCompletion(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if resp.Choices[0].FinishReason == nil || *resp.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("finish = %v", resp.Choices[0].FinishReason)
	}
	raw := resp.Choices[0].Message.Extra["tool_calls"]
	if !strings.Contains(string(raw), `"call_1"`) || !strings.Contains(string(raw), `Jakarta`) {
		t.Fatalf("tool_calls = %s", raw)
	}
}
func TestCommandCodeLateErrorRemainsVisibleInStream(t *testing.T) {
	p, _ := newTestProvider(t, "commandcode", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = io.WriteString(w, `{"type":"text-delta","text":"before"}`+"\n"+`{"type":"error","error":"late failure"}`+"\n")
	})
	ch, err := p.ChatCompletionStream(context.Background(), testRequest())
	if err != nil {
		t.Fatal(err)
	}
	var text string
	for c := range ch {
		if len(c.Choices) == 0 || c.Choices[0].Delta == nil {
			continue
		}
		if s, ok := c.Choices[0].Delta.Text(); ok {
			text += s
		}
	}
	if !strings.Contains(text, "before") || !strings.Contains(text, "[CommandCode error: late failure]") {
		t.Fatalf("stream text = %q, want visible late error", text)
	}
}

func TestCommandCodeNonStreamingLateErrorIsVisible(t *testing.T) {
	p, _ := newTestProvider(t, "commandcode", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = io.WriteString(w, `{"type":"text-delta","text":"before"}`+"\n"+`{"type":"error","error":"late failure"}`+"\n")
	})
	resp, err := p.ChatCompletion(context.Background(), testRequest())
	if err != nil {
		t.Fatal(err)
	}
	text, _ := resp.Choices[0].Message.Text()
	if !strings.Contains(text, "before") || !strings.Contains(text, "[CommandCode error: late failure]") {
		t.Fatalf("response text = %q", text)
	}
	if resp.Choices[0].FinishReason == nil || *resp.Choices[0].FinishReason != "stop" {
		t.Fatalf("finish = %v", resp.Choices[0].FinishReason)
	}
}

func TestCommandCodeStreamTerminatesOnFinishAndMapsNativeUsage(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	p, _ := newTestProvider(t, "commandcode", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = io.WriteString(w, `{"type":"text-delta","text":"ok"}`+"\n"+`{"type":"finish-step","finishReason":"tool-calls","usage":{"inputTokens":2,"outputTokens":3,"totalTokens":5}}`+"\n"+`{"type":"finish"}`+"\n")
		w.(http.Flusher).Flush()
		<-release
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ch, err := p.ChatCompletionStream(ctx, testRequest())
	if err != nil {
		t.Fatal(err)
	}
	var got []openai.StreamChunk
	for len(got) < 2 {
		select {
		case c, ok := <-ch:
			if !ok {
				t.Fatalf("stream closed after %d chunks, want text + finish", len(got))
			}
			got = append(got, c)
		case <-ctx.Done():
			t.Fatal("timed out waiting for flushed CommandCode chunks")
		}
	}
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("stream emitted an unexpected chunk after finish")
		}
		if ctx.Err() != nil {
			t.Fatal("stream closed only after context cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("stream did not close after finish while upstream remained open")
	}
	last := got[len(got)-1]
	if last.Choices[0].FinishReason == nil || *last.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("finish = %v", last.Choices[0].FinishReason)
	}
	if last.Usage == nil || last.Usage.TotalTokens != 5 {
		t.Fatalf("usage = %#v", last.Usage)
	}
}

func TestChatCompletionStream(t *testing.T) {
	body := "data: " + chunkJSON(t, "a") + "\n\n" +
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
			for range ch { //nolint:revive // draining is the point
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

func TestUpstreamErrorRedactsAPIKey(t *testing.T) {
	p, _ := newTestProvider(t, "openai", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		writeJSON(t, w, openai.ErrorResponse{
			Error: openai.Error{Message: "Incorrect API key provided: sk-test.", Type: "invalid_request_error"},
		})
	})

	_, err := p.ChatCompletion(context.Background(), testRequest())
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "sk-test") {
		t.Errorf("api key leaked in error: %q", err)
	}
	if !strings.Contains(err.Error(), redactionPlaceholder) {
		t.Errorf("error = %q, want the redaction placeholder", err)
	}
}
func TestCommandCodeJSONStatusError(t *testing.T) {
	p, _ := newTestProvider(t, "commandcode", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		writeJSON(t, w, map[string]any{"error": map[string]any{"code": "429", "message": "rate limited sk-test"}})
	})
	_, err := p.ChatCompletion(context.Background(), testRequest())
	if err == nil || !strings.Contains(err.Error(), "rate limited") || strings.Contains(err.Error(), "sk-test") {
		t.Fatalf("error = %v, want nested message, redacted key", err)
	}
	var pe *Error
	if !errors.As(err, &pe) || pe.Status != 429 || !errors.Is(err, ErrRateLimited) {
		t.Fatalf("error = %#v, want status 429/rate limited", err)
	}
}

func TestCommandCodeNestedEventStatusString(t *testing.T) {
	p, _ := newTestProvider(t, "commandcode", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = io.WriteString(w, `{"type":"start"}`+"\n"+`{"type":"error","error":{"status":"429","message":"rate limit exceeded"}}`+"\n")
	})
	_, err := p.ChatCompletion(context.Background(), testRequest())
	if err == nil || !errors.Is(err, ErrRateLimited) {
		t.Fatalf("error = %v, want rate limited", err)
	}
	var pe *Error
	if !errors.As(err, &pe) || pe.Status != 429 {
		t.Fatalf("error = %#v, want status 429", err)
	}
}

func TestCommandCodeEarlyNestedErrorUsesReadableMessage(t *testing.T) {
	p, _ := newTestProvider(t, "commandcode", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = io.WriteString(w, `{"type":"error","error":{"message":null,"error":"Boom","statusCode":429}}`+"\n")
	})
	_, err := p.ChatCompletion(context.Background(), testRequest())
	if err == nil || !strings.Contains(err.Error(), "Boom") || strings.Contains(err.Error(), `{"message":null`) {
		t.Fatalf("error = %v, want nested error string", err)
	}
	var pe *Error
	if !errors.As(err, &pe) || pe.Message != "Boom" || pe.Status != 429 || !errors.Is(err, ErrRateLimited) {
		t.Fatalf("error = %#v, want message Boom/status 429/rate limited", err)
	}
}

func TestTransportErrorRedactsAPIKey(t *testing.T) {
	p := NewOpenAI(Options{Name: "openai", BaseURL: "http://127.0.0.1:1/sk-secret", APIKey: "sk-secret", HTTPClient: &http.Client{}})
	_, err := p.ChatCompletion(context.Background(), testRequest())
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "sk-secret") {
		t.Errorf("api key leaked in error: %q", err)
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

func TestCommandCodeUsageExtractsCachedTokens(t *testing.T) {
	t.Run("cachedInputTokens_flat", func(t *testing.T) {
		u := commandCodeUsage(map[string]any{
			"inputTokens":      float64(48558),
			"outputTokens":     float64(125),
			"cachedInputTokens": float64(48320),
			"totalTokens":      float64(48683),
		})
		if u.Cached() != 48320 {
			t.Errorf("cached = %d, want 48320", u.Cached())
		}
		if u.PromptTokens != 48558 {
			t.Errorf("prompt = %d, want 48558", u.PromptTokens)
		}
	})
	t.Run("inputTokenDetails_cacheReadTokens", func(t *testing.T) {
		u := commandCodeUsage(map[string]any{
			"inputTokens":  float64(100),
			"outputTokens": float64(50),
			"inputTokenDetails": map[string]any{
				"cacheReadTokens": float64(80),
				"noCacheTokens":   float64(20),
			},
		})
		if u.Cached() != 80 {
			t.Errorf("cached = %d, want 80", u.Cached())
		}
	})
	t.Run("raw_prompt_tokens_details_fallback", func(t *testing.T) {
		u := commandCodeUsage(map[string]any{
			"inputTokens":  float64(300),
			"outputTokens": float64(50),
			"raw": map[string]any{
				"prompt_tokens_details": map[string]any{
					"cached_tokens": float64(250),
				},
			},
		})
		if u.Cached() != 250 {
			t.Errorf("cached = %d, want 250", u.Cached())
		}
	})
	t.Run("nested_prompt_tokens_details", func(t *testing.T) {
		u := commandCodeUsage(map[string]any{
			"inputTokens":  float64(300),
			"outputTokens": float64(50),
			"prompt_tokens_details": map[string]any{
				"cached_tokens":      float64(150),
				"cache_write_tokens": float64(10),
			},
		})
		if u.Cached() != 150 {
			t.Errorf("cached = %d, want 150", u.Cached())
		}
		if u.CacheWrite() != 10 {
			t.Errorf("cache_write = %d, want 10", u.CacheWrite())
		}
	})
	t.Run("canonical_takes_precedence_over_raw", func(t *testing.T) {
		u := commandCodeUsage(map[string]any{
			"inputTokens":      float64(100),
			"outputTokens":     float64(50),
			"cachedInputTokens": float64(60),
			"raw": map[string]any{
				"prompt_tokens_details": map[string]any{
					"cached_tokens": float64(999),
				},
			},
		})
		if u.Cached() != 60 {
			t.Errorf("cached = %d, want 60 (canonical wins)", u.Cached())
		}
	})
	t.Run("no_cached_fields", func(t *testing.T) {
		u := commandCodeUsage(map[string]any{
			"inputTokens":  float64(100),
			"outputTokens": float64(50),
		})
		if u.Cached() != 0 {
			t.Errorf("cached = %d, want 0", u.Cached())
		}
		if u.PromptTokensDetails != nil {
			t.Errorf("PromptTokensDetails = %+v, want nil", u.PromptTokensDetails)
		}
	})
}

func TestCommandCodeUsageExtractsReasoningTokens(t *testing.T) {
	t.Run("top_level_reasoningTokens", func(t *testing.T) {
		u := commandCodeUsage(map[string]any{
			"inputTokens":    float64(100),
			"outputTokens":   float64(50),
			"reasoningTokens": float64(30),
		})
		if u.Reasoning() != 30 {
			t.Errorf("reasoning = %d, want 30", u.Reasoning())
		}
	})
	t.Run("outputTokenDetails_reasoningTokens", func(t *testing.T) {
		u := commandCodeUsage(map[string]any{
			"inputTokens":  float64(100),
			"outputTokens": float64(50),
			"outputTokenDetails": map[string]any{
				"reasoningTokens": float64(25),
				"textTokens":      float64(25),
			},
		})
		if u.Reasoning() != 25 {
			t.Errorf("reasoning = %d, want 25", u.Reasoning())
		}
	})
}

func TestMergeCommandUsagePreservesCachedTokens(t *testing.T) {
	a := &openai.Usage{PromptTokens: 100, CompletionTokens: 50, PromptTokensDetails: &openai.PromptTokensDetails{CachedTokens: 40}}
	b := &openai.Usage{PromptTokens: 200, CompletionTokens: 80, PromptTokensDetails: &openai.PromptTokensDetails{CachedTokens: 30, CacheWriteTokens: 5}}
	merged := mergeCommandUsage(a, b)
	if merged.PromptTokens != 300 {
		t.Errorf("prompt = %d, want 300", merged.PromptTokens)
	}
	if merged.Cached() != 70 {
		t.Errorf("cached = %d, want 70 (40+30)", merged.Cached())
	}
	if merged.CacheWrite() != 5 {
		t.Errorf("cache_write = %d, want 5", merged.CacheWrite())
	}
}

func TestCommandCodeCatalogUsesAuthoritativeCMCPrefix(t *testing.T) {
	entries := Catalog("")
	var found bool
	for _, entry := range entries {
		if entry.ID == "commandcode" {
			found = true
			if entry.AliasPrefix != "cmc/" {
				t.Fatalf("alias prefix = %q, want cmc/", entry.AliasPrefix)
			}
		}
	}
	if !found {
		t.Fatal("commandcode catalog entry missing")
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("encode: %v", err)
	}
}

func TestEmbeddingsHappyPath(t *testing.T) {
	var gotPath string
	var gotHeaders http.Header
	var gotBody openai.EmbeddingRequest

	p, _ := newTestProvider(t, "openai", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotHeaders = r.Header.Clone()
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode upstream body: %v", err)
		}
		writeJSON(t, w, openai.EmbeddingResponse{
			Object: "list",
			Model:  "text-embedding-3-small",
			Data:   []openai.Embedding{{Object: "embedding", Index: 0, Embedding: json.RawMessage(`[0.1,0.2]`)}},
			Usage:  &openai.Usage{PromptTokens: 5, TotalTokens: 5},
		})
	})

	resp, err := p.Embeddings(context.Background(), &openai.EmbeddingRequest{
		Model: "text-embedding-3-small",
		Input: json.RawMessage(`"hi"`),
	})
	if err != nil {
		t.Fatalf("Embeddings: %v", err)
	}
	if gotPath != "/embeddings" {
		t.Errorf("path = %q, want /embeddings", gotPath)
	}
	if auth := gotHeaders.Get("Authorization"); auth != "Bearer sk-test" {
		t.Errorf("Authorization = %q", auth)
	}
	if string(gotBody.Input) != `"hi"` {
		t.Errorf("input = %s, want the raw payload forwarded", gotBody.Input)
	}
	if len(resp.Data) != 1 || string(resp.Data[0].Embedding) != "[0.1,0.2]" {
		t.Fatalf("unexpected data: %+v", resp.Data)
	}
	if resp.Usage == nil || resp.Usage.PromptTokens != 5 {
		t.Fatalf("usage not decoded: %+v", resp.Usage)
	}
}

func TestEmbeddingsErrorMapping(t *testing.T) {
	p, _ := newTestProvider(t, "openai", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		writeJSON(t, w, openai.ErrorResponse{Error: openai.Error{Message: "slow down"}})
	})

	_, err := p.Embeddings(context.Background(), &openai.EmbeddingRequest{
		Model: "text-embedding-3-small",
		Input: json.RawMessage(`"hi"`),
	})
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
}

func TestAnthropicEmbeddingsUnsupported(t *testing.T) {
	p := NewAnthropic(Options{Name: "anthropic", BaseURL: "http://invalid.test", APIKey: "sk-ant-test"})

	_, err := p.Embeddings(context.Background(), &openai.EmbeddingRequest{
		Model: "claude-sonnet-4.5",
		Input: json.RawMessage(`"hi"`),
	})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
	if Retryable(err) {
		t.Error("an unsupported endpoint must not be retryable")
	}
}
