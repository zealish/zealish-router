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
	"sync/atomic"

	"github.com/zealish/zealish-router/internal/extension/rtk"
	"github.com/zealish/zealish-router/internal/extension/sanitize"
	"github.com/zealish/zealish-router/internal/storage"
	"github.com/zealish/zealish-router/pkg/openai"
)

// charsPerToken is the ratio used to express shrinkage in tokens rather than
// bytes. It matches the router's usage estimator: an approximation, reported
// so an operator can see what an extension is worth, never billed on.
const charsPerToken = 4

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
	Stats       Stats           `json:"stats"`
}

// Stats is a lifetime read of one extension's effect on request bodies.
type Stats struct {
	// MessagesRewritten counts messages an extension actually changed;
	// running over an untouched request costs nothing here.
	MessagesRewritten int64 `json:"messages_rewritten"`
	// BytesSaved is the total content shrinkage across every rewrite.
	BytesSaved int64 `json:"bytes_saved"`
	// TokensSaved is BytesSaved expressed at the same approximate ratio the
	// router uses to estimate usage: informative, never billed on.
	TokensSaved int64 `json:"tokens_saved"`
}

func statsOf(messages, bytes int64) Stats {
	if bytes <= 0 {
		return Stats{}
	}
	return Stats{
		MessagesRewritten: messages,
		BytesSaved:        bytes,
		TokensSaved:       (bytes + charsPerToken - 1) / charsPerToken,
	}
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
// the hot request path never touches the database. Effect counters are
// atomic rather than behind mu: they are updated on every Apply call, far
// hotter than the config swap.
type Registry struct {
	settings storage.SettingStore

	mu sync.RWMutex
	st state

	rtkMessages, rtkBytes int64
	sanMessages, sanBytes int64
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
			Description: "Bounded semantic compression for verbose tool output before routing: terminal logs, git diffs, tree listings, stack traces and repetitive test output are reduced while tool errors, images and structured payloads remain intact. User and system prose, tool schemas and function calls are never modified.",
			Enabled:     st.rtkEnabled,
			Config:      rtkCfg,
			Stats:       statsOf(atomic.LoadInt64(&r.rtkMessages), atomic.LoadInt64(&r.rtkBytes)),
		},
		{
			ID:          IDSanitize,
			Name:        "Request Sanitization",
			Description: "Mechanical cleanup: whitespace trimming, history windowing and dropping consecutive duplicate messages. System prompts are never touched.",
			Enabled:     st.sanitizeEnabled,
			Config:      sanCfg,
			Stats:       statsOf(atomic.LoadInt64(&r.sanMessages), atomic.LoadInt64(&r.sanBytes)),
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
// Effect counters are updated so the dashboard can show what each extension
// is worth, not just whether it is on.
func (r *Registry) Apply(req *openai.ChatCompletionRequest) {
	r.mu.RLock()
	st := r.st
	r.mu.RUnlock()

	if !st.rtkEnabled && !st.sanitizeEnabled {
		return
	}

	msgs := req.Messages
	if st.sanitizeEnabled {
		before := messageBytes(msgs)
		var changed int64
		if st.sanitizeCfg.DedupMessages {
			next, removed := sanitize.DedupMessages(msgs)
			changed += int64(removed)
			msgs = next
		}
		if st.sanitizeCfg.HistoryWindow > 0 {
			next, dropped := sanitize.WindowHistory(msgs, st.sanitizeCfg.HistoryWindow)
			changed += int64(dropped)
			msgs = next
		}
		if st.sanitizeCfg.TrimWhitespace {
			next, trimmedBytes := sanitize.TrimWhitespace(msgs)
			if trimmedBytes > 0 {
				changed += messagesDiffer(msgs, next)
			}
			msgs = next
		}
		if delta := before - messageBytes(msgs); delta > 0 {
			atomic.AddInt64(&r.sanBytes, delta)
		}
		if changed > 0 {
			atomic.AddInt64(&r.sanMessages, changed)
		}
	}
	if st.rtkEnabled {
		result := rtk.Compress(msgs)
		msgs = result.Messages
		if result.SavedBytes > 0 {
			atomic.AddInt64(&r.rtkBytes, int64(result.SavedBytes))
		}
		if result.CompressedMessages > 0 {
			atomic.AddInt64(&r.rtkMessages, int64(result.CompressedMessages))
		}
	}
	req.Messages = msgs
}

// messageBytes sums the raw content length across a message list. It is a
// rough proxy for token count, consistent enough to diff before and after a
// rewrite.
func messageBytes(messages []openai.Message) int64 {
	var total int64
	for _, m := range messages {
		total += int64(len(m.Content))
	}
	return total
}

// messagesDiffer counts how many messages changed content between two
// same-length lists, as produced by TrimWhitespace which rewrites in place
// without adding or removing messages.
func messagesDiffer(before, after []openai.Message) int64 {
	var n int64
	for i := range before {
		if i < len(after) && string(before[i].Content) != string(after[i].Content) {
			n++
		}
	}
	return n
}
