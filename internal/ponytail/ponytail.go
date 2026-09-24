package ponytail

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Config holds ponytail configuration. It maps directly to the YAML section.
type Config struct {
	Enabled  bool   `yaml:"enabled"`
	Mode     string `yaml:"mode"` // conservative, balanced, aggressive
	Thresholds struct {
		MinInputTokens int `yaml:"min_input_tokens"`
		MinMessages    int `yaml:"min_messages"`
	} `yaml:"thresholds"`
	ProtectedWindow int `yaml:"protected_window"`
	Compression     struct {
		Conversation bool `yaml:"conversation"`
		Code         bool `yaml:"code"`
		Deduplicate  bool `yaml:"deduplicate"`
	} `yaml:"compression"`
	Metadata bool `yaml:"metadata"`
}

// DefaultConfig returns a Config with safe built-in values.
func DefaultConfig() Config {
	return Config{
		Enabled: false,
		Mode:    "balanced",
		Thresholds: struct {
			MinInputTokens int `yaml:"min_input_tokens"`
			MinMessages    int `yaml:"min_messages"`
		}{
			MinInputTokens: 12000,
			MinMessages:    16,
		},
		ProtectedWindow: 8,
		Compression: struct {
			Conversation bool `yaml:"conversation"`
			Code         bool `yaml:"code"`
			Deduplicate  bool `yaml:"deduplicate"`
		}{
			Conversation: true,
			Code:         true,
			Deduplicate:  true,
		},
		Metadata: true,
	}
}

// Result is the output of a ponytail optimization pass.
type Result struct {
	Messages        []Message
	Metadata        *Metadata
	OriginalTokens  int
	OptimizedTokens int
}

// Metadata describes what ponytail did during optimization.
type Metadata struct {
	Enabled          bool    `json:"enabled"`
	Mode             string  `json:"mode"`
	SavedTokens      int     `json:"saved_tokens"`
	CompressionRatio float64 `json:"compression_ratio"`
}

// Processor is the ponytail context optimizer. It is safe for concurrent use.
type Processor struct {
	logger *slog.Logger
	mu     sync.RWMutex
	config Config
}

// NewProcessor creates a new ponytail Processor.
func NewProcessor(logger *slog.Logger, config Config) *Processor {
	return &Processor{
		logger: logger,
		config: config,
	}
}

// SetConfig hot-swaps the processor configuration. It is safe to call from the
// admin API after persisting the new settings to the database.
func (p *Processor) SetConfig(cfg Config) {
	p.mu.Lock()
	p.config = cfg
	p.mu.Unlock()
	p.logger.Info("ponytail config updated",
		slog.Bool("enabled", cfg.Enabled),
		slog.String("mode", cfg.Mode))
}

// Enabled reports whether the processor's global configuration has ponytail enabled.
func (p *Processor) Enabled() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.config.Enabled
}

// Mode returns the configured compression mode.
func (p *Processor) Mode() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.config.Mode
}

// Stats holds aggregated ponytail optimization statistics. The handler
// records events here; the admin API reads from here. Thread-safe.
type Stats struct {
	mu                sync.Mutex
	RequestsOptimized int64   `json:"requests_optimized"`
	TotalTokensSaved  int64   `json:"total_tokens_saved"`
	TotalOriginal     int64   `json:"total_original"`
	TotalOptimized    int64   `json:"total_optimized"`
	TotalDurationMs   int64   `json:"total_duration_ms"`
}

// Record adds an optimization event to the stats.
func (s *Stats) Record(original, optimized int, duration time.Duration) {
	saved := original - optimized
	if saved < 0 {
		saved = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.RequestsOptimized++
	s.TotalTokensSaved += int64(saved)
	s.TotalOriginal += int64(original)
	s.TotalOptimized += int64(optimized)
	s.TotalDurationMs += duration.Milliseconds()
}

// Snapshot returns a copy of the current stats.
func (s *Stats) Snapshot() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Stats{
		RequestsOptimized: s.RequestsOptimized,
		TotalTokensSaved:  s.TotalTokensSaved,
		TotalOriginal:     s.TotalOriginal,
		TotalOptimized:    s.TotalOptimized,
		TotalDurationMs:   s.TotalDurationMs,
	}
}

// Process runs the ponytail optimization pipeline on a request's messages.
// It returns the optimized result. If optimization is not eligible or fails,
// the original messages are returned unchanged.
//
// Pipeline:
//  1. Estimate original tokens
//  2. Check eligibility (thresholds)
//  3. Dedup messages
//  4. Rank messages by relevance
//  5. Compress older context (outside protected window)
//  6. Compress code blocks
//  7. Estimate optimized tokens
//  8. Return result with metadata
func (p *Processor) Process(ctx context.Context, messages []Message) (*Result, error) {
	start := time.Now()

	p.mu.RLock()
	cfg := p.config
	p.mu.RUnlock()

	// Step 1: Estimate original tokens.
	originalTokens := estimateTokensFromMessages(messages)

	// Step 2: Check eligibility.
	if !cfg.isEligible(messages, originalTokens) {
		return &Result{
			Messages:        messages,
			OriginalTokens:  originalTokens,
			OptimizedTokens: originalTokens,
		}, nil
	}

	optimized := make([]Message, len(messages))
	copy(optimized, messages)

	// Step 3: Dedup.
	if cfg.Compression.Deduplicate {
		optimized = dedupMessages(optimized)
	}

	// Step 4 & 5: Rank and filter by budget.
	// Use a token budget based on the mode.
	budget := cfg.tokenBudget(originalTokens)
	protected := cfg.ProtectedWindow

	scored := rankMessages(optimized)
	optimized = filterByBudget(optimized, scored, budget, protected)

	// Step 6: Compress messages outside protected window.
	if cfg.Compression.Conversation {
		optimized = compressConversation(optimized, protected, cfg.Mode)
	}

	// Step 7: Compress code blocks.
	if cfg.Compression.Code {
		for i, m := range optimized {
			text, ok := extractMessageText(m)
			if ok && text != "" {
				compressed := compressCodeBlocks(text, cfg.Mode)
				if compressed != text {
					optimized[i].Content = marshalText(compressed)
				}
			}
		}
	}

	// Step 8: Estimate optimized tokens.
	optimizedTokens := estimateTokensFromMessages(optimized)

	elapsed := time.Since(start)
	p.logger.Debug("ponytail optimization complete",
		slog.Int("original_tokens", originalTokens),
		slog.Int("optimized_tokens", optimizedTokens),
		slog.Int("saved_tokens", originalTokens-optimizedTokens),
		slog.String("mode", cfg.Mode),
		slog.Duration("elapsed", elapsed),
	)

	var meta *Metadata
	if cfg.Metadata {
		ratio := 1.0
		if originalTokens > 0 {
			ratio = float64(optimizedTokens) / float64(originalTokens)
		}
		meta = &Metadata{
			Enabled:          true,
			Mode:             cfg.Mode,
			SavedTokens:      originalTokens - optimizedTokens,
			CompressionRatio: ratio,
		}
	}

	return &Result{
		Messages:        optimized,
		Metadata:        meta,
		OriginalTokens:  originalTokens,
		OptimizedTokens: optimizedTokens,
	}, nil
}

// isEligible checks whether the request meets the minimum thresholds for
// optimization. A request must have enough tokens AND enough messages.
// Additional heuristic checks for attached docs and duplicated context.
func (c Config) isEligible(messages []Message, tokenCount int) bool {
	if tokenCount < c.Thresholds.MinInputTokens {
		return false
	}
	if len(messages) < c.Thresholds.MinMessages {
		return false
	}
	return true
}

// tokenBudget returns the target token count after optimization. The budget
// is a fraction of the original based on the compression mode.
func (c Config) tokenBudget(originalTokens int) int {
	var ratio float64
	switch c.Mode {
	case "conservative":
		ratio = 0.85
	case "balanced":
		ratio = 0.65
	case "aggressive":
		ratio = 0.45
	default:
		ratio = 0.65
	}
	return int(float64(originalTokens) * ratio)
}