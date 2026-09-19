package provider

import "net/http"

// Options configures an HTTP-backed provider implementation.
type Options struct {
	Name    string
	BaseURL string
	APIKey  string
	// APIKeys enables credential selection when APIKeyMethod is round_robin.
	APIKeys      []string
	APIKeyMethod string
	HTTPClient   *http.Client
	// AuthHeader overrides the default "Authorization: Bearer <key>" credential
	// header. Anthropic-compatible upstreams use "x-api-key" for API keys and
	// keep the bearer form for OAuth tokens.
	AuthHeader string
}

// OpenAI talks to any OpenAI-compatible upstream.
type OpenAI struct {
	httpProvider
}

// NewOpenAI constructs an OpenAI provider.
func NewOpenAI(opts Options) *OpenAI {
	if opts.Name == "" {
		opts.Name = "openai"
	}
	return &OpenAI{httpProvider: newHTTPProvider(opts, nil)}
}
