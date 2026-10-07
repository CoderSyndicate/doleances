// Package token issues and verifies the secrets that stand in for accounts.
//
// A doléance contributor has no account: the token handed to them at
// submission is the only thing that proves a message is theirs, and the only
// way they can edit or delete it later. The same shape serves participant
// sessions.
//
// The site stores a hash and never the value. That is not a precaution against
// a database leak alone — it is what makes it true that the operator cannot
// edit or delete on a contributor's behalf, which the register promises and
// which a curator's own audit log depends on.
package token

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
)

// tokenBytes is the entropy behind one token. 32 bytes is far past guessing
// and still produces a string somebody can paste from an email.
const tokenBytes = 32

// codeDigits is how long a validation code is.
//
// Six, because it is typed by hand off a phone screen and a longer one is
// mistyped rather than read. Six digits is a million possibilities, which is
// only safe because of what surrounds it: a short expiry, a low attempt cap
// and a rate limit on issuing. Any of those three missing and this number is
// far too small — which is why they are not optional.
const codeDigits = 6

// Code returns a numeric validation code and the hash to store beside it.
//
// A code rather than a link, because this one proves an address answers before
// anything is created, and the person is already on the page that is waiting
// for it. A link would take them to a second tab to finish something they
// started in the first.
func Code() (value, hash string, err error) {
	digits := make([]byte, codeDigits)
	for i := range digits {
		n, err := rand.Int(rand.Reader, big.NewInt(10))
		if err != nil {
			return "", "", fmt.Errorf("token: read randomness: %w", err)
		}
		digits[i] = byte('0' + n.Int64())
	}

	value = string(digits)
	return value, Hash(value), nil
}

// NormaliseCode strips what people type around a code — spaces, dashes, the
// odd non-breaking space pasted out of an email — so a correct code is not
// refused for its punctuation.
func NormaliseCode(value string) string {
	var out strings.Builder
	for _, r := range value {
		if r >= '0' && r <= '9' {
			out.WriteRune(r)
		}
	}
	return out.String()
}

// New returns a fresh token and the hash to store beside the record.
//
// The token itself is returned once and never again: it is shown to the
// contributor at submission, and nothing in the system can recover it
// afterwards.
func New() (value, hash string, err error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("token: read random bytes: %w", err)
	}

	// URL-safe and unpadded, because a token travels in links and in emails
	// that wrap lines at awkward places.
	value = base64.RawURLEncoding.EncodeToString(raw)
	return value, Hash(value), nil
}

// Hash renders the stored form of a token.
func Hash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// Verify reports whether a presented token matches a stored hash.
//
// The comparison is constant-time. A byte-by-byte comparison of hashes leaks
// how much of a guess was right, and this is the check standing between a
// stranger and deleting somebody else's doléance.
func Verify(presented, storedHash string) bool {
	if presented == "" || storedHash == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(Hash(presented)), []byte(storedHash)) == 1
}

// Looks reports whether a string has the shape of a token at all. It is a
// cheap filter before a database lookup, never a substitute for Verify.
func Looks(value string) bool {
	if len(value) != base64.RawURLEncoding.EncodedLen(tokenBytes) {
		return false
	}
	return strings.IndexFunc(value, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') &&
			(r < '0' || r > '9') && r != '-' && r != '_'
	}) < 0
}

// recoveryAlphabet is what a recovery code is written in.
//
// Deliberately missing `I`, `L`, `O`, `U`, `0` and `1`: a recovery code is
// copied onto paper by hand and typed back months later, by somebody who has
// just lost their phone and is not in a good mood. Every character that can be
// read as another one is a code that fails for no reason. `U` goes too, so
// nothing in the set can accidentally spell a word worth a second look.
const recoveryAlphabet = "ABCDEFGHJKMNPQRSTVWXYZ23456789"

// recoveryGroups and recoveryGroupSize shape one code.
//
// Four groups of four, hyphenated: twenty characters of this alphabet is
// around 78 bits, which is far past guessing, and the grouping is what makes
// it transcribable at all. The hyphens are not stored — see NormaliseRecovery.
const (
	recoveryGroups    = 4
	recoveryGroupSize = 4
)

// Recovery returns one recovery code and the hash to store beside it.
//
// It exists because there is no address to send a reset to. Without it, losing
// the only device on an account is losing the account — and somebody who
// joined a group would simply cease to be that person, with no way back and
// nobody able to help them.
func Recovery() (value, hash string, err error) {
	var out strings.Builder
	for group := range recoveryGroups {
		if group > 0 {
			out.WriteByte('-')
		}
		for range recoveryGroupSize {
			n, err := rand.Int(rand.Reader, big.NewInt(int64(len(recoveryAlphabet))))
			if err != nil {
				return "", "", fmt.Errorf("token: read randomness: %w", err)
			}
			out.WriteByte(recoveryAlphabet[n.Int64()])
		}
	}

	value = out.String()
	return value, Hash(NormaliseRecovery(value)), nil
}

// NormaliseRecovery renders a typed recovery code in the form that was hashed.
//
// Case and grouping are presentation: somebody reading their own handwriting
// back will type lower case, put the hyphens somewhere else, or leave them
// out, and a code refused for that would be a code refused for nothing. The
// characters that cannot occur in the alphabet are dropped rather than
// rejected, so a stray space or a smart hyphen pasted out of a note costs
// nothing.
func NormaliseRecovery(value string) string {
	var out strings.Builder
	for _, r := range strings.ToUpper(value) {
		if strings.ContainsRune(recoveryAlphabet, r) {
			out.WriteRune(r)
		}
	}
	return out.String()
}

// HashRecovery renders the stored form of a presented recovery code.
//
// A code of the wrong length cannot be one this site issued, and is hashed
// anyway rather than refused early: the caller's one database lookup then
// takes the same time whatever was typed, and a wrong length is not a thing
// worth reporting differently from a wrong code.
func HashRecovery(value string) string {
	return Hash(NormaliseRecovery(value))
}
