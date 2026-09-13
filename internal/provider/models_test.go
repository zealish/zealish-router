package provider

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestListModels(t *testing.T) {
	var gotPath, gotAuth string
	p, _ := newTestProvider(t, "openai", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-5","owned_by":"openai"},{"id":""},{"id":"o3"}]}`))
	})

	models, err := p.(ModelLister).ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if gotPath != "/models" {
		t.Errorf("path = %q, want /models", gotPath)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("authorization = %q, want the provider key", gotAuth)
	}
	if len(models) != 2 || models[0].ID != "gpt-5" || models[1].ID != "o3" {
		t.Fatalf("models = %+v, want gpt-5 and o3 with the blank entry dropped", models)
	}
	if models[0].OwnedBy != "openai" {
		t.Errorf("owned_by = %q, want openai", models[0].OwnedBy)
	}
}

func TestListModelsUpstreamError(t *testing.T) {
	p, _ := newTestProvider(t, "openai", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Incorrect API key provided: sk-test"}}`))
	})

	_, err := p.(ModelLister).ListModels(context.Background())
	if err == nil {
		t.Fatal("want an error for a 401 catalogue response")
	}
	var perr *Error
	if !errors.As(err, &perr) {
		t.Fatalf("err = %T, want *Error", err)
	}
	if perr.Status != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", perr.Status)
	}
	if strings.Contains(perr.Message, "sk-test") {
		t.Errorf("message = %q, want the key redacted", perr.Message)
	}
}
