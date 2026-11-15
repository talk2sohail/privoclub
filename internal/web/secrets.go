package web

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"regexp"
)

// setupPrefix starts every setup token, so a leaked one is easy to recognise
// and to scan for.
const setupPrefix = "privo_setup_"

// invitePrefix starts every invite token (ADR 0017).
const invitePrefix = "privo_invite_"

// secretLen is the length of a random secret: 32 bytes, base64url, no padding.
const secretLen = 43

var (
	secretRe      = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
	setupTokenRe  = regexp.MustCompile(`^privo_setup_[A-Za-z0-9_-]{43}$`)
	inviteTokenRe = regexp.MustCompile(`^privo_invite_[A-Za-z0-9_-]{43}$`)
)

// randomSecret returns 256 random bits as 43 URL-safe characters.
func randomSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HashSecret is the hex SHA-256 of a secret. Session cookies, setup tokens,
// invite tokens and connect states are stored only as this hash: a copy of the database cannot be
// used to sign in.
func HashSecret(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// NewSetupToken returns a setup token to show once and the hash to store.
func NewSetupToken() (plain, hash string, err error) {
	s, err := randomSecret()
	if err != nil {
		return "", "", err
	}
	plain = setupPrefix + s
	return plain, HashSecret(plain), nil
}

// validSetupToken reports whether s has the exact shape of a setup token, so
// nothing else is ever looked up, logged or echoed.
func validSetupToken(s string) bool { return setupTokenRe.MatchString(s) }

// NewInviteToken returns an invite token to show once and the hash to store.
func NewInviteToken() (plain, hash string, err error) {
	s, err := randomSecret()
	if err != nil {
		return "", "", err
	}
	plain = invitePrefix + s
	return plain, HashSecret(plain), nil
}

func validInviteToken(s string) bool { return inviteTokenRe.MatchString(s) }

// validSecret reports whether s has the shape of a session cookie or state.
func validSecret(s string) bool { return len(s) == secretLen && secretRe.MatchString(s) }

// stateHash keys a connection in flight to both the value sent to the
// authorization server and the nonce in the browser's cookie. A callback from
// a browser without the cookie finds nothing, and does not use up the real
// state either.
func stateHash(state, nonce string) string {
	return HashSecret("privo-oauth-state\x00" + state + "\x00" + nonce)
}
