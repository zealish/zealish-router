package auth

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"time"

	"github.com/zealish/zealish-router/internal/storage"
)

// KeyPrefix marks generated gateway credentials.
const KeyPrefix = "zr_"

// secretBytes is the entropy behind a generated key.
const secretBytes = 32

const base62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// GeneratedKey pairs the raw credential with the record to persist. The raw
// value exists only here: it is shown to the operator once and never stored.
type GeneratedKey struct {
	Raw    string
	Record storage.APIKey
}

// GenerateKey builds a new credential named name. Only its hash is persisted,
// so the caller must surface Raw immediately or lose it.
func GenerateKey(name string) (GeneratedKey, error) {
	secret, err := randomBase62(secretBytes)
	if err != nil {
		return GeneratedKey{}, err
	}
	id, err := randomBase62(12)
	if err != nil {
		return GeneratedKey{}, err
	}

	raw := KeyPrefix + secret
	return GeneratedKey{
		Raw: raw,
		Record: storage.APIKey{
			ID:        id,
			Name:      name,
			KeyHash:   Hash(raw),
			Enabled:   true,
			CreatedAt: time.Now().UTC(),
		},
	}, nil
}

// randomBase62 returns n cryptographically random base62 characters.
func randomBase62(n int) (string, error) {
	limit := big.NewInt(int64(len(base62)))

	out := make([]byte, n)
	for i := range out {
		idx, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", fmt.Errorf("auth: generate key: %w", err)
		}
		out[i] = base62[idx.Int64()]
	}
	return string(out), nil
}
