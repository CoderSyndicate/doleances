package token

import (
	"strings"
	"testing"
)

func TestNewIssuesUniqueTokens(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		value, hash, err := New()
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if seen[value] {
			t.Fatal("New returned a token it had already issued")
		}
		seen[value] = true

		if !Looks(value) {
			t.Errorf("Looks(%q) = false for a freshly issued token", value)
		}
		if hash == value {
			t.Error("the stored hash is the token itself")
		}
		if len(hash) != 64 {
			t.Errorf("hash is %d characters, want 64", len(hash))
		}
	}
}

func TestVerify(t *testing.T) {
	value, hash, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if !Verify(value, hash) {
		t.Error("the issued token does not verify against its own hash")
	}
	if Verify(value+"x", hash) {
		t.Error("a modified token verified")
	}
	if Verify("", hash) || Verify(value, "") {
		t.Error("an empty value verified")
	}

	other, _, _ := New()
	if Verify(other, hash) {
		t.Error("a different token verified")
	}
}

// TestHashIsNotReversible is a guard on the promise the register makes: the
// site cannot act on a contributor's behalf, because it does not hold the
// token. If a hash ever contained its input, that promise would be false.
func TestHashIsNotReversible(t *testing.T) {
	value, hash, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if strings.Contains(hash, value) {
		t.Error("the hash contains the token")
	}
	// The first characters of a token must not survive into its hash either.
	if strings.Contains(hash, value[:8]) {
		t.Error("the hash leaks the start of the token")
	}
}

func TestLooksRejectsRubbish(t *testing.T) {
	for _, in := range []string{"", "short", strings.Repeat("a", 43) + "!", strings.Repeat("a", 100)} {
		if Looks(in) {
			t.Errorf("Looks(%q) = true, want false", in)
		}
	}
}

// TestRecoveryCodesAreTranscribable.
//
// A recovery code is the one credential in this project that somebody copies
// onto paper by hand and types back months later, having just lost their phone.
// Every character that can be read as another one is a code that fails for no
// reason, and a failure here is somebody locked out of their account for good.
func TestRecoveryCodesAreTranscribable(t *testing.T) {
	value, hash, err := Recovery()
	if err != nil {
		t.Fatalf("Recovery: %v", err)
	}

	// Four groups of four, hyphenated: twenty characters is far past guessing,
	// and the grouping is what makes it copyable at all.
	groups := strings.Split(value, "-")
	if len(groups) != 4 {
		t.Errorf("%q has %d groups, want 4", value, len(groups))
	}
	for _, group := range groups {
		if len(group) != 4 {
			t.Errorf("%q has a group of %d, want 4", value, len(group))
		}
	}

	// None of the characters that can be misread.
	for _, confusable := range []string{"I", "L", "O", "U", "0", "1"} {
		if strings.Contains(value, confusable) {
			t.Errorf("%q contains %q, which is misread as something else", value, confusable)
		}
	}

	if !strings.EqualFold(hash, HashRecovery(value)) {
		t.Error("a freshly issued code does not hash to its own stored form")
	}
}

// TestARecoveryCodeSurvivesBeingTypedBack.
//
// Case and grouping are presentation. Somebody reading their own handwriting
// will type lower case, put the hyphens somewhere else, or leave them out, and a
// code refused for that would be a code refused for nothing.
func TestARecoveryCodeSurvivesBeingTypedBack(t *testing.T) {
	const written = "ABCD-EFGH-JKMN-PQRS"
	stored := HashRecovery(written)

	for _, typed := range []string{
		"ABCD-EFGH-JKMN-PQRS",
		"abcd-efgh-jkmn-pqrs",
		"ABCDEFGHJKMNPQRS",
		"ABCD EFGH JKMN PQRS",
		"  abcd-EFGH jkmn–PQRS  ", // a stray space and an en dash pasted from a note
	} {
		if HashRecovery(typed) != stored {
			t.Errorf("HashRecovery(%q) does not match the code as written", typed)
		}
	}

	// And a different code is a different code.
	if HashRecovery("ABCD-EFGH-JKMN-PQRT") == stored {
		t.Error("two different codes hash the same")
	}
}

// TestRecoveryCodesDoNotRepeat. Ten are issued at once and a collision inside
// one sheet would silently give somebody nine.
func TestRecoveryCodesDoNotRepeat(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		value, _, err := Recovery()
		if err != nil {
			t.Fatalf("Recovery: %v", err)
		}
		if seen[value] {
			t.Fatalf("Recovery issued %q twice", value)
		}
		seen[value] = true
	}
}
