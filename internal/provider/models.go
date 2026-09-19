package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// maxModelsBody caps how much of a /models response is read into memory.
const maxModelsBody = 4 << 20

// Model is one entry of an upstream's GET /models catalogue.
type Model struct {
	ID            string `json:"id"`
	OwnedBy       string `json:"owned_by,omitempty"`
	Created       int64  `json:"created,omitempty"`
	ContextLength int    `json:"context_length,omitempty"`
	ContextWindow int    `json:"context_window,omitempty"`
	MaxContext    int    `json:"max_context,omitempty"`
}

// Context returns the first positive context-window value advertised by the upstream.
func (m Model) Context() int {
	for _, value := range []int{m.ContextLength, m.ContextWindow, m.MaxContext} {
		if value > 0 {
			return value
		}
	}
	return 0
}

// ModelLister is implemented by providers that can enumerate their catalogue.
type ModelLister interface {
	ListModels(ctx context.Context) ([]Model, error)
}

// ListModels fetches {base_url}/models and returns the advertised catalogue.
func (p *httpProvider) ListModels(ctx context.Context) ([]Model, error) {
	endpoint, err := url.JoinPath(p.opts.BaseURL, "models")
	if err != nil {
		return nil, &Error{Provider: p.opts.Name, Message: fmt.Sprintf("invalid base_url: %v", err)}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, &Error{Provider: p.opts.Name, Message: fmt.Sprintf("build request: %v", err)}
	}
	req.Header.Set("Accept", "application/json")
	p.applyHeaders(req)

	resp, err := p.opts.HTTPClient.Do(req)
	if err != nil {
		return nil, p.transportError(ctx, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, p.statusError(resp)
	}

	var body struct {
		Data []Model `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxModelsBody)).Decode(&body); err != nil {
		return nil, &Error{Provider: p.opts.Name, Message: fmt.Sprintf("decode response: %v", err)}
	}

	models := make([]Model, 0, len(body.Data))
	for _, m := range body.Data {
		if m.ID != "" {
			models = append(models, m)
		}
	}
	return models, nil
}
