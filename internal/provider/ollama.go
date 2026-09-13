package provider

// Ollama talks to a local Ollama server through its OpenAI-compatible API.
// It requires no API key.
type Ollama struct {
	httpProvider
}

// NewOllama constructs an Ollama provider.
func NewOllama(opts Options) *Ollama {
	if opts.Name == "" {
		opts.Name = "ollama"
	}
	opts.APIKey = ""
	return &Ollama{httpProvider: newHTTPProvider(opts, nil)}
}
