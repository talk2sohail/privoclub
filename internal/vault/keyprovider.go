package vault

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
)

// KeyProvider supplies the key-encryption key (KEK). Self-hosted installs use
// EnvKeyProvider; Privo Cloud will use a KMS-backed provider.
type KeyProvider interface {
	// Current returns the KEK used to wrap new DEKs.
	Current() []byte
	// Previous returns the prior KEK during rotation, or nil.
	Previous() []byte
}

type staticProvider struct{ current, previous []byte }

func (p staticProvider) Current() []byte  { return p.current }
func (p staticProvider) Previous() []byte { return p.previous }

// LoadEnvKeyProvider reads the master key from PRIVO_MASTER_KEY_FILE (preferred)
// or PRIVO_MASTER_KEY, and the optional PRIVO_MASTER_KEY_PREVIOUS[_FILE].
// Keys are 32 bytes, base64 encoded (standard or URL alphabet, padding optional).
// After loading, the plaintext variables are removed from the process
// environment so child processes never inherit them.
func LoadEnvKeyProvider() (KeyProvider, error) {
	cur, err := loadKey("PRIVO_MASTER_KEY")
	if err != nil {
		return nil, err
	}
	if cur == nil {
		return nil, errors.New("vault: master key missing: set PRIVO_MASTER_KEY_FILE (preferred) or PRIVO_MASTER_KEY")
	}
	prev, err := loadKey("PRIVO_MASTER_KEY_PREVIOUS")
	if err != nil {
		return nil, err
	}
	for _, name := range []string{"PRIVO_MASTER_KEY", "PRIVO_MASTER_KEY_PREVIOUS"} {
		_ = os.Unsetenv(name)
	}
	return staticProvider{current: cur, previous: prev}, nil
}

// StaticKeyProvider is for tests and tooling.
func StaticKeyProvider(current, previous []byte) KeyProvider {
	return staticProvider{current: current, previous: previous}
}

func loadKey(name string) ([]byte, error) {
	var raw string
	if path := os.Getenv(name + "_FILE"); path != "" {
		b, err := os.ReadFile(path) //nolint:gosec // operator-supplied path is the point
		if err != nil {
			return nil, fmt.Errorf("vault: reading %s_FILE: %w", name, err)
		}
		raw = string(b)
	} else {
		raw = os.Getenv(name)
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	key, err := DecodeKey(raw)
	if err != nil {
		return nil, fmt.Errorf("vault: %s: %w", name, err)
	}
	return key, nil
}

// DecodeKey parses a base64 key and checks its length.
func DecodeKey(s string) ([]byte, error) {
	s = strings.TrimRight(strings.TrimSpace(s), "=")
	for _, enc := range []*base64.Encoding{base64.RawStdEncoding, base64.RawURLEncoding} {
		if k, err := enc.DecodeString(s); err == nil {
			if len(k) != KeySize {
				return nil, fmt.Errorf("key must be %d bytes, got %d", KeySize, len(k))
			}
			return k, nil
		}
	}
	return nil, errors.New("key is not valid base64")
}
