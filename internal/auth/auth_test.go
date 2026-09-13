package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zealish/zealish-router/internal/storage"
)

func newStore(t *testing.T) storage.APIKeyStore {
	t.Helper()
	return storage.NewMemory().APIKeys()
}

func TestAuthenticateDisabledAllowsAnonymous(t *testing.T) {
	s := NewService(false, nil, nil, nil)

	id, err := s.Authenticate(context.Background(), "")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if id.KeyID != "anonymous" {
		t.Errorf("KeyID = %q, want anonymous", id.KeyID)
	}
}

func TestAuthenticateStaticKey(t *testing.T) {
	s := NewService(true, []string{"zr_static"}, nil, nil)
	ctx := context.Background()

	if _, err := s.Authenticate(ctx, "zr_static"); err != nil {
		t.Fatalf("valid static key rejected: %v", err)
	}
	if _, err := s.Authenticate(ctx, "zr_wrong"); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("err = %v, want ErrUnauthorized", err)
	}
	if _, err := s.Authenticate(ctx, ""); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("empty key: err = %v, want ErrUnauthorized", err)
	}
}

func TestAuthenticateStoredKeyTouchesLastUsed(t *testing.T) {
	ctx := context.Background()
	keys := newStore(t)

	generated, err := GenerateKey("ci")
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if err := keys.Create(ctx, generated.Record); err != nil {
		t.Fatalf("Create: %v", err)
	}

	s := NewService(true, nil, keys, nil)
	id, err := s.Authenticate(ctx, generated.Raw)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if id.KeyID != generated.Record.ID || id.Name != "ci" {
		t.Fatalf("identity = %+v", id)
	}

	// TouchLastUsed runs in the background; poll briefly for it to land.
	deadline := time.Now().Add(2 * time.Second)
	for {
		got, err := keys.GetByHash(ctx, generated.Record.KeyHash)
		if err != nil {
			t.Fatalf("GetByHash: %v", err)
		}
		if !got.LastUsedAt.IsZero() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("last_used_at was never recorded")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAuthenticateRejectsDisabledKey(t *testing.T) {
	ctx := context.Background()
	keys := newStore(t)

	generated, _ := GenerateKey("revoked")
	generated.Record.Enabled = false
	if err := keys.Create(ctx, generated.Record); err != nil {
		t.Fatalf("Create: %v", err)
	}

	s := NewService(true, nil, keys, nil)
	if _, err := s.Authenticate(ctx, generated.Raw); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

func TestBearerToken(t *testing.T) {
	tests := []struct {
		header string
		want   string
	}{
		{"Bearer abc123", "abc123"},
		{"bearer abc123", "abc123"},
		{"Bearer   abc123  ", "abc123"},
		{"Basic abc123", ""},
		{"Bearer", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := BearerToken(tt.header); got != tt.want {
			t.Errorf("BearerToken(%q) = %q, want %q", tt.header, got, tt.want)
		}
	}
}

func TestGenerateKeyShape(t *testing.T) {
	generated, err := GenerateKey("dashboard")
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	if !strings.HasPrefix(generated.Raw, KeyPrefix) {
		t.Errorf("raw key %q lacks the %q prefix", generated.Raw, KeyPrefix)
	}
	if got := len(generated.Raw) - len(KeyPrefix); got != secretBytes {
		t.Errorf("secret length = %d, want %d", got, secretBytes)
	}
	if generated.Record.KeyHash != Hash(generated.Raw) {
		t.Error("stored hash does not match the raw key")
	}
	if strings.Contains(generated.Record.KeyHash, generated.Raw) {
		t.Error("raw key leaked into the persisted record")
	}
	if !generated.Record.Enabled || generated.Record.CreatedAt.IsZero() {
		t.Errorf("record not initialised: %+v", generated.Record)
	}
}

func TestGenerateKeyIsUnique(t *testing.T) {
	seen := make(map[string]struct{}, 100)
	for range 100 {
		g, err := GenerateKey("bulk")
		if err != nil {
			t.Fatalf("GenerateKey: %v", err)
		}
		if _, dup := seen[g.Raw]; dup {
			t.Fatal("generated a duplicate key")
		}
		seen[g.Raw] = struct{}{}
	}
}
