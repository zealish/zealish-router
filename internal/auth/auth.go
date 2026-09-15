// Package auth verifies bearer API keys. Keys are compared by SHA-256 hash;
// raw keys are never stored.
package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/zealish/zealish-router/internal/storage"
)

// ErrUnauthorized is returned when a credential is missing or invalid.
var ErrUnauthorized = errors.New("auth: unauthorized")

// Identity describes an authenticated caller. The quota fields are copied off
// the key record so the request path does not re-read storage to enforce them;
// zero on either means unlimited.
type Identity struct {
	KeyID            string
	Name             string
	RateLimitPerMin  int
	MonthlyBudgetUSD float64
	// AllowedModels restricts which model names the caller may address. Empty
	// means every model, which is what static keys and pre-allowlist keys get.
	AllowedModels []string
}

// Allows reports whether the identity may address a model name. It is an
// exact match against the allowlist: aliases and combos share one namespace,
// so no prefix or pattern logic is needed.
func (i Identity) Allows(model string) bool {
	if len(i.AllowedModels) == 0 {
		return true
	}
	return slices.Contains(i.AllowedModels, model)
}

// Authenticator validates credentials presented by clients.
type Authenticator interface {
	Authenticate(ctx context.Context, rawKey string) (Identity, error)
}

// Hash returns the hex-encoded SHA-256 of a raw API key.
func Hash(rawKey string) string {
	sum := sha256.Sum256([]byte(rawKey))
	return hex.EncodeToString(sum[:])
}

// BearerToken extracts the token from an Authorization header value.
func BearerToken(header string) string {
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(header[len(prefix):])
}

// touchTimeout bounds the background last-used write.
const touchTimeout = 3 * time.Second

// Service authenticates keys against static configuration and a key store.
// Static keys are swappable so a configuration reload takes effect without a
// restart; the request path takes a single atomic load.
type Service struct {
	enabled bool
	static  atomic.Pointer[map[string]struct{}]
	keys    storage.APIKeyStore
	logger  *slog.Logger
}

// NewService constructs an Authenticator from injected dependencies. A nil
// logger discards background failures.
func NewService(enabled bool, staticKeys []string, keys storage.APIKeyStore, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	s := &Service{enabled: enabled, keys: keys, logger: logger}
	s.SetStaticKeys(staticKeys)
	return s
}

// SetStaticKeys replaces the configured static keys.
func (s *Service) SetStaticKeys(staticKeys []string) {
	static := make(map[string]struct{}, len(staticKeys))
	for _, k := range staticKeys {
		static[Hash(k)] = struct{}{}
	}
	s.static.Store(&static)
}

// Authenticate implements Authenticator. On a successful store-backed match it
// refreshes last_used_at in the background, so persistence never adds latency
// to the request path.
func (s *Service) Authenticate(ctx context.Context, rawKey string) (Identity, error) {
	if !s.enabled {
		return Identity{KeyID: "anonymous", Name: "anonymous"}, nil
	}
	if rawKey == "" {
		return Identity{}, ErrUnauthorized
	}

	hash := Hash(rawKey)
	if s.matchStatic(hash) {
		return Identity{KeyID: "static", Name: "static"}, nil
	}

	if s.keys != nil {
		key, err := s.keys.GetByHash(ctx, hash)
		if err == nil && key.Enabled {
			s.touch(key.ID)
			return Identity{
				KeyID:            key.ID,
				Name:             key.Name,
				RateLimitPerMin:  key.RateLimitPerMin,
				MonthlyBudgetUSD: key.MonthlyBudgetUSD,
				AllowedModels:    key.AllowedModels,
			}, nil
		}
	}
	return Identity{}, ErrUnauthorized
}

// matchStatic compares hash against every configured static key without an
// early return, so the number of comparisons does not depend on which key (if
// any) matched.
func (s *Service) matchStatic(hash string) bool {
	var matched int
	for known := range *s.static.Load() {
		matched |= subtle.ConstantTimeCompare([]byte(known), []byte(hash))
	}
	return matched == 1
}

// touch records key usage out of band. The request context is deliberately not
// used: it is cancelled as soon as the response completes.
func (s *Service) touch(id string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), touchTimeout)
		defer cancel()

		if err := s.keys.TouchLastUsed(ctx, id, time.Now().UTC()); err != nil {
			s.logger.Warn("could not record api key usage",
				slog.String("key_id", id), slog.Any("error", err))
		}
	}()
}

type contextKey struct{}

// WithIdentity stores an identity in the request context.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}

// FromContext retrieves the identity attached to a request context.
func FromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(contextKey{}).(Identity)
	return id, ok
}

// FromRequest extracts the bearer token of an HTTP request.
func FromRequest(r *http.Request) string {
	return BearerToken(r.Header.Get("Authorization"))
}
