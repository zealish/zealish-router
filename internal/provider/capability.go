package provider

import "strings"

// Capability names a feature a model alias can serve. Capabilities are
// advertised, not enforced: they tell clients and the dashboard what a route
// is expected to accept, which is what a catalogue is for.
const (
	// CapChat is ordinary conversational completion.
	CapChat = "chat"
	// CapVision is image input.
	CapVision = "vision"
	// CapTools is function / tool calling.
	CapTools = "tools"
	// CapEmbeddings is /v1/embeddings.
	CapEmbeddings = "embeddings"
	// CapReasoning is long-reasoning ("thinking") output.
	CapReasoning = "reasoning"
	// CapStreaming is SSE streaming.
	CapStreaming = "streaming"
	// CapAudio is audio input or output.
	CapAudio = "audio"
	// CapJSONMode is structured / JSON-schema output.
	CapJSONMode = "json_mode"
)

// capabilityOrder is the canonical presentation order. It also decides the
// order capabilities are stored in, so a round-trip never reshuffles them.
var capabilityOrder = []string{
	CapChat, CapVision, CapTools, CapEmbeddings,
	CapReasoning, CapStreaming, CapAudio, CapJSONMode,
}

// Capabilities lists every known capability in presentation order.
func Capabilities() []string {
	out := make([]string, len(capabilityOrder))
	copy(out, capabilityOrder)
	return out
}

// ValidCapability reports whether c names a known capability.
func ValidCapability(c string) bool {
	for _, known := range capabilityOrder {
		if c == known {
			return true
		}
	}
	return false
}

// NormalizeCapabilities drops unknown and duplicate entries and returns the
// rest in canonical order, so stored capabilities are comparable as a list.
func NormalizeCapabilities(caps []string) []string {
	seen := make(map[string]bool, len(caps))
	for _, c := range caps {
		c = strings.ToLower(strings.TrimSpace(c))
		if ValidCapability(c) {
			seen[c] = true
		}
	}

	out := make([]string, 0, len(seen))
	for _, c := range capabilityOrder {
		if seen[c] {
			out = append(out, c)
		}
	}
	return out
}

// InferCapabilities guesses what an upstream model supports from its name.
// It exists so importing a catalogue of hundreds of models does not require
// hand-tagging every one; the guess is a starting point an operator edits, not
// a contract. Unrecognised names get the conservative chat baseline.
func InferCapabilities(model string) []string {
	name := strings.ToLower(strings.TrimSpace(model))
	if name == "" {
		return nil
	}

	// Embedding and audio models are not chat models at all, so they are
	// classified first and never collect the chat baseline.
	if isEmbedding(name) {
		return []string{CapEmbeddings}
	}
	if isAudioOnly(name) {
		return []string{CapAudio}
	}

	caps := []string{CapChat, CapStreaming}
	if hasVision(name) {
		caps = append(caps, CapVision)
	}
	if hasTools(name) {
		caps = append(caps, CapTools)
	}
	if hasReasoning(name) {
		caps = append(caps, CapReasoning)
	}
	if hasAudio(name) {
		caps = append(caps, CapAudio)
	}
	if hasJSONMode(name) {
		caps = append(caps, CapJSONMode)
	}
	return NormalizeCapabilities(caps)
}

// containsAny reports whether name holds any of the markers.
func containsAny(name string, markers ...string) bool {
	for _, m := range markers {
		if strings.Contains(name, m) {
			return true
		}
	}
	return false
}

func isEmbedding(name string) bool {
	return containsAny(name, "embed", "bge-", "gte-", "e5-", "nomic-")
}

// isAudioOnly covers dedicated speech models — transcription and synthesis —
// which serve neither chat nor completions.
func isAudioOnly(name string) bool {
	if containsAny(name, "whisper", "transcribe", "tts") {
		return true
	}
	return containsAny(name, "realtime") && !containsAny(name, "gpt-4o", "gpt-5")
}

func hasVision(name string) bool {
	if containsAny(name, "vision", "-vl", "llava", "pixtral", "multimodal", "omni") {
		return true
	}
	// Model families whose whole current generation takes images.
	return containsAny(name,
		"gpt-4o", "gpt-4.1", "gpt-4-turbo", "gpt-5", "o3", "o4-mini",
		"claude-3", "claude-4", "claude-sonnet", "claude-opus", "claude-haiku",
		"gemini", "grok-2-vision", "grok-4", "llama-3.2", "llama-4", "qwen2.5-vl", "qwen3-vl",
	)
}

// hasTools covers the families that expose function calling. Small local
// instruct models are deliberately excluded unless they advertise it.
func hasTools(name string) bool {
	return containsAny(name,
		"gpt-3.5-turbo", "gpt-4", "gpt-5", "o1", "o3", "o4",
		"claude-", "gemini", "grok", "mistral", "mixtral", "command-r",
		"llama-3", "llama-4", "qwen2.5", "qwen3", "deepseek", "glm-4", "kimi",
	)
}

func hasReasoning(name string) bool {
	if containsAny(name, "think", "reason", "-r1", "deepseek-r1", "qwq") {
		return true
	}
	// OpenAI's o-series and GPT-5 reason by default; so do the Claude 4 and
	// Gemini "thinking" tiers.
	return containsAny(name,
		"o1-", "o3-", "o4-", "gpt-5",
		"claude-opus-4", "claude-sonnet-4", "claude-3-7",
		"gemini-2.5", "gemini-3",
	) || name == "o1" || name == "o3"
}

func hasAudio(name string) bool {
	return containsAny(name, "audio", "voice", "speech", "omni")
}

// hasJSONMode covers the families that accept response_format. It is broader
// than tool calling: several models take a JSON schema without tools.
func hasJSONMode(name string) bool {
	return containsAny(name,
		"gpt-3.5-turbo", "gpt-4", "gpt-5", "o1", "o3", "o4",
		"claude-", "gemini", "grok", "mistral", "mixtral", "command-r",
		"llama-3", "llama-4", "qwen2.5", "qwen3", "deepseek", "glm-4", "kimi",
	)
}
