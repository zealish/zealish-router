package router

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	"github.com/zealish/zealish-router/internal/provider"
	"github.com/zealish/zealish-router/internal/storage"
	"github.com/zealish/zealish-router/pkg/openai"
)

func TestIntelligentContextSkipsUndersizedModels(t *testing.T) {
	largeText, _ := json.Marshal(strings.Repeat("abcd", 400))
	maxTokens := 400
	for _, tc := range []struct {
		name string
		req  openai.ChatCompletionRequest
	}{
		{name: "system", req: openai.ChatCompletionRequest{Messages: []openai.Message{{Role: "system", Content: largeText}}}},
		{name: "history", req: openai.ChatCompletionRequest{Messages: []openai.Message{{Role: "assistant", Content: largeText}, {Role: "user", Content: json.RawMessage(`"continue"`)}}}},
		{name: "tool output", req: openai.ChatCompletionRequest{Messages: []openai.Message{{Role: "tool", Content: largeText}}}},
		{name: "tool schemas", req: openai.ChatCompletionRequest{Extra: map[string]json.RawMessage{"tools": json.RawMessage(`[{"type":"function","function":{"name":"lookup","description":` + string(largeText) + `}}]`)}}},
		{name: "tool calls", req: openai.ChatCompletionRequest{Messages: []openai.Message{{Role: "assistant", Extra: map[string]json.RawMessage{"tool_calls": json.RawMessage(`[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":` + string(largeText) + `}}]`)}}}}},
		{name: "message name", req: openai.ChatCompletionRequest{Messages: []openai.Message{{Role: "user", Name: strings.Repeat("a", 1600)}}}},
		{name: "response schema", req: openai.ChatCompletionRequest{Extra: map[string]json.RawMessage{"response_format": json.RawMessage(`{"type":"json_schema","json_schema":{"name":"result","schema":{"description":` + string(largeText) + `}}}`)}}},
		{name: "output reserve", req: openai.ChatCompletionRequest{MaxTokens: &maxTokens}},
		{name: "completion reserve", req: openai.ChatCompletionRequest{Extra: map[string]json.RawMessage{"max_completion_tokens": json.RawMessage(`400`)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			small := &fakeProvider{name: "small"}
			large := &fakeProvider{name: "large"}
			e := NewEngine(slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
			caps := []string{provider.CapTools, provider.CapJSONMode}
			e.Reload([]storage.ModelAlias{
				{Alias: "small", Provider: "small", Model: "small", MaxContext: 256, Capabilities: caps},
				{Alias: "large", Provider: "large", Model: "large", MaxContext: 4096, Capabilities: caps},
			}, []storage.Combo{intelligentCombo("small", "large")}, provider.NewRegistry(small, large))
			tc.req.Model = "code-agent"
			resp, err := e.ChatCompletion(context.Background(), &tc.req)
			if err != nil {
				t.Fatal(err)
			}
			if resp.ID != "large" || small.callCount() != 0 || large.callCount() != 1 {
				t.Fatalf("served by %q, calls small=%d large=%d", resp.ID, small.callCount(), large.callCount())
			}
		})
	}
}

func TestIntelligentContextWindowBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name       string
		window     int
		maxTokens  int
		completion string
		wantError  bool
	}{
		{name: "exact fit", window: 400, maxTokens: 400},
		{name: "insufficient", window: 399, maxTokens: 400, wantError: true},
		{name: "unknown", window: 0, maxTokens: 1000000},
		{name: "completion takes precedence", window: 400, maxTokens: 1000000, completion: "400"},
		{name: "larger completion takes precedence", window: 400, maxTokens: 1, completion: "401", wantError: true},
		{name: "null completion uses legacy reserve", window: 399, maxTokens: 400, completion: "null", wantError: true},
		{name: "reserve does not overflow", window: 4096, maxTokens: int(^uint(0) >> 1), wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &fakeProvider{name: "p"}
			e := NewEngine(slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
			e.Reload([]storage.ModelAlias{{Alias: "model", Provider: "p", Model: "m", MaxContext: tc.window}}, []storage.Combo{intelligentCombo("model")}, provider.NewRegistry(p))
			req := &openai.ChatCompletionRequest{Model: "code-agent", MaxTokens: &tc.maxTokens}
			if tc.completion != "" {
				req.Extra = map[string]json.RawMessage{"max_completion_tokens": json.RawMessage(tc.completion)}
			}
			_, err := e.ChatCompletion(context.Background(), req)
			if tc.wantError {
				if !errors.Is(err, ErrContextExceeded) || p.callCount() != 0 {
					t.Fatalf("error=%v calls=%d, want context rejection without provider call", err, p.callCount())
				}
			} else if err != nil || p.callCount() != 1 {
				t.Fatalf("error=%v calls=%d, want successful provider call", err, p.callCount())
			}
		})
	}
}

func TestIntelligentContextFiltersAliasFallbacks(t *testing.T) {
	primary := &fakeProvider{name: "primary", results: []error{upstreamErr(429, provider.ErrRateLimited)}}
	small := &fakeProvider{name: "small"}
	unknown := &fakeProvider{name: "unknown"}
	e := NewEngine(slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	e.retry = Retry{Attempts: 1}
	e.Reload([]storage.ModelAlias{
		{Alias: "primary", Provider: "primary", Model: "m", MaxContext: 4096, Fallback: []string{"small", "unknown"}},
		{Alias: "small", Provider: "small", Model: "m", MaxContext: 10},
		{Alias: "unknown", Provider: "unknown", Model: "m"},
	}, []storage.Combo{intelligentCombo("primary")}, provider.NewRegistry(primary, small, unknown))
	reserve := 400
	resp, err := e.ChatCompletion(context.Background(), &openai.ChatCompletionRequest{Model: "code-agent", MaxTokens: &reserve})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "unknown" || small.callCount() != 0 || primary.callCount() != 1 {
		t.Fatalf("served by %q, primary calls=%d small calls=%d", resp.ID, primary.callCount(), small.callCount())
	}
}

func TestIntelligentContextDoesNotCountBase64AsText(t *testing.T) {
	for _, kind := range []string{"image_url", "input_audio"} {
		t.Run(kind, func(t *testing.T) {
			p := &fakeProvider{name: "p"}
			e := NewEngine(slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
			e.Reload([]storage.ModelAlias{{Alias: "model", Provider: "p", Model: "m", MaxContext: 1024, Capabilities: []string{provider.CapVision, provider.CapAudio}}}, []storage.Combo{intelligentCombo("model")}, provider.NewRegistry(p))
			encoded := strconv.Quote(strings.Repeat("AAAA", 10000))
			part := `{"type":"input_audio","input_audio":{"data":` + encoded + `,"format":"wav"}}`
			if kind == "image_url" {
				part = `{"type":"image_url","image_url":{"url":"data:image/png;base64,` + strings.Repeat("AAAA", 10000) + `"}}`
			}
			req := &openai.ChatCompletionRequest{Model: "code-agent", Messages: []openai.Message{{Role: "user", Content: json.RawMessage(`[{"type":"text","text":"describe"},` + part + `]`)}}}
			if _, err := e.ChatCompletion(context.Background(), req); err != nil || p.callCount() != 1 {
				t.Fatalf("error=%v calls=%d, binary bytes must not exhaust the context", err, p.callCount())
			}
		})
	}
}
