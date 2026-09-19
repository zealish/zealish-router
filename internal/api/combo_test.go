package api

import (
	"net/http"
	"testing"
)

// seedCombo installs a provider and two aliases a combo can point at.
func seedCombo(t *testing.T, h http.Handler) {
	t.Helper()

	adminRequest(t, h, http.MethodPut, "/api/v1/providers/openai",
		`{"kind":"openai","base_url":"https://api.openai.com/v1","api_key":"sk-test","timeout_ms":30000,"enabled":true}`)
	adminRequest(t, h, http.MethodPut, "/api/v1/models/gpt-5", `{"provider":"openai","model":"gpt-5-upstream"}`)
	adminRequest(t, h, http.MethodPut, "/api/v1/models/fast", `{"provider":"openai","model":"gpt-5-mini"}`)
}

func TestAdminComboCRUDRepublishesRoutes(t *testing.T) {
	h, _, engine := newAdminServer(t)
	seedCombo(t, h)

	rec := adminRequest(t, h, http.MethodPut, "/api/v1/combos/code-agent",
		`{"strategy":"round_robin","members":["gpt-5","fast"],"enabled":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put combo status = %d (body=%q)", rec.Code, rec.Body.String())
	}

	// The engine must route the combo without a restart.
	if chain := engine.Chain("code-agent"); len(chain) != 2 {
		t.Errorf("chain = %v, want two members", chain)
	}
	route, err := engine.Resolve("code-agent")
	if err != nil {
		t.Fatalf("Resolve combo: %v", err)
	}
	if route.Provider != "openai" {
		t.Errorf("route = %+v, want a concrete member route", route)
	}

	combos := decodeJSON[[]map[string]any](t,
		adminRequest(t, h, http.MethodGet, "/api/v1/combos", ""))
	if len(combos) != 1 || combos[0]["name"] != "code-agent" {
		t.Fatalf("list = %v, want the stored combo", combos)
	}
	if combos[0]["strategy"] != "round_robin" {
		t.Errorf("strategy = %v, want round_robin", combos[0]["strategy"])
	}

	if got := adminRequest(t, h, http.MethodDelete, "/api/v1/combos/code-agent", "").Code; got != http.StatusNoContent {
		t.Fatalf("delete combo status = %d, want 204", got)
	}
	if _, err := engine.Resolve("code-agent"); err == nil {
		t.Error("deleted combo still resolves")
	}
}

func TestAdminComboRenameRepublishesRoutes(t *testing.T) {
	h, _, engine := newAdminServer(t)
	seedCombo(t, h)
	if rec := adminRequest(t, h, http.MethodPut, "/api/v1/combos/old-name", `{"strategy":"round_robin","members":["gpt-5","fast"],"enabled":true}`); rec.Code != http.StatusOK {
		t.Fatalf("put combo status = %d (body=%q)", rec.Code, rec.Body.String())
	}

	rec := adminRequest(t, h, http.MethodPost, "/api/v1/combos/old-name/rename", `{"name":"new-name"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("rename status = %d (body=%q)", rec.Code, rec.Body.String())
	}
	response := decodeJSON[map[string]any](t, rec)
	if response["name"] != "new-name" || response["strategy"] != "round_robin" {
		t.Fatalf("rename response = %v", response)
	}
	if _, err := engine.Resolve("old-name"); err == nil {
		t.Error("old combo name still resolves after rename")
	}
	if _, err := engine.Resolve("new-name"); err != nil {
		t.Fatalf("new combo name does not resolve: %v", err)
	}

	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{"blank", `{"name":"   "}`, http.StatusBadRequest},
		{"alias collision", `{"name":"gpt-5"}`, http.StatusConflict},
		{"combo collision", `{"name":"other"}`, http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "combo collision" {
				adminRequest(t, h, http.MethodPut, "/api/v1/combos/other", `{"members":["gpt-5"]}`)
			}
			rec := adminRequest(t, h, http.MethodPost, "/api/v1/combos/new-name/rename", tc.body)
			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d (body=%q)", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestAdminComboValidation(t *testing.T) {
	h, _, _ := newAdminServer(t)
	seedCombo(t, h)

	cases := []struct {
		name string
		path string
		body string
		want int
	}{
		{"no members", "/api/v1/combos/c", `{"members":[]}`, http.StatusBadRequest},
		{"unknown member", "/api/v1/combos/c", `{"members":["ghost"]}`, http.StatusBadRequest},
		{"bad strategy", "/api/v1/combos/c", `{"strategy":"random","members":["gpt-5"]}`, http.StatusBadRequest},
		{"weight count mismatch", "/api/v1/combos/c",
			`{"strategy":"weighted","members":["gpt-5","fast"],"weights":[3]}`, http.StatusBadRequest},
		{"non-positive weight", "/api/v1/combos/c",
			`{"strategy":"weighted","members":["gpt-5"],"weights":[0]}`, http.StatusBadRequest},
		{"alias name clash", "/api/v1/combos/gpt-5", `{"members":["fast"]}`, http.StatusConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := adminRequest(t, h, http.MethodPut, tc.path, tc.body)
			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d (body=%q)", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestAdminComboAcceptsIntelligentStrategy(t *testing.T) {
	h, _, engine := newAdminServer(t)
	seedCombo(t, h)

	rec := adminRequest(t, h, http.MethodPut, "/api/v1/combos/code-agent",
		`{"strategy":"intelligent","members":["gpt-5","fast"],"enabled":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put combo status = %d (body=%q)", rec.Code, rec.Body.String())
	}
	if chain := engine.Chain("code-agent"); len(chain) != 2 {
		t.Errorf("chain = %v, want both members", chain)
	}
}

func TestComboAppearsInModelList(t *testing.T) {
	h, _, _ := newAdminServer(t)
	seedCombo(t, h)
	adminRequest(t, h, http.MethodPut, "/api/v1/combos/code-agent", `{"members":["gpt-5","fast"]}`)

	rec := adminRequest(t, h, http.MethodGet, "/v1/models", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%q)", rec.Code, rec.Body.String())
	}

	list := decodeJSON[struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}](t, rec)

	var found bool
	for _, m := range list.Data {
		if m.ID == "code-agent" {
			found = true
		}
	}
	if !found {
		t.Errorf("combo missing from /v1/models: %+v", list.Data)
	}
}

func TestDeletingProviderPrunesComboMembers(t *testing.T) {
	h, store, engine := newAdminServer(t)
	seedCombo(t, h)
	adminRequest(t, h, http.MethodPut, "/api/v1/providers/ollama",
		`{"kind":"openai","base_url":"http://localhost:11434/v1","timeout_ms":30000,"enabled":true}`)
	adminRequest(t, h, http.MethodPut, "/api/v1/models/local", `{"provider":"ollama","model":"qwen3:32b"}`)
	adminRequest(t, h, http.MethodPut, "/api/v1/combos/code-agent", `{"members":["gpt-5","local"]}`)

	if got := adminRequest(t, h, http.MethodDelete, "/api/v1/providers/ollama", "").Code; got != http.StatusNoContent {
		t.Fatalf("delete provider status = %d, want 204", got)
	}

	combo, err := store.Combos().Get(t.Context(), "code-agent")
	if err != nil {
		t.Fatalf("get combo: %v", err)
	}
	if len(combo.Members) != 1 || combo.Members[0] != "gpt-5" {
		t.Errorf("members = %v, want only gpt-5", combo.Members)
	}
	if _, err := engine.Resolve("code-agent"); err != nil {
		t.Errorf("pruned combo stopped resolving: %v", err)
	}
}

func TestDeletingEveryMemberDropsTheCombo(t *testing.T) {
	h, store, _ := newAdminServer(t)
	seedCombo(t, h)
	adminRequest(t, h, http.MethodPut, "/api/v1/combos/code-agent", `{"members":["gpt-5","fast"]}`)

	if got := adminRequest(t, h, http.MethodDelete, "/api/v1/providers/openai", "").Code; got != http.StatusNoContent {
		t.Fatalf("delete provider status = %d, want 204", got)
	}

	combos, err := store.Combos().List(t.Context())
	if err != nil {
		t.Fatalf("list combos: %v", err)
	}
	if len(combos) != 0 {
		t.Errorf("combos = %v, want the empty combo dropped", combos)
	}
}
