// Package ids generates UUIDv7 identifiers (time-ordered, RFC 9562).
package ids

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"time"
)

// New returns a new UUIDv7 string.
func New() string {
	var b [16]byte
	ms := uint64(time.Now().UnixMilli()) //nolint:gosec // time after 1970
	binary.BigEndian.PutUint64(b[:8], ms<<16)
	if _, err := rand.Read(b[6:]); err != nil {
		panic("ids: crypto/rand failed: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x70 // version 7
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 9562 variant
	var out [36]byte
	hex.Encode(out[0:8], b[0:4])
	out[8] = '-'
	hex.Encode(out[9:13], b[4:6])
	out[13] = '-'
	hex.Encode(out[14:18], b[6:8])
	out[18] = '-'
	hex.Encode(out[19:23], b[8:10])
	out[23] = '-'
	hex.Encode(out[24:], b[10:])
	return string(out[:])
}
