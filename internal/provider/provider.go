// Package provider defines the upstream LLM provider abstraction and a
// registry for resolving providers by name. Implementations translate between
// the OpenAI-compatible wire format and a concrete upstream API.
package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/zealish/zealish-router/pkg/openai"
)

// ErrNotFound is returned when a provider name is not registered.
var ErrNotFound = errors.New("provider: not found")

// Provider is the contract every upstream implementation must satisfy.
type Provider interface {
	Name() string
	ChatCompletion(ctx context.Context, req *openai.ChatCompletionRequest) (*openai.ChatCompletionResponse, error)
	ChatCompletionStream(ctx context.Context, req *openai.ChatCompletionRequest) (<-chan openai.StreamChunk, error)
}

// Registry is an immutable lookup table of providers keyed by name.
type Registry struct {
	providers map[string]Provider
}

// NewRegistry builds a registry from the given providers.
func NewRegistry(providers ...Provider) *Registry {
	m := make(map[string]Provider, len(providers))
	for _, p := range providers {
		m[p.Name()] = p
	}
	return &Registry{providers: m}
}

// Get resolves a provider by name.
func (r *Registry) Get(name string) (Provider, error) {
	p, ok := r.providers[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	return p, nil
}

// Names returns every registered provider name.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	return names
}
