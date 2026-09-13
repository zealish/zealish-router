package provider

import "strings"

// Group classifies how a provider is configured and how it authenticates.
// It is orthogonal to Kind: a group says where the credential comes from, a
// kind says which wire dialect the upstream speaks.
type Group string

const (
	// GroupCustom is a self-hosted or arbitrary upstream: the operator supplies
	// the base URL and picks the wire dialect.
	GroupCustom Group = "custom"
	// GroupOAuth is an upstream authenticated with a token obtained out of band
	// through an OAuth flow. The token is sent as a bearer credential.
	GroupOAuth Group = "oauth"
	// GroupAPIKey is a known upstream picked from the catalogue below and
	// authenticated with a plain API key.
	GroupAPIKey Group = "api_key"
)

// Wire dialects understood by the router. Every upstream speaks one of the two
// formats; anything else is reached through a compatible endpoint.
const (
	KindOpenAI    = "openai"
	KindAnthropic = "anthropic"
)

// Kinds lists every wire dialect in presentation order.
func Kinds() []string { return []string{KindOpenAI, KindAnthropic} }

// Groups lists every provider group in presentation order.
func Groups() []Group { return []Group{GroupCustom, GroupOAuth, GroupAPIKey} }

// ValidGroup reports whether g names a known group.
func ValidGroup(g string) bool {
	switch Group(g) {
	case GroupCustom, GroupOAuth, GroupAPIKey:
		return true
	default:
		return false
	}
}

// KindsFor lists the wire dialects selectable within a group. Both formats are
// available everywhere; the group only decides how the credential is supplied.
func KindsFor(Group) []string { return Kinds() }

// CatalogEntry is a preset the dashboard offers when adding a provider, so a
// known upstream needs a credential and nothing else.
type CatalogEntry struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Group       Group  `json:"group"`
	Kind        string `json:"kind"`
	BaseURL     string `json:"base_url"`
	AliasPrefix string `json:"alias_prefix"`
	Docs        string `json:"docs,omitempty"`
}

// catalog holds the presets. Custom providers are deliberately absent: they
// exist precisely because the operator supplies their own endpoint.
var catalog = []CatalogEntry{
	{
		ID:          "commandcode",
		Label:       "Command Code",
		Group:       GroupAPIKey,
		Kind:        KindAnthropic,
		BaseURL:     "https://api.commandcode.ai/provider/v1",
		AliasPrefix: "cc/",
		Docs:        "https://commandcode.ai/docs/provider",
	},
	{
		ID:          "openai",
		Label:       "OpenAI",
		Group:       GroupAPIKey,
		Kind:        KindOpenAI,
		BaseURL:     "https://api.openai.com/v1",
		AliasPrefix: "openai/",
	},
	{
		ID:          "anthropic",
		Label:       "Anthropic",
		Group:       GroupAPIKey,
		Kind:        KindAnthropic,
		BaseURL:     "https://api.anthropic.com/v1",
		AliasPrefix: "anthropic/",
	},
	{
		ID:          "openrouter",
		Label:       "OpenRouter",
		Group:       GroupAPIKey,
		Kind:        KindOpenAI,
		BaseURL:     "https://openrouter.ai/api/v1",
		AliasPrefix: "or/",
	},
	{
		ID:          "anthropic-oauth",
		Label:       "Anthropic (OAuth token)",
		Group:       GroupOAuth,
		Kind:        KindAnthropic,
		BaseURL:     "https://api.anthropic.com/v1",
		AliasPrefix: "claude/",
	},
	{
		ID:          "commandcode-oauth",
		Label:       "Command Code (OAuth token)",
		Group:       GroupOAuth,
		Kind:        KindAnthropic,
		BaseURL:     "https://api.commandcode.ai/provider/v1",
		AliasPrefix: "cc/",
		Docs:        "https://commandcode.ai/docs/provider",
	},
}

// Catalog returns the presets, optionally narrowed to one group.
func Catalog(group string) []CatalogEntry {
	out := make([]CatalogEntry, 0, len(catalog))
	for _, e := range catalog {
		if group != "" && string(e.Group) != group {
			continue
		}
		out = append(out, e)
	}
	return out
}

// NormalizeKind maps a stored kind onto a known dialect, tolerating the
// "-compatible" suffix the dashboard displays. Anything unrecognised falls back
// to plain OpenAI, which every compatible upstream speaks.
func NormalizeKind(kind string) string {
	switch strings.TrimSuffix(strings.ToLower(strings.TrimSpace(kind)), "-compatible") {
	case KindAnthropic, "claude":
		return KindAnthropic
	default:
		return KindOpenAI
	}
}
