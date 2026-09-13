package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/zealish/zealish-router/pkg/openai"
)

// doneSentinel terminates an upstream SSE stream.
const doneSentinel = "[DONE]"

// maxErrorBody caps how much of an upstream error body is read into memory.
const maxErrorBody = 8 << 10

// httpProvider is the shared implementation behind every OpenAI-compatible
// upstream. Concrete providers embed it and supply their own headers.
type httpProvider struct {
	opts    Options
	headers map[string]string
}

func newHTTPProvider(opts Options, headers map[string]string) httpProvider {
	if opts.HTTPClient == nil {
		opts.HTTPClient = http.DefaultClient
	}
	return httpProvider{opts: opts, headers: headers}
}

// Name implements Provider.
func (p *httpProvider) Name() string { return p.opts.Name }

// ChatCompletion performs a non-streaming completion against the upstream.
func (p *httpProvider) ChatCompletion(ctx context.Context, req *openai.ChatCompletionRequest) (*openai.ChatCompletionResponse, error) {
	body := *req
	body.Stream = false
	body.StreamOptions = nil // only meaningful on streaming requests

	resp, err := p.post(ctx, &body, false)
	if err != nil {
		return nil, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	var out openai.ChatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, &Error{Provider: p.opts.Name, Message: fmt.Sprintf("decode response: %v", err)}
	}
	return &out, nil
}

// ChatCompletionStream opens an SSE stream against the upstream. The returned
// channel is closed when the stream terminates, the upstream sends [DONE], or
// ctx is cancelled.
func (p *httpProvider) ChatCompletionStream(ctx context.Context, req *openai.ChatCompletionRequest) (<-chan openai.StreamChunk, error) {
	body := *req
	body.Stream = true

	// The body is closed by the pump goroutine below, which bodyclose cannot see.
	resp, err := p.post(ctx, &body, true) //nolint:bodyclose
	if err != nil {
		return nil, err
	}

	out := make(chan openai.StreamChunk)
	go func() {
		defer close(out)
		defer func() { _ = resp.Body.Close() }()
		p.pump(ctx, resp.Body, out)
	}()
	return out, nil
}

// pump parses the upstream SSE byte stream and forwards decoded chunks.
func (p *httpProvider) pump(ctx context.Context, body io.Reader, out chan<- openai.StreamChunk) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)

	var data strings.Builder
	flush := func() bool {
		defer data.Reset()
		payload := data.String()
		if payload == "" {
			return true
		}
		if payload == doneSentinel {
			return false
		}
		var chunk openai.StreamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return true // skip malformed event, keep the stream alive
		}
		select {
		case out <- chunk:
			return true
		case <-ctx.Done():
			return false
		}
	}

	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" { // event boundary
			if !flush() {
				return
			}
			continue
		}
		if strings.HasPrefix(line, ":") { // comment / keepalive
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		if field != "data" {
			continue
		}
		if data.Len() > 0 {
			data.WriteByte('\n')
		}
		data.WriteString(strings.TrimPrefix(value, " "))
	}
	flush()
}

// post sends the request body and returns a response whose status is 2xx. Any
// other status is converted into a classified *Error and the body is closed.
func (p *httpProvider) post(ctx context.Context, body *openai.ChatCompletionRequest, stream bool) (*http.Response, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, &Error{Provider: p.opts.Name, Message: fmt.Sprintf("encode request: %v", err)}
	}

	endpoint, err := url.JoinPath(p.opts.BaseURL, "chat/completions")
	if err != nil {
		return nil, &Error{Provider: p.opts.Name, Message: fmt.Sprintf("invalid base_url: %v", err)}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, &Error{Provider: p.opts.Name, Message: fmt.Sprintf("build request: %v", err)}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if stream {
		httpReq.Header.Set("Accept", "text/event-stream")
	} else {
		httpReq.Header.Set("Accept", "application/json")
	}
	if p.opts.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.opts.APIKey)
	}
	for k, v := range p.headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := p.opts.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, p.transportError(ctx, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		return nil, p.statusError(resp)
	}
	return resp, nil
}

// transportError classifies a failure that occurred before a response arrived.
func (p *httpProvider) transportError(_ context.Context, err error) error {
	kind := ErrConnection
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		kind = ErrTimeout
	}
	return &Error{Provider: p.opts.Name, Kind: kind, Message: redact(p.opts.APIKey, err.Error())}
}

// statusError maps a non-2xx upstream response onto a classified error.
func (p *httpProvider) statusError(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))

	e := &Error{
		Provider: p.opts.Name,
		Status:   resp.StatusCode,
		Message:  redact(p.opts.APIKey, upstreamMessage(raw)),
	}
	switch {
	case resp.StatusCode == http.StatusRequestTimeout, resp.StatusCode == http.StatusGatewayTimeout:
		e.Kind = ErrTimeout
	case resp.StatusCode == http.StatusTooManyRequests:
		e.Kind = ErrRateLimited
	case resp.StatusCode >= 500:
		e.Kind = ErrUpstream5xx
	}
	return e
}

// upstreamMessage extracts the OpenAI error message from a body, falling back
// to the trimmed raw text.
func upstreamMessage(raw []byte) string {
	var envelope openai.ErrorResponse
	if err := json.Unmarshal(raw, &envelope); err == nil && envelope.Error.Message != "" {
		return envelope.Error.Message
	}
	return strings.TrimSpace(string(raw))
}
