package provider

// OpenRouter talks to the OpenRouter aggregation API. It is OpenAI-compatible
// and additionally accepts attribution headers.
type OpenRouter struct {
	httpProvider
}

// NewOpenRouter constructs an OpenRouter provider.
func NewOpenRouter(opts Options) *OpenRouter {
	if opts.Name == "" {
		opts.Name = "openrouter"
	}
	if opts.Referer == "" {
		opts.Referer = "https://github.com/zealish/zealish-router"
	}
	if opts.Title == "" {
		opts.Title = "Zealish Router"
	}
	headers := map[string]string{
		"HTTP-Referer": opts.Referer,
		"X-Title":      opts.Title,
	}
	return &OpenRouter{httpProvider: newHTTPProvider(opts, headers)}
}
