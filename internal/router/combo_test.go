package router

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/zealish/zealish-router/internal/provider"
	"github.com/zealish/zealish-router/internal/storage"
	"github.com/zealish/zealish-router/pkg/openai"
)

// newComboEngine builds an engine over three single-provider aliases plus the
// combo under test.
func newComboEngine(t *testing.T, combo storage.Combo, providers ...provider.Provider) *Engine {
	t.Helper()

	aliases := []storage.ModelAlias{
		{Alias: "gpt-5", Provider: "openai", Model: "gpt-5-upstream"},
		{Alias: "fast", Provider: "openrouter", Model: "gpt-5-mini"},
		{Alias: "local", Provider: "ollama", Model: "qwen3:32b"},
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	e := NewEngine(logger, nil)
	e.Reload(aliases, []storage.Combo{combo}, provider.NewRegistry(providers...))
	e.retry = Retry{Attempts: 1}
	return e
}

func fallbackCombo(members ...string) storage.Combo {
	return storage.Combo{Name: "code-agent", Strategy: storage.ComboFallback, Members: members, Enabled: true}
}

func TestComboChainExpandsMembersAndTheirFallbacks(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	e := NewEngine(logger, nil)
	e.Reload([]storage.ModelAlias{
		{Alias: "gpt-5", Provider: "openai", Model: "m", Fallback: []string{"local"}},
		{Alias: "fast", Provider: "openrouter", Model: "m"},
		{Alias: "local", Provider: "ollama", Model: "m"},
	}, []storage.Combo{fallbackCombo("gpt-5", "fast", "local")}, provider.NewRegistry())

	got := e.Chain("code-agent")
	want := []string{"gpt-5", "local", "fast"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("chain = %v, want %v", got, want)
	}
}

// A combo reports the union of its members' capabilities: any member may take
// the request, so the combo serves whatever any of them serves.
func TestComboCapabilitiesAreTheMemberUnion(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	e := NewEngine(logger, nil)
	e.Reload([]storage.ModelAlias{
		{Alias: "gpt-5", Provider: "openai", Model: "m", Capabilities: []string{"chat", "vision"}},
		{Alias: "fast", Provider: "openrouter", Model: "m", Capabilities: []string{"chat", "tools"}},
	}, []storage.Combo{fallbackCombo("gpt-5", "fast")}, provider.NewRegistry())

	if got := strings.Join(e.Capabilities("code-agent"), ","); got != "chat,vision,tools" {
		t.Errorf("combo capabilities = %q, want chat,vision,tools", got)
	}
	if got := strings.Join(e.Capabilities("gpt-5"), ","); got != "chat,vision" {
		t.Errorf("alias capabilities = %q, want chat,vision", got)
	}
	if got := e.Capabilities("ghost"); got != nil {
		t.Errorf("unknown name capabilities = %v, want nil", got)
	}
}

func TestComboFallbackCascadesToNextMember(t *testing.T) {
	primary := &fakeProvider{name: "openai", results: []error{upstreamErr(429, provider.ErrRateLimited)}}
	secondary := &fakeProvider{name: "openrouter"}

	e := newComboEngine(t, fallbackCombo("gpt-5", "fast"), primary, secondary)

	resp, err := e.ChatCompletion(context.Background(), &openai.ChatCompletionRequest{Model: "code-agent"})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if resp.ID != "openrouter" {
		t.Errorf("served by %q, want openrouter", resp.ID)
	}
	if resp.Model != "gpt-5-mini" {
		t.Errorf("upstream model = %q, want gpt-5-mini", resp.Model)
	}
	if primary.callCount() != 1 {
		t.Errorf("primary calls = %d, want 1", primary.callCount())
	}
}

func TestComboRoundRobinRotatesStartingMember(t *testing.T) {
	e := newComboEngine(t,
		storage.Combo{
			Name:     "code-agent",
			Strategy: storage.ComboRoundRobin,
			Members:  []string{"gpt-5", "fast", "local"},
			Enabled:  true,
		},
		&fakeProvider{name: "openai"},
		&fakeProvider{name: "openrouter"},
		&fakeProvider{name: "ollama"},
	)

	want := []string{"openai", "openrouter", "ollama", "openai"}
	for i, served := range want {
		resp, err := e.ChatCompletion(context.Background(), &openai.ChatCompletionRequest{Model: "code-agent"})
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		if resp.ID != served {
			t.Errorf("request %d served by %q, want %q", i, resp.ID, served)
		}
	}
}

func TestComboWeightedSpreadsStartsByWeight(t *testing.T) {
	e := newComboEngine(t,
		storage.Combo{
			Name:     "code-agent",
			Strategy: storage.ComboWeighted,
			Members:  []string{"gpt-5", "fast", "local"},
			Weights:  []int{3, 1, 2},
			Enabled:  true,
		},
		&fakeProvider{name: "openai"},
		&fakeProvider{name: "openrouter"},
		&fakeProvider{name: "ollama"},
	)

	served := map[string]int{}
	for i := range 12 {
		resp, err := e.ChatCompletion(context.Background(), &openai.ChatCompletionRequest{Model: "code-agent"})
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		served[resp.ID]++
	}

	// Two full cycles of 3+1+2 slots.
	want := map[string]int{"openai": 6, "openrouter": 2, "ollama": 4}
	for name, count := range want {
		if served[name] != count {
			t.Errorf("%s served %d requests, want %d (got %v)", name, served[name], count, served)
		}
	}
}

func TestComboWeightedFallsBackToTheRestOfThePool(t *testing.T) {
	e := newComboEngine(t,
		storage.Combo{
			Name:     "code-agent",
			Strategy: storage.ComboWeighted,
			Members:  []string{"gpt-5", "fast"},
			Weights:  []int{5, 1},
			Enabled:  true,
		},
		&fakeProvider{name: "openai", results: []error{upstreamErr(429, provider.ErrRateLimited)}},
		&fakeProvider{name: "openrouter"},
	)

	resp, err := e.ChatCompletion(context.Background(), &openai.ChatCompletionRequest{Model: "code-agent"})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if resp.ID != "openrouter" {
		t.Errorf("served by %q, want openrouter", resp.ID)
	}
}

func TestComboWeightedTreatsMissingWeightsAsEqual(t *testing.T) {
	e := newComboEngine(t,
		storage.Combo{
			Name:     "code-agent",
			Strategy: storage.ComboWeighted,
			Members:  []string{"gpt-5", "fast"},
			Enabled:  true,
		},
		&fakeProvider{name: "openai"},
		&fakeProvider{name: "openrouter"},
	)

	want := []string{"openai", "openrouter", "openai"}
	for i, served := range want {
		resp, err := e.ChatCompletion(context.Background(), &openai.ChatCompletionRequest{Model: "code-agent"})
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		if resp.ID != served {
			t.Errorf("request %d served by %q, want %q", i, resp.ID, served)
		}
	}
}

func intelligentCombo(members ...string) storage.Combo {
	return storage.Combo{
		Name:     "code-agent",
		Strategy: storage.ComboIntelligent,
		Members:  members,
		Enabled:  true,
	}
}

// With no history every member scores neutral, so the pool keeps rotating:
// a cold combo explores instead of pinning traffic on its first member.
func TestComboIntelligentRotatesWithoutHistory(t *testing.T) {
	e := newComboEngine(t, intelligentCombo("gpt-5", "fast", "local"),
		&fakeProvider{name: "openai"},
		&fakeProvider{name: "openrouter"},
		&fakeProvider{name: "ollama"},
	)

	want := []string{"openai", "openrouter", "ollama"}
	for i, served := range want {
		resp, err := e.ChatCompletion(context.Background(), &openai.ChatCompletionRequest{Model: "code-agent"})
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		if resp.ID != served {
			t.Errorf("request %d served by %q, want %q", i, resp.ID, served)
		}
	}
}

func TestComboIntelligentPrefersTheHealthierMember(t *testing.T) {
	e := newComboEngine(t, intelligentCombo("gpt-5", "fast"),
		&fakeProvider{name: "openai"},
		&fakeProvider{name: "openrouter"},
	)

	// "gpt-5" answers half the time, "fast" always: the record must outweigh
	// the rotation that would otherwise start at gpt-5.
	for range 12 {
		e.stats.Record(RequestMetric{Alias: "gpt-5", Success: false, LatencyMs: 900, Timestamp: time.Now()})
		e.stats.Record(RequestMetric{Alias: "gpt-5", Success: true, LatencyMs: 900, Timestamp: time.Now()})
		e.stats.Record(RequestMetric{Alias: "fast", Success: true, LatencyMs: 900, Timestamp: time.Now()})
		e.stats.Record(RequestMetric{Alias: "fast", Success: true, LatencyMs: 900, Timestamp: time.Now()})
	}

	for i := range 3 {
		resp, err := e.ChatCompletion(context.Background(), &openai.ChatCompletionRequest{Model: "code-agent"})
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		if resp.ID != "openrouter" {
			t.Errorf("request %d served by %q, want openrouter", i, resp.ID)
		}
	}
}

// Equal success rates are broken by latency: the faster route goes first.
func TestComboIntelligentPrefersTheFasterMember(t *testing.T) {
	e := newComboEngine(t, intelligentCombo("gpt-5", "fast"),
		&fakeProvider{name: "openai"},
		&fakeProvider{name: "openrouter"},
	)

	for range 20 {
		e.stats.Record(RequestMetric{Alias: "gpt-5", Success: true, LatencyMs: 8000, Timestamp: time.Now()})
		e.stats.Record(RequestMetric{Alias: "fast", Success: true, LatencyMs: 200, Timestamp: time.Now()})
	}

	resp, err := e.ChatCompletion(context.Background(), &openai.ChatCompletionRequest{Model: "code-agent"})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if resp.ID != "openrouter" {
		t.Errorf("served by %q, want the faster openrouter", resp.ID)
	}
}

// An open circuit ranks below every routable member, however good its history.
func TestComboIntelligentDemotesAnOpenCircuit(t *testing.T) {
	e := newComboEngine(t, intelligentCombo("gpt-5", "fast"),
		&fakeProvider{name: "openai"},
		&fakeProvider{name: "openrouter"},
	)

	for range 20 {
		e.stats.Record(RequestMetric{Alias: "gpt-5", Success: true, LatencyMs: 100, Timestamp: time.Now()})
		e.stats.Record(RequestMetric{Alias: "fast", Success: false, LatencyMs: 5000, Timestamp: time.Now()})
		e.stats.Record(RequestMetric{Alias: "fast", Success: true, LatencyMs: 5000, Timestamp: time.Now()})
	}
	for range DefaultBreaker.FailureThreshold {
		e.breakers.failure("openai")
	}

	resp, err := e.ChatCompletion(context.Background(), &openai.ChatCompletionRequest{Model: "code-agent"})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if resp.ID != "openrouter" {
		t.Errorf("served by %q, want openrouter: openai's circuit is open", resp.ID)
	}
}

func TestComboSkipsUnroutableMembers(t *testing.T) {
	// "fast" has no registered provider, so the combo must move past it.
	e := newComboEngine(t, fallbackCombo("fast", "local"), &fakeProvider{name: "ollama"})

	resp, err := e.ChatCompletion(context.Background(), &openai.ChatCompletionRequest{Model: "code-agent"})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if resp.ID != "ollama" {
		t.Errorf("served by %q, want ollama", resp.ID)
	}
}

func TestDisabledComboIsNotRoutable(t *testing.T) {
	combo := fallbackCombo("gpt-5")
	combo.Enabled = false
	e := newComboEngine(t, combo, &fakeProvider{name: "openai"})

	if _, err := e.Resolve("code-agent"); err == nil {
		t.Fatal("disabled combo resolved")
	}
	if got := e.Combos(); len(got) != 0 {
		t.Errorf("combos = %v, want none", got)
	}
}

func TestComboNeverShadowsAnAlias(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	e := NewEngine(logger, nil)
	e.Reload([]storage.ModelAlias{
		{Alias: "gpt-5", Provider: "openai", Model: "gpt-5-upstream"},
		{Alias: "fast", Provider: "openrouter", Model: "gpt-5-mini"},
	}, []storage.Combo{
		{Name: "gpt-5", Strategy: storage.ComboFallback, Members: []string{"fast"}, Enabled: true},
	}, provider.NewRegistry())

	route, err := e.Resolve("gpt-5")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if route.Provider != "openai" {
		t.Errorf("gpt-5 resolved to %q, want the alias's own provider", route.Provider)
	}
}
