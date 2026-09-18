package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/zealish/zealish-router/pkg/openai"
)

const commandCodeDefaultMaxTokens = 64000
const commandCodeErrorKey = "commandcode_error"

type CommandCode struct{ opts Options }

func NewCommandCode(opts Options) *CommandCode {
	if opts.Name == "" {
		opts.Name = "commandcode"
	}
	if opts.BaseURL == "" {
		opts.BaseURL = "https://api.commandcode.ai"
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = http.DefaultClient
	}
	return &CommandCode{opts: opts}
}
func (p *CommandCode) Name() string         { return p.opts.Name }
func (p *CommandCode) Client() *http.Client { return p.opts.HTTPClient }

func (p *CommandCode) ChatCompletion(ctx context.Context, req *openai.ChatCompletionRequest) (*openai.ChatCompletionResponse, error) {
	ch, err := p.ChatCompletionStream(ctx, req)
	if err != nil {
		return nil, err
	}
	var text, reasoning strings.Builder
	var usage *openai.Usage
	id, model := "", ""
	var created int64
	finish := "stop"
	for c := range ch {
		if raw := c.Extra[commandCodeErrorKey]; len(raw) > 0 {
			var m string
			_ = json.Unmarshal(raw, &m)
			return nil, p.eventError(m)
		}
		id, model, created = c.ID, c.Model, c.Created
		if c.Usage != nil {
			usage = mergeCommandUsage(usage, c.Usage)
		}
		if len(c.Choices) == 0 || c.Choices[0].Delta == nil {
			continue
		}
		d := c.Choices[0].Delta
		if s, ok := d.Text(); ok {
			text.WriteString(s)
		}
		if raw := d.Extra["reasoning_content"]; len(raw) > 0 {
			var s string
			_ = json.Unmarshal(raw, &s)
			reasoning.WriteString(s)
		}
		if c.Choices[0].FinishReason != nil {
			finish = *c.Choices[0].FinishReason
		}
	}
	m := &openai.Message{Role: "assistant", Content: mustJSON(text.String())}
	if reasoning.Len() > 0 {
		m.Extra = map[string]json.RawMessage{"reasoning_content": mustJSON(reasoning.String())}
	}
	return &openai.ChatCompletionResponse{ID: id, Object: "chat.completion", Created: created, Model: model, Choices: []openai.Choice{{Index: 0, Message: m, FinishReason: &finish}}, Usage: usage}, nil
}

func (p *CommandCode) ChatCompletionStream(ctx context.Context, req *openai.ChatCompletionRequest) (<-chan openai.StreamChunk, error) {
	payload, err := json.Marshal(commandCodeRequest(req))
	if err != nil {
		return nil, &Error{Provider: p.Name(), Message: fmt.Sprintf("encode request: %v", err)}
	}
	endpoint := strings.TrimRight(p.opts.BaseURL, "/") + "/alpha/generate"
	h, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, &Error{Provider: p.Name(), Message: fmt.Sprintf("build request: %v", err)}
	}
	h.Header.Set("Content-Type", "application/json")
	h.Header.Set("Accept", "application/x-ndjson")
	h.Header.Set("x-command-code-version", "0.25.7")
	h.Header.Set("x-cli-environment", "cli")
	h.Header.Set("x-session-id", uuid.NewString())
	if p.opts.APIKey != "" {
		h.Header.Set("Authorization", "Bearer "+p.opts.APIKey)
	}
	resp, err := p.opts.HTTPClient.Do(h)
	if err != nil {
		return nil, p.transportError(err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		return nil, p.statusError(resp)
	}
	out := make(chan openai.StreamChunk)
	go func() { defer close(out); defer resp.Body.Close(); p.scan(ctx, resp.Body, req.Model, out) }()
	return out, nil
}
func (p *CommandCode) scan(ctx context.Context, body io.Reader, model string, out chan<- openai.StreamChunk) {
	s := bufio.NewScanner(body)
	s.Buffer(make([]byte, 64<<10), 4<<20)
	st := commandCodeState{id: "chatcmpl-" + uuid.NewString(), created: time.Now().Unix(), model: model}
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || line == "[DONE]" {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		var e map[string]any
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		if typ, _ := e["type"].(string); typ == "error" {
			p.emit(ctx, out, []openai.StreamChunk{st.errorChunk(commandCodeEventMessage(e))})
			return
		}
		p.emit(ctx, out, st.translate(e))
	}
	if err := s.Err(); err != nil {
		p.emit(ctx, out, []openai.StreamChunk{st.errorChunk(redact(p.opts.APIKey, err.Error()))})
	}
}
func (p *CommandCode) emit(ctx context.Context, out chan<- openai.StreamChunk, cs []openai.StreamChunk) {
	for _, c := range cs {
		select {
		case out <- c:
		case <-ctx.Done():
			return
		}
	}
}

type commandCodeState struct {
	id      string
	created int64
	model   string
	finish  string
}

func (s *commandCodeState) base(d openai.Message, f *string) openai.StreamChunk {
	return openai.StreamChunk{ID: s.id, Object: "chat.completion.chunk", Created: s.created, Model: s.model, Choices: []openai.Choice{{Index: 0, Delta: &d, FinishReason: f}}}
}
func (s *commandCodeState) translate(e map[string]any) []openai.StreamChunk {
	typ, _ := e["type"].(string)
	a := openai.Message{Role: "assistant"}
	switch typ {
	case "text-delta":
		x, _ := e["text"].(string)
		if x == "" {
			return nil
		}
		a.Content = mustJSON(x)
		return []openai.StreamChunk{s.base(a, nil)}
	case "reasoning-delta":
		x, _ := e["text"].(string)
		if x == "" {
			return nil
		}
		a.Extra = map[string]json.RawMessage{"reasoning_content": mustJSON(x)}
		return []openai.StreamChunk{s.base(a, nil)}
	case "finish-step":
		s.finish, _ = e["finishReason"].(string)
		return nil
	case "finish":
		r := s.finish
		if r == "" {
			r = "stop"
		}
		return []openai.StreamChunk{s.base(a, &r)}
	case "usage":
		if u := commandCodeUsage(e); u != nil {
			return []openai.StreamChunk{{ID: s.id, Object: "chat.completion.chunk", Created: s.created, Model: s.model, Usage: u}}
		}
	}
	return nil
}
func (s *commandCodeState) errorChunk(m string) openai.StreamChunk {
	return openai.StreamChunk{ID: s.id, Object: "chat.completion.chunk", Created: s.created, Model: s.model, Extra: map[string]json.RawMessage{commandCodeErrorKey: mustJSON(m)}}
}
func commandCodeEventMessage(e map[string]any) string {
	if s, ok := e["message"].(string); ok {
		return s
	}
	if s, ok := e["error"].(string); ok {
		return s
	}
	return "CommandCode upstream error"
}
func (p *CommandCode) eventError(m string) error {
	st := commandCodeStatus(m)
	return &Error{Provider: p.Name(), Status: st, Kind: commandCodeKind(st), Message: redact(p.opts.APIKey, m)}
}
func commandCodeStatus(m string) int {
	l := strings.ToLower(m)
	switch {
	case strings.Contains(l, "rate limit"), strings.Contains(l, "too many"):
		return 429
	case strings.Contains(l, "unauthorized"), strings.Contains(l, "invalid api key"), strings.Contains(l, "authentication"):
		return 401
	case strings.Contains(l, "forbidden"), strings.Contains(l, "permission"):
		return 403
	case strings.Contains(l, "not found"):
		return 404
	default:
		return 503
	}
}
func commandCodeKind(st int) error {
	if st == 429 {
		return ErrRateLimited
	}
	if st >= 500 {
		return ErrUpstream5xx
	}
	return nil
}
func (p *CommandCode) transportError(err error) error {
	k := ErrConnection
	if err == context.DeadlineExceeded || strings.Contains(strings.ToLower(err.Error()), "timeout") {
		k = ErrTimeout
	}
	return &Error{Provider: p.Name(), Kind: k, Message: redact(p.opts.APIKey, err.Error())}
}
func (p *CommandCode) statusError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	return &Error{Provider: p.Name(), Status: resp.StatusCode, Kind: commandCodeKind(resp.StatusCode), Message: redact(p.opts.APIKey, strings.TrimSpace(string(b)))}
}
func commandCodeRequest(req *openai.ChatCompletionRequest) map[string]any {
	msgs := make([]map[string]any, 0, len(req.Messages))
	var system []string
	for _, m := range req.Messages {
		if m.Role == "system" || m.Role == "developer" {
			if s, ok := m.Text(); ok {
				system = append(system, s)
			}
			continue
		}
		if m.Role == "tool" {
			s, _ := m.Text()
			msgs = append(msgs, map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool-result", "toolCallId": string(m.Extra["tool_call_id"]), "toolName": m.Name, "output": map[string]any{"type": "text", "value": s}}}})
			continue
		}
		var content any
		_ = json.Unmarshal(m.Content, &content)
		msgs = append(msgs, map[string]any{"role": m.Role, "content": []any{map[string]any{"type": "text", "text": content}}})
	}
	params := map[string]any{"model": req.Model, "messages": msgs, "stream": true, "max_tokens": commandCodeDefaultMaxTokens, "temperature": 0.3}
	if req.MaxTokens != nil {
		params["max_tokens"] = *req.MaxTokens
	}
	if req.Temperature != nil {
		params["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		params["top_p"] = *req.TopP
	}
	if len(system) > 0 {
		params["system"] = strings.Join(system, "\n\n")
	}
	if raw := req.Extra["tools"]; len(raw) > 0 {
		var tools any
		_ = json.Unmarshal(raw, &tools)
		params["tools"] = tools
	}
	return map[string]any{"threadId": uuid.NewString(), "memory": "", "config": map[string]any{"workingDir": "", "date": time.Now().Format("2006-01-02"), "environment": "linux", "structure": []any{}, "isGitRepo": false, "currentBranch": "", "mainBranch": "", "gitStatus": "", "recentCommits": []any{}}, "params": params}
}
func commandCodeUsage(e map[string]any) *openai.Usage {
	u := &openai.Usage{}
	if x, ok := e["prompt_tokens"].(float64); ok {
		u.PromptTokens = int(x)
	}
	if x, ok := e["completion_tokens"].(float64); ok {
		u.CompletionTokens = int(x)
	}
	if x, ok := e["total_tokens"].(float64); ok {
		u.TotalTokens = int(x)
	} else {
		u.TotalTokens = u.PromptTokens + u.CompletionTokens
	}
	return u
}
func mergeCommandUsage(a, b *openai.Usage) *openai.Usage {
	if a == nil {
		return b
	}
	a.PromptTokens += b.PromptTokens
	a.CompletionTokens += b.CompletionTokens
	a.TotalTokens += b.TotalTokens
	return a
}
func mustJSON(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
func (p *CommandCode) Embeddings(context.Context, *openai.EmbeddingRequest) (*openai.EmbeddingResponse, error) {
	return nil, &Error{Provider: p.Name(), Kind: ErrUnsupported, Message: "the CommandCode API has no embeddings endpoint"}
}
func (p *CommandCode) ListModels(context.Context) ([]Model, error) {
	ids := []string{"deepseek/deepseek-v4-pro", "deepseek/deepseek-v4-flash", "moonshotai/Kimi-K2.6", "moonshotai/Kimi-K2.5", "zai-org/GLM-5.1", "zai-org/GLM-5", "MiniMaxAI/MiniMax-M2.7", "MiniMaxAI/MiniMax-M2.5", "Qwen/Qwen3.6-Max-Preview", "Qwen/Qwen3.6-Plus", "stepfun/Step-3.5-Flash"}
	out := make([]Model, len(ids))
	for i, id := range ids {
		out[i] = Model{ID: id, OwnedBy: "commandcode"}
	}
	return out, nil
}
