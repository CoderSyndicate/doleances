package harness

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestTheFixturesAreUsable is the cheap guard that stops --seeding failing at
// the one moment somebody wanted to see the thing working.
func TestTheFixturesAreUsable(t *testing.T) {
	var file seedFile
	if err := json.Unmarshal(fixtures, &file); err != nil {
		t.Fatalf("the fixtures do not parse: %v", err)
	}

	if len(file.Messages) == 0 || len(file.Groups) == 0 {
		t.Fatal("the fixtures are empty")
	}

	for _, message := range file.Messages {
		if strings.TrimSpace(message.Text) == "" {
			t.Error("a doléance with no text")
		}
		// Below a hundred runes every submission goes to a curator whatever it
		// scored, so a file of one-liners would seed a register that looks
		// empty and a queue that looks alarming. One short one is deliberate
		// — the register must accept "le bus ne passe plus le dimanche" — and
		// more than that would be an accident.
		if utf8.RuneCountInString(message.Text) < 100 {
			t.Logf("short doléance, will go to curation: %q", message.Text)
		}
	}

	names := map[string]bool{}
	for _, group := range file.Groups {
		if group.Name == "" {
			t.Error("a group with no name")
		}
		if names[group.Name] {
			t.Errorf("two groups are called %q; the second would be refused", group.Name)
		}
		names[group.Name] = true

		for _, action := range group.Actions {
			if action.Title == "" {
				t.Errorf("an action of %q has no title", group.Name)
			}
			switch action.Type {
			case "onetime":
				if action.InDays <= 0 {
					t.Errorf("%q is one-off and would be seeded in the past",
						action.Title)
				}
			case "recurrent":
				if action.Repeat == "" {
					t.Errorf("%q recurs but says nothing about how", action.Title)
				}
			default:
				t.Errorf("%q is neither one-off nor recurring", action.Title)
			}
		}
	}
}
