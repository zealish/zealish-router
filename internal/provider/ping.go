package provider

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// Pinger is implemented by providers that can answer a cheap liveness probe.
// Ping reports nil when the upstream is reachable; the error carries the
// usual retryable classification otherwise.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Ping issues a GET against {base_url}/models and discards the body. Any
// response at all — including a 4xx — proves the upstream is reachable, so
// only transport failures, timeouts and 5xx responses are reported as errors.
func (p *httpProvider) Ping(ctx context.Context) error {
	endpoint, err := url.JoinPath(p.opts.BaseURL, "models")
	if err != nil {
		return &Error{Provider: p.opts.Name, Message: fmt.Sprintf("invalid base_url: %v", err)}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return &Error{Provider: p.opts.Name, Message: fmt.Sprintf("build request: %v", err)}
	}
	req.Header.Set("Accept", "application/json")
	p.applyHeaders(req)

	resp, err := p.opts.HTTPClient.Do(req)
	if err != nil {
		return p.transportError(ctx, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBody))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode >= 500 {
		return p.statusError(resp)
	}
	return nil
}
