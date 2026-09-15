// Package extension hosts the gateway's request extensions: optional
// middleware applied to a chat request before routing. Each extension is
// identified by a stable id, persisted through the settings store and toggled
// from the dashboard's Extensions page.
package extension

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"

	"github.com/zealish/zealish-router/internal/extension/rtk"
	"github.com/zealish/zealish-router/internal/extension/sanitize"
	"github.com/zealish/zealish-router/internal/storage"
	"github.com/zealish/zealish-router/pkg/openai"
)

// Extension ids. They are wire-visible (admin API, settings keys), so they
// never change once shipped.
const (
	IDRTK      = "rtk"
	IDSanitize = "sanitize"
)

// settingKey namespaces extension state inside the settings table.
func settingKey(id, field string) string { return "extension." + id + "." + field }

// Info describes one extension for the dashboard.
type Info struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Enabled     bool            `json:"enabled"`
	Config      json.RawMessage `json:"config"`
}

// RTKConfig has no knobs yet; the kernel's invariants are not configuration.
type RTKConfig struct{}

// SanitizeConfig tunes the Request Sanitization extension.
type SanitizeConfig struct {
	// TrimWhitespace collapses trailing spaces and blank-line runs.
	TrimWhitespace bool `json:"trim_whitespace"`
	// HistoryWindow keeps only the last N non-system messages. 0 disables.
	HistoryWindow int `json:"history_window"`
	// DedupMessages drops consecutive byte-identical messages.
	DedupMessages bool `json:"dedup_messages"`
}

// state is the registry's in-memory snapshot, swapped atomically on save.
type state struct {
	rtkEnabled      bool
	sanitizeEnabled bool
	sanitizeCfg     SanitizeConfig
}

// Registry owns extension state: persisted in storage, cached in memory so
// the hot request path never touches the database.
type Registry struct {
	settings storage.SettingStore

	mu sync.RWMutex
	st state
}

// NewRegistry builds a registry reading and writing through settings.
func NewRegistry(settings storage.SettingStore) *Registry {
	return &Registry{settings: settings, st: state{sanitizeCfg: SanitizeConfig{TrimWhitespace: true}}}
}

// Load hydrates the in-memory snapshot from storage. Missing keys keep their
// defaults: everything disabled.
func (r *Registry) Load(ctx context.Context) error {
	all, err := r.settings.All(ctx)
	if err != nil {
		return err
	}
	st := state{sanitizeCfg: SanitizeConfig{TrimWhitespace: true}}
	st.rtkEnabled = all[settingKey(IDRTK, "enabled")] == "true"
	st.sanitizeEnabled = all[settingKey(IDSanitize, "enabled")] == "true"
	if raw, ok := all[settingKey(IDSanitize, "config")]; ok {
		var cfg SanitizeConfig
		if err := json.Unmarshal([]byte(raw), &cfg); err == nil {
			st.sanitizeCfg = cfg
		}
	}

	r.mu.Lock()
	r.st = st
	r.mu.Unlock()
	return nil
}

// List reports every extension in presentation order.
func (r *Registry) List() []Info {
	r.mu.RLock()
	st := r.st
	r.mu.RUnlock()

	rtkCfg, _ := json.Marshal(RTKConfig{})
	sanCfg, _ := json.Marshal(st.sanitizeCfg)
	return []Info{
		{
			ID:          IDRTK,
			Name:        "RTK — Reduce Token Kernel",
			Description: "Semantic token reduction before routing: terminal logs, git diffs, tree/ls listings, stack traces and repetitive PASS/OK output are replaced with structured summaries. System prompts, user intent, tool schemas, JSON payloads and function calls are never modified.",
			Enabled:     st.rtkEnabled,
			Config:      rtkCfg,
		},
		{
			ID:          IDSanitize,
			Name:        "Request Sanitization",
			Description: "Mechanical cleanup: whitespace trimming, history windowing and dropping consecutive duplicate messages. System prompts are never touched.",
			Enabled:     st.sanitizeEnabled,
			Config:      sanCfg,
		},
	}
}

// Update persists an extension's enabled flag and configuration, then swaps
// the in-memory snapshot. Unknown ids are rejected.
func (r *Registry) Update(ctx context.Context, id string, enabled bool, config json.RawMessage) error {
	switch id {
	case IDRTK:
		// No config to validate yet.
	case IDSanitize:
		var cfg SanitizeConfig
		if len(config) > 0 {
			if err := json.Unmarshal(config, &cfg); err != nil {
				return fmt.Errorf("extension: invalid sanitize config: %w", err)
			}
			if cfg.HistoryWindow < 0 {
				return fmt.Errorf("extension: history_window must be >= 0")
			}
		}
		raw, _ := json.Marshal(cfg)
		if err := r.settings.Put(ctx, settingKey(IDSanitize, "config"), string(raw)); err != nil {
			return err
		}
	default:
		return fmt.Errorf("extension: unknown id %q: %w", id, storage.ErrNotFound)
	}

	if err := r.settings.Put(ctx, settingKey(id, "enabled"), strconv.FormatBool(enabled)); err != nil {
		return err
	}
	return r.Load(ctx)
}

// Apply runs every enabled extension over the request messages, in a fixed
// order: sanitization first (cheap, mechanical), then RTK (semantic). The
// request is mutated in place; disabled extensions cost one atomic read.
func (r *Registry) Apply(req *openai.ChatCompletionRequest) {
	r.mu.RLock()
	st := r.st
	r.mu.RUnlock()

	if !st.rtkEnabled && !st.sanitizeEnabled {
		return
	}

	msgs := req.Messages
	if st.sanitizeEnabled {
		if st.sanitizeCfg.DedupMessages {
			msgs, _ = sanitize.DedupMessages(msgs)
		}
		if st.sanitizeCfg.HistoryWindow > 0 {
			msgs, _ = sanitize.WindowHistory(msgs, st.sanitizeCfg.HistoryWindow)
		}
		if st.sanitizeCfg.TrimWhitespace {
			msgs, _ = sanitize.TrimWhitespace(msgs)
		}
	}
	if st.rtkEnabled {
		msgs = rtk.Compress(msgs).Messages
	}
	req.Messages = msgs
}
