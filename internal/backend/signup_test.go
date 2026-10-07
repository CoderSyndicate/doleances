package backend

import (
	"context"
	"strings"
	"testing"

	"github.com/CoderSyndicate/doleances/internal/passkey"
)

// TestAnAccountNeedsAName.
//
// The name is the only thing an account carries — no address, no password,
// nothing else — and it is what a group's member table shows and what a
// message to a group is signed with. An account without one is a row other
// people cannot address, and the member list beside it reads as a gap rather
// than a person.
//
// Enforced on the server rather than by the form's `required`, because the
// ceremony is reachable without the form. And checked *after* cleaning: a name
// of nothing but spaces, or one that sanitation empties, has to fail the same
// way as an empty field rather than creating an account called "".
func TestAnAccountNeedsAName(t *testing.T) {
	a := newAPI(t)
	ctx := context.Background()

	// With a relying party, or beginSignup answers 503 before it ever reads
	// the name and this test passes without testing anything — which is what
	// it did on the first attempt.
	service, err := passkey.New(passkey.Options{
		ID: "localhost", DisplayName: "doléances",
		Origins: []string{"http://localhost:5302"},
	})
	if err != nil {
		t.Fatalf("passkey.New: %v", err)
	}
	a.passkeys = service

	for _, name := range []string{"", "   ", "\u200b\u200b"} {
		in := &SignupBeginInput{}
		in.Body.Name = name
		_, err := a.beginSignup(ctx, in)
		if err == nil {
			t.Errorf("a signup with name %q was allowed to start", name)
			continue
		}
		// The refusal has to be about the name. Any other failure would pass
		// this test while leaving a nameless account perfectly creatable.
		if !strings.Contains(err.Error(), "needs a name") {
			t.Errorf("name %q was refused for the wrong reason: %v", name, err)
		}
	}

	in := &SignupBeginInput{}
	in.Body.Name = "Camille"
	if _, err := a.beginSignup(ctx, in); err != nil {
		t.Errorf("a named signup was refused: %v", err)
	}
}
