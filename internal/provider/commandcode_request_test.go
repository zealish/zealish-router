package provider

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/zealish/zealish-router/pkg/openai"
)

func TestCommandCodeRequestCompatibilityGaps(t *testing.T) {
	t.Run("max_output_tokens_precedence", func(t *testing.T) {
		req := testRequest()
		req.Extra = map[string]json.RawMessage{"max_output_tokens": json.RawMessage(`321`)}
		if got := commandCodeRequest(req)["params"].(map[string]any)["max_tokens"]; got != 321 {
			t.Fatalf("fallback = %v", got)
		}
		n := 0
		req.MaxTokens = &n
		if got := commandCodeRequest(req)["params"].(map[string]any)["max_tokens"]; got != 0 {
			t.Fatalf("precedence = %v", got)
		}
		req.MaxTokens = nil
		req.Extra["max_output_tokens"] = json.RawMessage(`null`)
		if got := commandCodeRequest(req)["params"].(map[string]any)["max_tokens"]; got != commandCodeDefaultMaxTokens {
			t.Fatalf("null fallback = %v", got)
		}
	})
	t.Run("non_string_arguments_and_missing_identity", func(t *testing.T) {
		req := testRequest()
		req.Messages = []openai.Message{{Role: "assistant", Extra: map[string]json.RawMessage{"tool_calls": json.RawMessage(`[{"function":{"arguments":{"city":"Jakarta"}}}]`)}}}
		blocks := commandCodeRequest(req)["params"].(map[string]any)["messages"].([]map[string]any)[0]["content"].([]any)
		call := blocks[0].(map[string]any)
		if call["toolCallId"] != "" || call["toolName"] != "" {
			t.Fatalf("identity = %#v", call)
		}
		if got := call["input"].(map[string]any)["city"]; got != "Jakarta" {
			t.Fatalf("input = %#v", call["input"])
		}
	})
	t.Run("image_precedes_text", func(t *testing.T) {
		req := testRequest()
		req.Messages = []openai.Message{{Role: "user", Content: json.RawMessage(`[{"type":"image_url","text":"omit me"},{"type":"image","text":"omit too"},{"type":"text","text":"retain"}]`)}}
		blocks := commandCodeRequest(req)["params"].(map[string]any)["messages"].([]map[string]any)[0]["content"].([]any)
		for i, want := range []string{"[image omitted]", "[image omitted]", "retain"} {
			if got := blocks[i].(map[string]any)["text"]; got != want {
				t.Fatalf("block %d = %v", i, got)
			}
		}
	})
	t.Run("UTC_date", func(t *testing.T) {
		old := time.Local
		time.Local = time.FixedZone("next-day", 24*60*60)
		defer func() { time.Local = old }()
		before := time.Now().UTC().Format("2006-01-02")
		got := commandCodeRequest(testRequest())["config"].(map[string]any)["date"]
		after := time.Now().UTC().Format("2006-01-02")
		if got != before && got != after {
			t.Fatalf("date = %v; UTC = %s", got, before)
		}
	})
}
