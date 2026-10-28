// Package vault implements envelope encryption for credentials (ADR 0003).
//
// Each organisation has a data key (DEK). Every credential is sealed with
// AES-256-GCM under a subkey derived from the DEK with HKDF over the
// credential id and version. Associated data binds the ciphertext to its org,
// credential, owner, server, purpose, key version and row version, so a
// ciphertext moved to another row, owner or older version fails to open.
// DEKs are stored wrapped by the key-encryption key (KEK) from a KeyProvider,
// with the org id as associated data.
package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// KeySize is the size of KEKs and DEKs in bytes.
const KeySize = 32

const formatV1 byte = 1

// ErrDecrypt is returned for any failure to open a sealed value. It never says
// why, so it cannot be used as an oracle.
var ErrDecrypt = errors.New("vault: cannot decrypt")

// Binding is the context a credential ciphertext is bound to.
type Binding struct {
	OrgID        string
	CredentialID string
	OwnerID      string // user id, or "" for an org-shared credential
	ServerID     string
	Purpose      string // e.g. "env", "header", "oauth_refresh"
	KeyVersion   uint32 // version of the org's DEK
	RowVersion   uint64 // incremented on every write of the credential
}

// maxField bounds each binding field; ids are far shorter.
const maxField = 1024

func (b Binding) aad() ([]byte, error) {
	// Length-prefixed fields: unambiguous, no separator injection.
	var out []byte
	for _, s := range []string{"privo/credential/v1", b.OrgID, b.CredentialID, b.OwnerID, b.ServerID, b.Purpose} {
		n := len(s)
		if n > maxField {
			return nil, errors.New("vault: binding field too long")
		}
		out = binary.BigEndian.AppendUint16(out, uint16(n)) //nolint:gosec // n <= maxField, checked above
		out = append(out, s...)
	}
	out = binary.BigEndian.AppendUint32(out, b.KeyVersion)
	out = binary.BigEndian.AppendUint64(out, b.RowVersion)
	return out, nil
}

// NewKey returns a fresh random 256-bit key.
func NewKey() ([]byte, error) {
	k := make([]byte, KeySize)
	if _, err := io.ReadFull(rand.Reader, k); err != nil {
		return nil, fmt.Errorf("vault: generating key: %w", err)
	}
	return k, nil
}

func seal(key, plaintext, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	out := make([]byte, 0, 1+len(nonce)+len(plaintext)+gcm.Overhead())
	out = append(out, formatV1)
	out = append(out, nonce...)
	return gcm.Seal(out, nonce, plaintext, aad), nil
}

func open(key, sealed, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrDecrypt
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrDecrypt
	}
	if len(sealed) < 1+gcm.NonceSize()+gcm.Overhead() || sealed[0] != formatV1 {
		return nil, ErrDecrypt
	}
	pt, err := gcm.Open(nil, sealed[1:1+gcm.NonceSize()], sealed[1+gcm.NonceSize():], aad)
	if err != nil {
		return nil, ErrDecrypt
	}
	return pt, nil
}

func dekAAD(orgID string, version uint32) []byte {
	return binary.BigEndian.AppendUint32([]byte("privo/dek/v1\x00"+orgID+"\x00"), version)
}

// WrapDEK seals an org's data key under the KEK, bound to the org and key version.
func WrapDEK(kek, dek []byte, orgID string, version uint32) ([]byte, error) {
	if len(kek) != KeySize || len(dek) != KeySize {
		return nil, errors.New("vault: keys must be 32 bytes")
	}
	return seal(kek, dek, dekAAD(orgID, version))
}

// UnwrapDEK opens an org's data key.
func UnwrapDEK(kek, wrapped []byte, orgID string, version uint32) ([]byte, error) {
	if len(kek) != KeySize {
		return nil, ErrDecrypt
	}
	dek, err := open(kek, wrapped, dekAAD(orgID, version))
	if err != nil || len(dek) != KeySize {
		return nil, ErrDecrypt
	}
	return dek, nil
}

func subkey(dek []byte, b Binding) ([]byte, error) {
	info := make([]byte, 0, 64)
	info = append(info, "privo/credential-key/v1\x00"...)
	info = append(info, b.CredentialID...)
	info = binary.BigEndian.AppendUint64(info, b.RowVersion)
	return hkdf.Key(sha256.New, dek, nil, string(info), KeySize)
}

// Seal encrypts a credential under the org DEK with its binding.
func Seal(dek, plaintext []byte, b Binding) ([]byte, error) {
	if len(dek) != KeySize {
		return nil, errors.New("vault: DEK must be 32 bytes")
	}
	if b.OrgID == "" || b.CredentialID == "" {
		return nil, errors.New("vault: binding needs org and credential ids")
	}
	aad, err := b.aad()
	if err != nil {
		return nil, err
	}
	k, err := subkey(dek, b)
	if err != nil {
		return nil, err
	}
	return seal(k, plaintext, aad)
}

// Open decrypts a credential sealed with the same DEK and binding.
func Open(dek, sealed []byte, b Binding) ([]byte, error) {
	if len(dek) != KeySize {
		return nil, ErrDecrypt
	}
	aad, err := b.aad()
	if err != nil {
		return nil, ErrDecrypt
	}
	k, err := subkey(dek, b)
	if err != nil {
		return nil, ErrDecrypt
	}
	return open(k, sealed, aad)
}
