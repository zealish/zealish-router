package anthropic

import (
	"encoding/json"
	"testing"
)

func TestSystemText(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"absent", "", ""},
		{"string", `"be brief"`, "be brief"},
		{"single block", `[{"type":"text","text":"be brief"}]`, "be brief"},
		{"joined blocks", `[{"type":"text","text":"be brief"},{"type":"text","text":"be kind"}]`, "be brief\n\nbe kind"},
		{"non-text blocks skipped", `[{"type":"image"},{"type":"text","text":"be brief"}]`, "be brief"},
		{"unknown shape", `42`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SystemText(json.RawMessage(tc.raw)); got != tc.want {
				t.Errorf("SystemText(%s) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}
