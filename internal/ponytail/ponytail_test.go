package ponytail

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// msg is a shorthand to build a test message with string content.
func msg(role, content string) Message {
	return Message{Role: role, Content: json.RawMessage(`"` + content + `"`)}
}

// msgName is a shorthand to build a test message with a Name field.
func msgName(role, name, content string) Message {
	return Message{Role: role, Name: name, Content: json.RawMessage(`"` + content + `"`)}
}

// rawMsg builds a message with raw JSON content (e.g. array of parts).
func rawMsg(role string, content json.RawMessage) Message {
	return Message{Role: role, Content: content}
}

// enabledConfig returns a Config with ponytail enabled at low thresholds for testing.
func enabledConfig(mode string) Config {
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.Mode = mode
	cfg.Thresholds.MinInputTokens = 10
	cfg.Thresholds.MinMessages = 1
	cfg.ProtectedWindow = 2
	cfg.Metadata = true
	return cfg
}

// ---------------------------------------------------------------------------
// 1. Token estimation
// ---------------------------------------------------------------------------

func TestEstimateTokensFromText(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		wantAt int // minimum expected tokens
	}{
		{"empty", "", 0},
		{"single char", "a", 1},
		{"four chars", "abcd", 1},
		{"five chars", "abcde", 2},
		{"ten chars", "abcdefghij", 3},
		{"unicode 2-rune", "你好", 1},         // 2 runes, (2+3)/4 = 1
		{"unicode 4-rune", "你好世界", 1},     // 4 runes, (4+3)/4 = 1
		{"unicode 5-rune", "你好世界!", 2},    // 5 runes, (5+3)/4 = 2
		{"long string", "this is a longer test string for token estimation purposes", 15},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := estimateTokensFromText(tt.input)
			if got < tt.wantAt {
				t.Errorf("estimateTokensFromText(%q) = %d, want at least %d", tt.input, got, tt.wantAt)
			}
			if tt.input == "" && got != 0 {
				t.Errorf("estimateTokensFromText(\"\") = %d, want 0", got)
			}
			if tt.input != "" && got < 1 {
				t.Errorf("estimateTokensFromText(%q) = %d, want at least 1", tt.input, got)
			}
		})
	}
}

func TestEstimateTokensFromMessages(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		got := estimateTokensFromMessages(nil)
		if got != 0 {
			t.Errorf("nil messages = %d tokens, want 0", got)
		}
	})

	t.Run("single message", func(t *testing.T) {
		msgs := []Message{msg("user", "hello")}
		got := estimateTokensFromMessages(msgs)
		// role("user"=4 chars) → 1 token + 4 overhead + content("hello"=5 chars) → 2 tokens = 7
		if got < 5 {
			t.Errorf("single message = %d tokens, want at least 5", got)
		}
	})

	t.Run("multiple messages accumulate", func(t *testing.T) {
		msgs := []Message{
			msg("system", "you are a helpful assistant"),
			msg("user", "hello"),
			msg("assistant", "hi there"),
		}
		got := estimateTokensFromMessages(msgs)
		single := estimateTokensFromMessages([]Message{msg("user", "hello")})
		if got <= single {
			t.Errorf("3 messages (%d) should have more tokens than 1 message (%d)", got, single)
		}
	})

	t.Run("name field adds tokens", func(t *testing.T) {
		plain := []Message{msg("user", "hello")}
		withName := []Message{msgName("user", "alice", "hello")}
		if estimateTokensFromMessages(withName) <= estimateTokensFromMessages(plain) {
			t.Error("message with name should have more tokens than without")
		}
	})
}

func TestExtractText(t *testing.T) {
	tests := []struct {
		name  string
		input json.RawMessage
		want  string
	}{
		{"nil", nil, ""},
		{"empty array", json.RawMessage(`[]`), ""},
		{"plain string", json.RawMessage(`"hello world"`), "hello world"},
		{"array of parts", json.RawMessage(`[{"type":"text","text":"hello "},{"type":"text","text":"world"}]`), "hello world"},
		{"invalid json", json.RawMessage(`{broken`), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractText(tt.input)
			if got != tt.want {
				t.Errorf("extractText(%s) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 2. Deduplication
// ---------------------------------------------------------------------------

func TestDedupExactDuplicates(t *testing.T) {
	msgs := []Message{
		msg("system", "be helpful"),
		msg("user", "hi"),
		msg("assistant", "hello"),
		msg("user", "hi"),
		msg("assistant", "hello"), // exact dup of index 2
	}
	got := dedupMessages(msgs)

	// User messages are never removed — both "hi" messages stay.
	userCount := 0
	for _, m := range got {
		if m.Role == "user" {
			userCount++
		}
	}
	if userCount != 2 {
		t.Errorf("user messages = %d, want 2 (never removed)", userCount)
	}

	// The second "hello" assistant is a dup; only one should remain (the last).
	assistantCount := 0
	for _, m := range got {
		if m.Role == "assistant" && extractTextStr(m.Content) == "hello" {
			assistantCount++
		}
	}
	if assistantCount != 1 {
		t.Errorf("assistant 'hello' count = %d, want 1 (collapsed to newest)", assistantCount)
	}
}

func TestDedupConsecutiveSystemPrompts(t *testing.T) {
	msgs := []Message{
		msg("system", "be helpful"),
		msg("system", "be helpful"),
		msg("system", "be helpful"),
		msg("system", "be concise"),
		msg("user", "hi"),
	}
	got := dedupMessages(msgs)

	sysCount := 0
	for _, m := range got {
		if m.Role == "system" {
			sysCount++
		}
	}
	// 3 identical consecutive → keep first; different one kept = 2 total.
	if sysCount != 2 {
		t.Errorf("system messages after dedup = %d, want 2", sysCount)
	}
}

func TestDedupUserMessagesNeverRemoved(t *testing.T) {
	msgs := []Message{
		msg("user", "same"),
		msg("user", "same"),
		msg("user", "same"),
		msg("user", "different"),
	}
	got := dedupMessages(msgs)
	if len(got) != 4 {
		t.Errorf("user-only conversation: got %d messages, want 4 (all preserved)", len(got))
	}
}

func TestDedupAssistantRepeatsCollapsesNewest(t *testing.T) {
	msgs := []Message{
		msg("assistant", "answer A"),
		msg("user", "q1"),
		msg("assistant", "answer A"), // repeat of index 0
		msg("user", "q2"),
		msg("assistant", "answer A"), // repeat again
	}
	got := dedupMessages(msgs)

	aCount := 0
	for _, m := range got {
		if m.Role == "assistant" && extractTextStr(m.Content) == "answer A" {
			aCount++
		}
	}
	if aCount != 1 {
		t.Errorf("assistant 'answer A' count = %d, want 1 (collapsed to single)", aCount)
	}
	// Pass 2 keeps first occurrence; the remaining "answer A" should be at index 0.
	if len(got) > 0 && extractTextStr(got[0].Content) != "answer A" {
		t.Errorf("kept assistant should be first occurrence at index 0, got %q", extractTextStr(got[0].Content))
	}
}

func TestDedupPreservesOrdering(t *testing.T) {
	msgs := []Message{
		msg("system", "sys"),
		msg("user", "u1"),
		msg("assistant", "a1"),
		msg("user", "u2"),
		msg("assistant", "a2"),
	}
	got := dedupMessages(msgs)
	if len(got) != 5 {
		t.Fatalf("length = %d, want 5", len(got))
	}
	for i, m := range got {
		if m.Role != msgs[i].Role {
			t.Errorf("index %d: role = %q, want %q", i, m.Role, msgs[i].Role)
		}
	}
}

func TestDedupSingleMessage(t *testing.T) {
	msgs := []Message{msg("user", "only")}
	got := dedupMessages(msgs)
	if len(got) != 1 {
		t.Errorf("single message dedup = %d messages, want 1", len(got))
	}
}

// extractTextStr is a test helper that extracts text without the bool return.
func extractTextStr(raw json.RawMessage) string {
	s, _ := extractMessageText(Message{Content: raw})
	return s
}

// ---------------------------------------------------------------------------
// 3. Ranking
// ---------------------------------------------------------------------------

func TestRankMessagesRoleScores(t *testing.T) {
	msgs := []Message{
		msg("system", "you are helpful"),
		msg("developer", "use typescript"),
		msg("user", "first question"),
		msg("assistant", "first answer"),
		msg("user", "second question"),
		msg("assistant", "second answer"),
		msg("tool", "result data"),
	}
	scored := rankMessages(msgs)

	wantScores := map[string]float64{
		"system":    100,
		"developer": 90,
		"tool":      70,
	}

	for role, want := range wantScores {
		found := false
		for _, s := range scored {
			if s.message.Role == role {
				if s.score != want {
					t.Errorf("role %s: score = %f, want %f", role, s.score, want)
				}
				found = true
				break
			}
		}
		if !found {
			t.Errorf("role %s not found in scored messages", role)
		}
	}

	// Latest user message (index 5 → "second question") should be 80.
	if scored[4].score != 80 {
		t.Errorf("latest user message score = %f, want 80", scored[4].score)
	}

	// First user message (index 2 → "first question") should use age score.
	if scored[2].score >= 80 {
		t.Errorf("non-latest user message score = %f, should be < 80", scored[2].score)
	}

	// Last 2 assistant messages should be 60.
	for _, s := range scored {
		if s.message.Role == "assistant" && s.score != 60 {
			t.Errorf("recent assistant at index %d: score = %f, want 60", s.index, s.score)
		}
	}
}

func TestRankMessagesAgeScore(t *testing.T) {
	// Single message: ageScore should be 40.
	if got := ageScore(0, 1); got != 40 {
		t.Errorf("ageScore(0,1) = %f, want 40", got)
	}

	// Two messages: first=10, second=40.
	if got := ageScore(0, 2); got != 10 {
		t.Errorf("ageScore(0,2) = %f, want 10", got)
	}
	if got := ageScore(1, 2); got != 40 {
		t.Errorf("ageScore(1,2) = %f, want 40", got)
	}

	// Five messages: first=10, middle=25, last=40.
	if got := ageScore(0, 5); got != 10 {
		t.Errorf("ageScore(0,5) = %f, want 10", got)
	}
	if got := ageScore(4, 5); got != 40 {
		t.Errorf("ageScore(4,5) = %f, want 40", got)
	}
}

func TestIsRecentAssistant(t *testing.T) {
	msgs := []Message{
		msg("user", "q1"),
		msg("assistant", "a1"),
		msg("user", "q2"),
		msg("assistant", "a2"),
		msg("user", "q3"),
		msg("assistant", "a3"),
	}
	// Last 2 assistants are indices 3 and 5.
	if !isRecentAssistant(msgs, 5, 2) {
		t.Error("last assistant should be recent")
	}
	if !isRecentAssistant(msgs, 3, 2) {
		t.Error("second-to-last assistant should be recent")
	}
	if isRecentAssistant(msgs, 1, 2) {
		t.Error("third-to-last assistant should NOT be recent")
	}
}

func TestFilterByBudget(t *testing.T) {
	msgs := []Message{
		msg("system", "sys"),
		msg("user", "u1"),
		msg("assistant", "a1"),
		msg("user", "u2"),
		msg("assistant", "a2"),
		msg("user", "u3"),
		msg("assistant", "a3"),
	}
	scored := rankMessages(msgs)

	// With a generous budget and 2 protected, all should be kept.
	allKept := filterByBudget(msgs, scored, 99999, 2)
	if len(allKept) != len(msgs) {
		t.Errorf("generous budget: kept %d, want %d", len(allKept), len(msgs))
	}

	// With a tight budget, protected tail (last 2) always kept, system always kept (high score).
	tight := filterByBudget(msgs, scored, 100, 2)
	if len(tight) < 3 {
		t.Errorf("tight budget: kept %d, want at least 3 (system + 2 protected)", len(tight))
	}

	// Protected messages must always appear.
	lastTwo := msgs[len(msgs)-2:]
	for _, want := range lastTwo {
		found := false
		for _, got := range tight {
			if got.Role == want.Role && string(got.Content) == string(want.Content) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("protected message not in result: %s %s", want.Role, want.Content)
		}
	}
}

// ---------------------------------------------------------------------------
// 4. Compression
// ---------------------------------------------------------------------------

func TestCompressCodeBlocksAggressive(t *testing.T) {
	input := "Here is some code:\n```go\nimport \"fmt\"\n\n// just a comment\nfunc main() {\n    fmt.Println(\"hello\")\n}\n// IMPORTANT: do not remove this\n```\nEnd."
	result := compressCodeBlocks(input, "aggressive")

	// Should keep import.
	if !contains(result, "import") {
		t.Error("aggressive mode should keep import lines")
	}
	// Should keep function signature.
	if !contains(result, "func main()") {
		t.Error("aggressive mode should keep function signatures")
	}
	// Should keep IMPORTANT comment.
	if !contains(result, "IMPORTANT") {
		t.Error("aggressive mode should keep IMPORTANT comments")
	}
	// Should remove plain comment.
	if contains(result, "just a comment") {
		t.Error("aggressive mode should remove non-IMPORTANT comments")
	}
	// Non-code section should be untouched.
	if !contains(result, "Here is some code:") {
		t.Error("non-code text should be preserved")
	}
	if !contains(result, "End.") {
		t.Error("non-code text should be preserved")
	}
}

func TestCompressCodeBlocksConservative(t *testing.T) {
	input := "```go\nimport \"fmt\"\n\n\n\nfunc main() {\n    fmt.Println(\"hello\")\n}\n```"
	result := compressCodeBlocks(input, "conservative")

	// Conservative should keep everything except collapse blank lines.
	if !contains(result, "import") {
		t.Error("conservative should keep import")
	}
	if !contains(result, "func main()") {
		t.Error("conservative should keep function signature")
	}
	// Should not have 4 consecutive blank lines.
	if contains(result, "\n\n\n\n") {
		t.Error("conservative should collapse excessive blank lines")
	}
}

func TestCompressCodeBlocksBalanced(t *testing.T) {
	input := "```go\nimport \"fmt\"\n// a normal comment\nfunc main() {\n    fmt.Println(\"hello\")\n}\n// IMPORTANT: keep this\n```"
	result := compressCodeBlocks(input, "balanced")

	if !contains(result, "import") {
		t.Error("balanced should keep import")
	}
	if !contains(result, "func main()") {
		t.Error("balanced should keep signature")
	}
	if contains(result, "a normal comment") {
		t.Error("balanced should remove non-IMPORTANT comments")
	}
	if !contains(result, "IMPORTANT") {
		t.Error("balanced should keep IMPORTANT comments")
	}
}

func TestCompressCodeBlocksNoCode(t *testing.T) {
	input := "Just some regular text with no code blocks."
	result := compressCodeBlocks(input, "aggressive")
	// splitCodeSections appends a trailing newline; trim for comparison.
	if strings.TrimSpace(result) != input {
		t.Errorf("text without code blocks should be unchanged (modulo trailing newline), got %q", result)
	}
}

func TestCompressConversationProtectedWindow(t *testing.T) {
	msgs := []Message{
		msg("user", "first message"),
		msg("assistant", "first response"),
		msg("user", "second message"),
		msg("assistant", "second response"),
		msg("user", "third message"),
		msg("assistant", "third response"),
	}

	// Protected window of 4: last 4 messages untouched.
	result := compressConversation(msgs, 4, "aggressive")

	// Last 4 messages should be exactly the same.
	for i := 2; i < 6; i++ {
		if string(result[i].Content) != string(msgs[i].Content) {
			t.Errorf("protected message at index %d was modified", i)
		}
	}
}

func TestCompressConversationConservative(t *testing.T) {
	// Conservative only compresses very long messages (>2000 chars).
	short := msg("user", "short message")
	msgs := []Message{short, short, short, short}
	result := compressConversation(msgs, 0, "conservative")

	for i, m := range result {
		if string(m.Content) != string(msgs[i].Content) {
			t.Errorf("short message at index %d was modified by conservative mode", i)
		}
	}
}

func TestCompressTextConservativeTruncates(t *testing.T) {
	// Build a very long text (>2000 chars).
	long := make([]byte, 3000)
	for i := range long {
		long[i] = 'a'
	}
	result := compressConservative(string(long))
	if len(result) > 1600 {
		t.Errorf("conservative should truncate long text, got length %d", len(result))
	}
	if !contains(result, "truncated") {
		t.Error("conservative should add truncation marker")
	}
}

func TestCompressTextBalancedShortPassthrough(t *testing.T) {
	short := "Hello. World."
	result := compressBalanced(short)
	if result != short {
		t.Errorf("balanced should pass through short text, got %q", result)
	}
}

func TestCompressTextAggressiveBullets(t *testing.T) {
	input := "This is a long line that needs compression.\n- First bullet point\n- Second bullet point\nAnother regular line here.\n# Header line\nMore filler text to exceed the threshold."
	result := compressAggressive(input)

	// Should keep bullet points.
	if !contains(result, "First bullet") {
		t.Error("aggressive should keep bullet points")
	}
	if !contains(result, "Header line") {
		t.Error("aggressive should keep headers")
	}
}

func TestCollapseBlankLines(t *testing.T) {
	input := "a\n\n\n\n\nb"
	got := collapseBlankLines(input, 1)
	// Should have at most 1 blank line between a and b.
	if contains(got, "\n\n\n") {
		t.Errorf("collapseBlankLines should limit to 1 blank, got %q", got)
	}
}

// contains is a simple substring check for tests.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstring(s, substr)
}

func searchSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// 5. Process pipeline
// ---------------------------------------------------------------------------

func TestProcessBelowThresholdPassthrough(t *testing.T) {
	cfg := enabledConfig("balanced")
	cfg.Thresholds.MinInputTokens = 100000 // very high threshold
	p := NewProcessor(testLogger(), cfg)

	msgs := []Message{
		msg("system", "you are helpful"),
		msg("user", "hello"),
		msg("assistant", "hi"),
		msg("user", "bye"),
		msg("assistant", "goodbye"),
	}

	result, err := p.Process(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Process() error: %v", err)
	}
	// Should passthrough unchanged.
	if len(result.Messages) != len(msgs) {
		t.Errorf("below threshold: got %d messages, want %d", len(result.Messages), len(msgs))
	}
	if result.OriginalTokens != result.OptimizedTokens {
		t.Error("below threshold: tokens should be equal (passthrough)")
	}
	if result.Metadata != nil {
		t.Error("below threshold: metadata should be nil for passthrough")
	}
}

func TestProcessBelowMinMessagesPassthrough(t *testing.T) {
	cfg := enabledConfig("balanced")
	cfg.Thresholds.MinMessages = 100
	p := NewProcessor(testLogger(), cfg)

	msgs := []Message{
		msg("system", "sys"),
		msg("user", "hello"),
	}

	result, err := p.Process(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Process() error: %v", err)
	}
	if len(result.Messages) != len(msgs) {
		t.Errorf("below min messages: got %d, want %d", len(result.Messages), len(msgs))
	}
}

func TestProcessFullPipelineReducesTokens(t *testing.T) {
	cfg := enabledConfig("balanced")
	cfg.Thresholds.MinInputTokens = 10
	cfg.Thresholds.MinMessages = 1
	cfg.ProtectedWindow = 2
	p := NewProcessor(testLogger(), cfg)

	// Build a realistic multi-message conversation that exceeds thresholds.
	msgs := []Message{
		msg("system", "You are a helpful coding assistant. Always provide clear explanations."),
		msg("user", "Can you help me write a Go function that sorts a slice of integers?"),
		msg("assistant", "Sure! Here is a simple implementation using sort.Ints from the standard library. You can also implement quicksort manually if you prefer."),
		msg("user", "That works. Now can you also add error handling for nil slices?"),
		msg("assistant", "Of course. Here is the updated version with nil check. If the slice is nil we return early without panicking."),
		msg("user", "What about concurrent access? Is sort.Ints thread-safe?"),
		msg("assistant", "sort.Ints is not thread-safe. You need to synchronize access with a mutex or use a copy of the slice for sorting."),
		msg("user", "Thanks. Can you show me the mutex approach?"),
		msg("assistant", "Here is how you would protect the slice with sync.Mutex. Lock before sort and unlock after."),
	}

	result, err := p.Process(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Process() error: %v", err)
	}

	// Should have metadata since metadata=true in config.
	if result.Metadata == nil {
		t.Fatal("expected metadata for eligible request")
	}
	if !result.Metadata.Enabled {
		t.Error("metadata.Enabled should be true")
	}
	if result.Metadata.Mode != "balanced" {
		t.Errorf("metadata.Mode = %q, want %q", result.Metadata.Mode, "balanced")
	}

	// Should have reduced or equal tokens.
	if result.OptimizedTokens > result.OriginalTokens {
		t.Errorf("optimized tokens (%d) > original tokens (%d)", result.OptimizedTokens, result.OriginalTokens)
	}

	// System message should still be present (highest score).
	hasSystem := false
	for _, m := range result.Messages {
		if m.Role == "system" {
			hasSystem = true
			break
		}
	}
	if !hasSystem {
		t.Error("system message should be preserved (highest score)")
	}
}

func TestProcessMetadataDisabled(t *testing.T) {
	cfg := enabledConfig("balanced")
	cfg.Metadata = false
	p := NewProcessor(testLogger(), cfg)

	msgs := make([]Message, 20)
	for i := range msgs {
		if i%2 == 0 {
			msgs[i] = msg("user", "this is a fairly long user message to exceed token thresholds easily")
		} else {
			msgs[i] = msg("assistant", "this is a fairly long assistant response that provides detailed information")
		}
	}
	msgs[0] = msg("system", "You are a helpful coding assistant. Always provide clear and detailed explanations.")

	result, err := p.Process(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Process() error: %v", err)
	}
	if result.Metadata != nil {
		t.Error("metadata should be nil when metadata=false")
	}
}

func TestProcessorEnabledAndMode(t *testing.T) {
	cfg := enabledConfig("aggressive")
	p := NewProcessor(testLogger(), cfg)

	if !p.Enabled() {
		t.Error("Enabled() = false, want true")
	}
	if p.Mode() != "aggressive" {
		t.Errorf("Mode() = %q, want %q", p.Mode(), "aggressive")
	}

	disabled := NewProcessor(testLogger(), DefaultConfig())
	if disabled.Enabled() {
		t.Error("default config should be disabled")
	}
}

// ---------------------------------------------------------------------------
// 6. Config defaults
// ---------------------------------------------------------------------------

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Enabled {
		t.Error("DefaultConfig().Enabled should be false")
	}
	if cfg.Mode != "balanced" {
		t.Errorf("DefaultConfig().Mode = %q, want %q", cfg.Mode, "balanced")
	}
	if cfg.Thresholds.MinInputTokens != 12000 {
		t.Errorf("DefaultConfig().Thresholds.MinInputTokens = %d, want 12000", cfg.Thresholds.MinInputTokens)
	}
	if cfg.Thresholds.MinMessages != 16 {
		t.Errorf("DefaultConfig().Thresholds.MinMessages = %d, want 16", cfg.Thresholds.MinMessages)
	}
	if cfg.ProtectedWindow != 8 {
		t.Errorf("DefaultConfig().ProtectedWindow = %d, want 8", cfg.ProtectedWindow)
	}
	if !cfg.Compression.Conversation {
		t.Error("DefaultConfig().Compression.Conversation should be true")
	}
	if !cfg.Compression.Code {
		t.Error("DefaultConfig().Compression.Code should be true")
	}
	if !cfg.Compression.Deduplicate {
		t.Error("DefaultConfig().Compression.Deduplicate should be true")
	}
	if !cfg.Metadata {
		t.Error("DefaultConfig().Metadata should be true")
	}
}

// ---------------------------------------------------------------------------
// 7. Stats thread safety
// ---------------------------------------------------------------------------

func TestStatsRecordAndSnapshot(t *testing.T) {
	var s Stats

	s.Record(1000, 600, 5*time.Millisecond)
	s.Record(2000, 1200, 10*time.Millisecond)

	snap := s.Snapshot()
	if snap.RequestsOptimized != 2 {
		t.Errorf("RequestsOptimized = %d, want 2", snap.RequestsOptimized)
	}
	if snap.TotalTokensSaved != 400+800 {
		t.Errorf("TotalTokensSaved = %d, want %d", snap.TotalTokensSaved, 400+800)
	}
	if snap.TotalOriginal != 3000 {
		t.Errorf("TotalOriginal = %d, want 3000", snap.TotalOriginal)
	}
	if snap.TotalOptimized != 1800 {
		t.Errorf("TotalOptimized = %d, want 1800", snap.TotalOptimized)
	}
	if snap.TotalDurationMs != 15 {
		t.Errorf("TotalDurationMs = %d, want 15", snap.TotalDurationMs)
	}
}

func TestStatsNegativeSavedClamped(t *testing.T) {
	var s Stats
	// original < optimized should clamp saved to 0.
	s.Record(100, 200, time.Millisecond)
	snap := s.Snapshot()
	if snap.TotalTokensSaved != 0 {
		t.Errorf("negative saved clamped: TotalTokensSaved = %d, want 0", snap.TotalTokensSaved)
	}
}

func TestStatsConcurrency(t *testing.T) {
	var s Stats
	var wg sync.WaitGroup
	const goroutines = 50
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			s.Record(1000, 500, time.Millisecond)
		}()
	}
	wg.Wait()

	snap := s.Snapshot()
	if snap.RequestsOptimized != goroutines {
		t.Errorf("concurrent Record: RequestsOptimized = %d, want %d", snap.RequestsOptimized, goroutines)
	}
	if snap.TotalTokensSaved != int64(goroutines)*500 {
		t.Errorf("concurrent Record: TotalTokensSaved = %d, want %d", snap.TotalTokensSaved, int64(goroutines)*500)
	}
}

func TestStatsSnapshotIsCopy(t *testing.T) {
	var s Stats
	s.Record(1000, 500, time.Millisecond)

	snap1 := s.Snapshot()
	s.Record(2000, 1000, time.Millisecond)
	snap2 := s.Snapshot()

	// snap1 should not have changed.
	if snap1.RequestsOptimized != 1 {
		t.Errorf("snapshot should be a copy: snap1.RequestsOptimized = %d, want 1", snap1.RequestsOptimized)
	}
	if snap2.RequestsOptimized != 2 {
		t.Errorf("snap2.RequestsOptimized = %d, want 2", snap2.RequestsOptimized)
	}
}
