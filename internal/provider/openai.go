package provider

import "net/http"

// Options configures an HTTP-backed provider implementation.
type Options struct {
	Name       string
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
	// Referer and Title are OpenRouter attribution headers; ignored elsewhere.
	Referer string
	Title   string
}

// OpenAI talks to the official OpenAI API.
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
