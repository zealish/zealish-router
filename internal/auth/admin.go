package auth

import (
	"context"
	"crypto/subtle"
	"sync/atomic"
)

// AdminService authenticates the dashboard against a single administrative
// token. It is deliberately separate from gateway API keys: an admin token
// grants configuration access, a gateway key only grants model access.
type AdminService struct {
	enabled bool
	token   atomic.Pointer[string]
}

// NewAdminService constructs an admin authenticator. An empty token with
// enabled set rejects every request, so a misconfiguration fails closed.
func NewAdminService(enabled bool, token string) *AdminService {
	s := &AdminService{enabled: enabled}
	s.SetToken(token)
	return s
}

// SetToken replaces the admin token, letting a configuration reload rotate it.
func (s *AdminService) SetToken(token string) {
	s.token.Store(&token)
}

// Authenticate implements Authenticator. The token is compared in constant
// time and carries a fixed identity.
func (s *AdminService) Authenticate(_ context.Context, rawKey string) (Identity, error) {
	if !s.enabled {
		return Identity{}, ErrUnauthorized
	}

	token := *s.token.Load()
	if token == "" || rawKey == "" {
		return Identity{}, ErrUnauthorized
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(rawKey)) != 1 {
		return Identity{}, ErrUnauthorized
	}
	return Identity{KeyID: "admin", Name: "admin"}, nil
}
