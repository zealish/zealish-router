package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/zealish/zealish-router/pkg/openai"
)

const commandCodeDefaultMaxTokens = 64000
const commandCodeErrorKey = "commandcode_error"

type CommandCode struct{ httpProvider }

func NewCommandCode(opts Options) *CommandCode {
	if opts.Name == "" {
		opts.Name = "commandcode"
	}
	if opts.BaseURL == "" {
		opts.BaseURL = "https://api.commandcode.ai"
	}
	return &CommandCode{httpProvider: newHTTPProvider(opts, nil)}
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
	var toolCalls []map[string]any
	id, model := "", ""
	var created int64
	finish := "stop"
	for c := range ch {
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
		if raw := d.Extra["tool_calls"]; len(raw) > 0 {
			var deltas []map[string]any
			if json.Unmarshal(raw, &deltas) == nil {
				for _, delta := range deltas {
					idx, _ := delta["index"].(float64)
					for len(toolCalls) <= int(idx) {
						toolCalls = append(toolCalls, map[string]any{"type": "function", "function": map[string]any{}})
					}
					call := toolCalls[int(idx)]
					if v, ok := delta["id"]; ok {
						call["id"] = v
					}
					if v, ok := delta["type"]; ok {
						call["type"] = v
					}
					fn, _ := call["function"].(map[string]any)
					df, _ := delta["function"].(map[string]any)
					if v, ok := df["name"]; ok {
						fn["name"] = v
					}
					if v, ok := df["arguments"].(string); ok {
						old, _ := fn["arguments"].(string)
						fn["arguments"] = old + v
					}
					call["function"] = fn
				}
			}
		}
		if c.Choices[0].FinishReason != nil {
			finish = *c.Choices[0].FinishReason
		}
	}
	m := &openai.Message{Role: "assistant", Content: mustJSON(text.String())}
	if reasoning.Len() > 0 {
		m.Extra = map[string]json.RawMessage{"reasoning_content": mustJSON(reasoning.String())}
	}
	if len(toolCalls) > 0 {
		if m.Extra == nil {
			m.Extra = make(map[string]json.RawMessage)
		}
		m.Extra["tool_calls"] = mustJSON(toolCalls)
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
	h.Header.Set("Accept", "text/event-stream")
	h.Header.Set("x-command-code-version", "0.25.7")
	h.Header.Set("x-cli-environment", "cli")
	h.Header.Set("x-session-id", uuid.NewString())
	key := p.credential()
	if key != "" {
		h.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := p.opts.HTTPClient.Do(h)
	if err != nil {
		return nil, p.transportError(err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		return nil, p.statusError(resp)
	}
	// Peek metadata before exposing the stream, matching the reference executor.
	br := bufio.NewReader(resp.Body)
	var peek []string
	for {
		line, readErr := br.ReadString('\n')
		if len(line) > 0 {
			trimmed := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "data:"))
			if trimmed != "" {
				peek = append(peek, line)
				if trimmed == "[DONE]" {
					break
				}
				var event map[string]any
				if json.Unmarshal([]byte(trimmed), &event) == nil {
					typ, _ := event["type"].(string)
					if typ == "error" {
						resp.Body.Close()
						return nil, p.eventErrorWithStatus(event)
					}
					if typ == "text-delta" || typ == "reasoning-delta" || typ == "tool-input-start" || typ == "tool-call" || typ == "finish-step" || typ == "finish" {
						break
					}
				}
			}
		}
		if readErr != nil {
			if readErr == io.EOF && len(peek) > 0 {
				break
			}
			resp.Body.Close()
			if readErr == io.EOF {
				return nil, &Error{Provider: p.Name(), Message: "empty CommandCode response"}
			}
			return nil, p.transportError(readErr)
		}
	}
	body := io.MultiReader(strings.NewReader(strings.Join(peek, "")), br)
	out := make(chan openai.StreamChunk)
	go func() { defer close(out); defer resp.Body.Close(); p.scan(ctx, body, req.Model, out) }()
	return out, nil
}
func (p *CommandCode) scan(ctx context.Context, body io.Reader, model string, out chan<- openai.StreamChunk) {
	s := bufio.NewScanner(body)
	// Tool arguments can be large; match the reference's line-oriented NDJSON
	// handling without Scanner's small default token limit.
	s.Buffer(make([]byte, 64*1024), 4*1024*1024)
	st := commandCodeState{id: "chatcmpl-" + uuid.NewString(), created: time.Now().Unix(), model: model, toolIdx: make(map[string]int)}
	for s.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s.Text()), "data:"))
		if line == "[DONE]" {
			return
		}
		if line == "" {
			continue
		}
		var e map[string]any
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		typ, _ := e["type"].(string)
		if typ == "error" {
			m := commandCodeEventMessage(e)
			p.emit(ctx, out, []openai.StreamChunk{st.errorChunk(m), st.errorFinishChunk()})
			return
		}
		p.emit(ctx, out, st.translate(e))
		// CommandCode's finish event is the stream terminator.  Do not wait for
		// an upstream connection close: OMP treats an open response as loading.
		if typ == "finish" {
			return
		}
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
	id       string
	created  int64
	model    string
	finish   string
	toolIdx  map[string]int
	nextTool int
	started  bool
	usage    *openai.Usage
}

func (s *commandCodeState) base(d openai.Message, f *string) openai.StreamChunk {
	return openai.StreamChunk{ID: s.id, Object: "chat.completion.chunk", Created: s.created, Model: s.model, Choices: []openai.Choice{{Index: 0, Delta: &d, FinishReason: f}}}
}
func (s *commandCodeState) translate(e map[string]any) []openai.StreamChunk {
	typ, _ := e["type"].(string)
	a := openai.Message{Role: "assistant"}
	first := !s.started
	if !first {
		a.Role = ""
	}
	switch typ {
	case "text-delta":
		x, _ := e["text"].(string)
		if x == "" {
			x, _ = e["delta"].(string)
		}
		if x == "" {
			return nil
		}
		a.Content = mustJSON(x)
		s.started = true
		return []openai.StreamChunk{s.base(a, nil)}
	case "reasoning-delta":
		x, _ := e["text"].(string)
		if x == "" {
			return nil
		}
		a.Extra = map[string]json.RawMessage{"reasoning_content": mustJSON(x)}
		s.started = true
		return []openai.StreamChunk{s.base(a, nil)}
	case "tool-input-start":
		id, _ := e["id"].(string)
		if id == "" {
			id, _ = e["toolCallId"].(string)
		}
		if id == "" {
			id = fmt.Sprintf("call_%d", s.nextTool)
		}
		idx, ok := s.toolIdx[id]
		if !ok {
			idx = s.nextTool
			s.nextTool++
			s.toolIdx[id] = idx
		}
		name, _ := e["toolName"].(string)
		a.Extra = map[string]json.RawMessage{"tool_calls": mustJSON([]any{map[string]any{"index": idx, "id": id, "type": "function", "function": map[string]any{"name": name, "arguments": ""}}})}
		s.started = true
		return []openai.StreamChunk{s.base(a, nil)}
	case "tool-call":
		id, _ := e["toolCallId"].(string)
		if id == "" {
			id, _ = e["id"].(string)
		}
		if id == "" {
			id = fmt.Sprintf("call_%d", s.nextTool)
		}
		if _, ok := s.toolIdx[id]; ok {
			return nil
		}
		idx := s.nextTool
		s.nextTool++
		s.toolIdx[id] = idx
		name, _ := e["toolName"].(string)
		args := "{}"
		if x, ok := e["input"].(string); ok {
			args = x
		} else if e["input"] != nil {
			b, _ := json.Marshal(e["input"])
			args = string(b)
		}
		a.Extra = map[string]json.RawMessage{"tool_calls": mustJSON([]any{map[string]any{"index": idx, "id": id, "type": "function", "function": map[string]any{"name": name, "arguments": args}}})}
		s.started = true
		return []openai.StreamChunk{s.base(a, nil)}
	case "tool-input-delta":
		id, _ := e["id"].(string)
		if id == "" {
			id, _ = e["toolCallId"].(string)
		}
		idx, ok := s.toolIdx[id]
		if !ok {
			return nil
		}
		delta, _ := e["delta"].(string)
		if delta == "" {
			delta, _ = e["inputTextDelta"].(string)
		}
		a.Extra = map[string]json.RawMessage{"tool_calls": mustJSON([]any{map[string]any{"index": idx, "function": map[string]any{"arguments": delta}}})}
		return []openai.StreamChunk{s.base(a, nil)}
	case "finish-step":
		s.finish = commandCodeFinishReason(stringValue(e["finishReason"]))
		if u, ok := e["usage"].(map[string]any); ok {
			s.usage = commandCodeUsage(u)
		}
		return nil
	case "finish":
		r := s.finish
		if r == "" {
			r = commandCodeFinishReason(stringValue(e["finishReason"]))
		}
		if r == "" {
			r = "stop"
		}
		c := s.base(openai.Message{}, &r)
		c.Usage = s.usage
		if u, ok := e["totalUsage"].(map[string]any); ok {
			c.Usage = commandCodeUsage(u)
		}
		return []openai.StreamChunk{c}
	}
	return nil
}
func stringValue(v any) string { s, _ := v.(string); return s }
func commandCodeFinishReason(reason string) string {
	switch reason {
	case "stop":
		return "stop"
	case "length":
		return "length"
	case "tool-calls", "tool_use":
		return "tool_calls"
	case "content-filter":
		return "content_filter"
	case "error":
		return "stop"
	}
	return reason
}
func (s *commandCodeState) errorChunk(m string) openai.StreamChunk {
	return openai.StreamChunk{ID: s.id, Object: "chat.completion.chunk", Created: s.created, Model: s.model, Choices: []openai.Choice{{Index: 0, Delta: &openai.Message{Role: "assistant", Content: mustJSON("\n\n[CommandCode error: " + m + "]")}}}, Extra: map[string]json.RawMessage{commandCodeErrorKey: mustJSON(m)}}
}
func (s *commandCodeState) errorFinishChunk() openai.StreamChunk {
	reason := "stop"
	return s.base(openai.Message{}, &reason)
}
func commandCodeEventMessage(e map[string]any) string {
	if v, ok := e["error"]; ok && v != nil {
		if s, ok := v.(string); ok {
			return s
		}
		if raw, err := json.Marshal(v); err == nil {
			return string(raw)
		}
	}
	if v, ok := e["message"]; ok && v != nil {
		if s, ok := v.(string); ok {
			return s
		}
		if raw, err := json.Marshal(v); err == nil {
			return string(raw)
		}
	}
	return "CommandCode upstream error"
}
func (p *CommandCode) eventError(m string) error {
	st := commandCodeStatus(m)
	return &Error{Provider: p.Name(), Status: st, Kind: commandCodeKind(st), Message: redact(p.opts.APIKey, m)}
}
func (p *CommandCode) eventErrorWithStatus(e map[string]any) error {
	m := commandCodePreflightMessage(e)
	status := commandCodeEventStatus(e)
	if status == 0 {
		status = commandCodeStatus(m)
	}
	return &Error{Provider: p.Name(), Status: status, Kind: commandCodeKind(status), Message: redact(p.opts.APIKey, m)}
}
func commandCodePreflightMessage(e map[string]any) string {
	v, ok := e["error"]
	if !ok || v == nil {
		v, ok = e["message"]
	}
	if !ok || v == nil {
		return "CommandCode upstream error"
	}
	if s, ok := v.(string); ok {
		return s
	}
	if obj, ok := v.(map[string]any); ok {
		if s, ok := obj["message"].(string); ok {
			return s
		}
		if s, ok := obj["error"].(string); ok {
			return s
		}
	}
	if raw, err := json.Marshal(v); err == nil {
		return string(raw)
	}
	return "CommandCode upstream error"
}
func commandCodeEventStatus(e map[string]any) int {
	for _, v := range []any{e["statusCode"], e["code"]} {
		if n := commandCodeStatusValue(v); n != 0 {
			return n
		}
	}
	for _, key := range []string{"error", "message"} {
		if obj, ok := e[key].(map[string]any); ok {
			for _, v := range []any{obj["statusCode"], obj["status"], obj["code"]} {
				if n := commandCodeStatusValue(v); n != 0 {
					return n
				}
			}
		}
	}
	return 0
}
func commandCodeStatusValue(v any) int {
	switch n := v.(type) {
	case float64:
		if int(n) >= 400 && int(n) <= 599 {
			return int(n)
		}
	case string:
		var i int
		if _, err := fmt.Sscanf(n, "%d", &i); err == nil && i >= 400 && i <= 599 {
			return i
		}
	}
	return 0
}
func commandCodeStatus(m string) int {
	l := strings.ToLower(m)
	switch {
	case strings.Contains(l, "rate limit"), strings.Contains(l, "too many"):
		return 429
	case strings.Contains(l, "unauthorized"), strings.Contains(l, "invalid api key"), strings.Contains(l, "authentication"):
		return 401
	case strings.Contains(l, "payment required"), strings.Contains(l, "billing"):
		return 402
	case strings.Contains(l, "forbidden"), strings.Contains(l, "permission"), strings.Contains(l, "quota"):
		return 403
	case strings.Contains(l, "not found"):
		return 404
	case strings.Contains(l, "unavailable"), strings.Contains(l, "overloaded"), strings.Contains(l, "server error"):
		return 503
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
	message := strings.TrimSpace(string(b))
	status := resp.StatusCode
	var payload map[string]any
	if json.Unmarshal(b, &payload) == nil {
		obj := payload
		if nested, ok := payload["error"].(map[string]any); ok {
			obj = nested
		}
		for _, key := range []string{"statusCode", "code"} {
			if parsed := commandCodeStatusValue(obj[key]); parsed != 0 {
				status = parsed
				break
			}
		}
		if v, ok := obj["message"].(string); ok && v != "" {
			message = v
		} else if v, ok := payload["message"].(string); ok && v != "" {
			message = v
		}
	}
	return &Error{Provider: p.Name(), Status: status, Kind: commandCodeKind(status), Message: redact(p.opts.APIKey, message)}
}
func commandCodeRequest(req *openai.ChatCompletionRequest) map[string]any {
	msgs := make([]map[string]any, 0, len(req.Messages))
	var system []string
	for _, m := range req.Messages {
		if m.Role == "system" {
			if text := commandCodeFlattenText(m.Content); text != "" {
				system = append(system, text)
			}
			continue
		}
		if m.Role == "tool" {
			msgs = append(msgs, map[string]any{
				"role": "tool",
				"content": []any{map[string]any{
					"type":       "tool-result",
					"toolCallId": commandCodeRawString(m.Extra["tool_call_id"]),
					"toolName":   m.Name,
					"output":     map[string]any{"type": "text", "value": commandCodeFlattenText(m.Content)},
				}},
			})
			continue
		}
		if m.Role == "assistant" {
			blocks := make([]any, 0, 1)
			if text := commandCodeFlattenText(m.Content); text != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": text})
			}
			var calls []map[string]any
			_ = json.Unmarshal(m.Extra["tool_calls"], &calls)
			for _, call := range calls {
				fn, _ := call["function"].(map[string]any)
				input := fn["arguments"]
				if input == nil {
					input = map[string]any{}
				} else if args, ok := input.(string); ok {
					if json.Unmarshal([]byte(args), &input) != nil {
						input = map[string]any{}
					}
				}
				blocks = append(blocks, map[string]any{"type": "tool-call", "toolCallId": commandCodeJSString(call["id"]), "toolName": commandCodeJSString(fn["name"]), "input": input})
			}
			if len(blocks) == 0 {
				blocks = []any{map[string]any{"type": "text", "text": ""}}
			}
			msgs = append(msgs, map[string]any{"role": "assistant", "content": blocks})
			continue
		}
		msgs = append(msgs, map[string]any{"role": "user", "content": commandCodeContentBlocks(m.Content)})
	}
	params := map[string]any{"model": req.Model, "messages": msgs, "stream": true, "max_tokens": commandCodeDefaultMaxTokens, "temperature": 0.3}
	if req.MaxTokens != nil {
		params["max_tokens"] = *req.MaxTokens
	} else if raw := req.Extra["max_output_tokens"]; len(raw) > 0 {
		var maxOutput *int
		if json.Unmarshal(raw, &maxOutput) == nil && maxOutput != nil {
			params["max_tokens"] = *maxOutput
		}
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
		var in []map[string]any
		if json.Unmarshal(raw, &in) == nil {
			tools := make([]map[string]any, 0, len(in))
			for _, t := range in {
				var name string
				var description any
				var hasDescription bool
				var schema any
				if t["type"] == "function" {
					fn, _ := t["function"].(map[string]any)
					name, _ = fn["name"].(string)
					description, hasDescription = fn["description"]
					schema = fn["parameters"]
				} else {
					name, _ = t["name"].(string)
					description, hasDescription = t["description"]
					schema = t["input_schema"]
					if schema == nil {
						schema = t["parameters"]
					}
				}
				if name == "" {
					continue
				}
				if schema == nil {
					schema = map[string]any{"type": "object"}
				}
				tool := map[string]any{"name": name, "input_schema": schema}
				if hasDescription {
					tool["description"] = description
				}
				tools = append(tools, tool)
			}
			if len(tools) > 0 {
				params["tools"] = tools
			}
		}
	}
	wd, _ := os.Getwd()
	return map[string]any{"threadId": uuid.NewString(), "memory": "", "config": map[string]any{"workingDir": wd, "date": time.Now().UTC().Format("2006-01-02"), "environment": runtime.GOOS, "structure": []any{}, "isGitRepo": false, "currentBranch": "", "mainBranch": "", "gitStatus": "", "recentCommits": []any{}}, "params": params}
}

func commandCodeRawString(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

func commandCodeJSString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case map[string]any:
		return "[object Object]"
	default:
		return fmt.Sprint(x)
	}
}

func commandCodeFlattenText(raw json.RawMessage) string {
	var v any
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	if parts, ok := v.([]any); ok {
		var out []string
		for _, part := range parts {
			if s, ok := part.(string); ok {
				out = append(out, s)
			} else if p, ok := part.(map[string]any); ok {
				if s, ok := p["text"].(string); ok {
					out = append(out, s)
				}
			}
		}
		return strings.Join(out, "\n")
	}
	return commandCodeJSString(v)
}

func commandCodeContentBlocks(raw json.RawMessage) []any {
	var v any
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil {
		return []any{map[string]any{"type": "text", "text": ""}}
	}
	if s, ok := v.(string); ok {
		return []any{map[string]any{"type": "text", "text": s}}
	}
	if parts, ok := v.([]any); ok {
		out := make([]any, 0, len(parts))
		for _, part := range parts {
			if s, ok := part.(string); ok {
				out = append(out, map[string]any{"type": "text", "text": s})
				continue
			}
			p, _ := part.(map[string]any)
			if p["type"] == "image_url" || p["type"] == "image" {
				out = append(out, map[string]any{"type": "text", "text": "[image omitted]"})
			} else if s, ok := p["text"].(string); ok {
				out = append(out, map[string]any{"type": "text", "text": s})
			}
		}
		if len(out) > 0 {
			return out
		}
		return []any{map[string]any{"type": "text", "text": ""}}
	}
	return []any{map[string]any{"type": "text", "text": commandCodeJSString(v)}}
}
func commandCodeUsage(e map[string]any) *openai.Usage {
	u := &openai.Usage{}
	if x, ok := e["inputTokens"].(float64); ok {
		u.PromptTokens = int(x)
	} else if x, ok := e["prompt_tokens"].(float64); ok {
		u.PromptTokens = int(x)
	}
	if x, ok := e["outputTokens"].(float64); ok {
		u.CompletionTokens = int(x)
	} else if x, ok := e["completion_tokens"].(float64); ok {
		u.CompletionTokens = int(x)
	}
	if x, ok := e["totalTokens"].(float64); ok {
		u.TotalTokens = int(x)
	} else if x, ok := e["total_tokens"].(float64); ok {
		u.TotalTokens = int(x)
	} else {
		u.TotalTokens = u.PromptTokens + u.CompletionTokens
	}

	// Cached tokens: try the nested prompt_tokens_details object first, then
	// flat fields in both camelCase and snake_case.
	if d, ok := e["prompt_tokens_details"].(map[string]any); ok {
		ptd := &openai.PromptTokensDetails{}
		if x, ok := d["cached_tokens"].(float64); ok {
			ptd.CachedTokens = int(x)
		}
		if x, ok := d["cache_write_tokens"].(float64); ok {
			ptd.CacheWriteTokens = int(x)
		}
		if ptd.CachedTokens > 0 || ptd.CacheWriteTokens > 0 {
			u.PromptTokensDetails = ptd
		}
	}
	if u.PromptTokensDetails == nil {
		var cached, written int
		if x, ok := e["cachedTokens"].(float64); ok {
			cached = int(x)
		} else if x, ok := e["cached_tokens"].(float64); ok {
			cached = int(x)
		} else if x, ok := e["cache_read_input_tokens"].(float64); ok {
			cached = int(x)
		}
		if x, ok := e["cacheWriteTokens"].(float64); ok {
			written = int(x)
		} else if x, ok := e["cache_write_tokens"].(float64); ok {
			written = int(x)
		} else if x, ok := e["cache_creation_input_tokens"].(float64); ok {
			written = int(x)
		}
		if cached > 0 || written > 0 {
			u.PromptTokensDetails = &openai.PromptTokensDetails{CachedTokens: cached, CacheWriteTokens: written}
		}
	}

	if d, ok := e["completion_tokens_details"].(map[string]any); ok {
		if x, ok := d["reasoning_tokens"].(float64); ok && int(x) > 0 {
			u.CompletionTokensDetails = &openai.CompletionTokensDetails{ReasoningTokens: int(x)}
		}
	}
	if u.CompletionTokensDetails == nil {
		if x, ok := e["reasoningTokens"].(float64); ok && int(x) > 0 {
			u.CompletionTokensDetails = &openai.CompletionTokensDetails{ReasoningTokens: int(x)}
		} else if x, ok := e["reasoning_tokens"].(float64); ok && int(x) > 0 {
			u.CompletionTokensDetails = &openai.CompletionTokensDetails{ReasoningTokens: int(x)}
		}
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
	if b.PromptTokensDetails != nil {
		if a.PromptTokensDetails == nil {
			a.PromptTokensDetails = &openai.PromptTokensDetails{}
		}
		a.PromptTokensDetails.CachedTokens += b.PromptTokensDetails.CachedTokens
		a.PromptTokensDetails.CacheWriteTokens += b.PromptTokensDetails.CacheWriteTokens
	}
	if b.CompletionTokensDetails != nil {
		if a.CompletionTokensDetails == nil {
			a.CompletionTokensDetails = &openai.CompletionTokensDetails{}
		}
		a.CompletionTokensDetails.ReasoningTokens += b.CompletionTokensDetails.ReasoningTokens
	}
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
