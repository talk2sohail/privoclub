// Package token creates and parses personal access tokens (ADR 0003).
//
// Format: privo_pat_<id>_<secret><crc>
//
//	id      16 chars, lowercase base32: indexed lookup key, safe to log
//	secret  52 chars, lowercase base32 of 32 random bytes
//	crc     8 hex chars, CRC-32 of everything before it, so secret scanners
//	        and Privo can reject mistyped or fabricated tokens without a lookup
//
// Only SHA-256(secret) is stored. SHA-256 is sufficient for 256-bit random
// secrets; a slow password hash would add latency to every request for no gain.
package token

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"strings"
)

// Prefix identifies Privo personal access tokens.
const Prefix = "privo_pat_"

const (
	idLen     = 16
	secretLen = 52
	crcLen    = 8
)

var enc = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// ErrMalformed is returned for strings that are not well-formed Privo tokens.
var ErrMalformed = errors.New("token: malformed")

// ErrWeak is returned for a well-formed token whose secret is too regular to
// have been generated at random.
var ErrWeak = errors.New("token: the secret is too regular to be random; make one with `privo token generate`")

// Token is a freshly generated token. Plain is shown to the user once.
type Token struct {
	ID         string
	Plain      string
	SecretHash string // hex SHA-256 of the secret, for storage
}

// New generates a token.
func New() (Token, error) {
	idBytes := make([]byte, 10) // 80 bits -> 16 base32 chars
	secret := make([]byte, 32)  // 256 bits -> 52 base32 chars
	if _, err := rand.Read(idBytes); err != nil {
		return Token{}, fmt.Errorf("token: %w", err)
	}
	if _, err := rand.Read(secret); err != nil {
		return Token{}, fmt.Errorf("token: %w", err)
	}
	id, sec := enc.EncodeToString(idBytes), enc.EncodeToString(secret)
	body := Prefix + id + "_" + sec
	return Token{
		ID:         id,
		Plain:      body + checksum(body),
		SecretHash: HashSecret(sec),
	}, nil
}

// Parse splits a token into its id and secret after checking its shape and checksum.
func Parse(plain string) (id, secret string, err error) {
	if !strings.HasPrefix(plain, Prefix) || len(plain) != len(Prefix)+idLen+1+secretLen+crcLen {
		return "", "", ErrMalformed
	}
	body, crc := plain[:len(plain)-crcLen], plain[len(plain)-crcLen:]
	if subtle.ConstantTimeCompare([]byte(checksum(body)), []byte(crc)) != 1 {
		return "", "", ErrMalformed
	}
	rest := body[len(Prefix):]
	if rest[idLen] != '_' {
		return "", "", ErrMalformed
	}
	id, secret = rest[:idLen], rest[idLen+1:]
	if !valid(id) || !valid(secret) {
		return "", "", ErrMalformed
	}
	return id, secret, nil
}

// The checks in Weak. A secret from New has about 26 different symbols of 32;
// fewer than 16, a run of 8, or 16 steps along the alphabet happen to a random
// one far less than once in a million million times.
const (
	weakMinDistinct = 16
	weakRun         = 8
	weakSteps       = 16
	symbolOrder     = "abcdefghijklmnopqrstuvwxyz234567"
)

// Weak reports whether a secret looks hand-made: too few different symbols,
// a long run of one symbol, or a long walk along the alphabet in order. It is a
// cheap guard against the careless case (a secret of repeated characters on a
// token that grants owner access), not a proof of randomness. A secret from New
// is never weak in practice.
func Weak(secret string) bool {
	if secret == "" {
		return true
	}
	var seen [256]bool
	distinct, run, steps := 0, 0, 0
	for i := 0; i < len(secret); i++ {
		c := secret[i]
		if !seen[c] {
			seen[c] = true
			distinct++
		}
		if i == 0 {
			run = 1
			continue
		}
		prev := secret[i-1]
		if c == prev {
			if run++; run >= weakRun {
				return true
			}
		} else {
			run = 1
		}
		a, b := strings.IndexByte(symbolOrder, prev), strings.IndexByte(symbolOrder, c)
		if a >= 0 && b >= 0 && (b-a == 1 || a-b == 1) {
			if steps++; steps >= weakSteps {
				return true
			}
		}
	}
	return distinct < weakMinDistinct
}

// HashSecret returns the hex SHA-256 of a secret.
func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// Verify reports whether secret matches the stored hash, in constant time.
func Verify(secret, storedHash string) bool {
	return subtle.ConstantTimeCompare([]byte(HashSecret(secret)), []byte(storedHash)) == 1
}

func checksum(body string) string {
	return fmt.Sprintf("%08x", crc32.ChecksumIEEE([]byte(body)))
}

func valid(s string) bool {
	for _, c := range s {
		if (c < 'a' || c > 'z') && (c < '2' || c > '7') {
			return false
		}
	}
	return true
}
