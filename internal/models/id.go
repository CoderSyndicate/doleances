package models

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// NewID returns a random identifier. It is a UUIDv4 in canonical form, taken
// from the cryptographic source because identifiers appear in permalinks and
// must not be guessable from one another.
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on any supported platform; if it ever
		// does, continuing with a predictable identifier would be worse.
		panic(fmt.Sprintf("models: cannot read random bytes: %v", err))
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10

	var out [36]byte
	hex.Encode(out[0:8], b[0:4])
	out[8] = '-'
	hex.Encode(out[9:13], b[4:6])
	out[13] = '-'
	hex.Encode(out[14:18], b[6:8])
	out[18] = '-'
	hex.Encode(out[19:23], b[8:10])
	out[23] = '-'
	hex.Encode(out[24:36], b[10:16])
	return string(out[:])
}
