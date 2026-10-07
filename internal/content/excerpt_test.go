package content

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestAShortDoleanceIsShownWhole. "Le bus ne passe plus le dimanche" is a
// complete grievance, and cutting it would be absurd.
func TestAShortDoleanceIsShownWhole(t *testing.T) {
	const short = "Le bus ne passe plus le dimanche."

	excerpt, truncated := Excerpt(short, 260)
	if truncated {
		t.Error("a short doléance was reported as cut")
	}
	if excerpt != short {
		t.Errorf("excerpt = %q, want the text unchanged", excerpt)
	}
}

// TestItCutsAtAWordBoundary. A card that ends mid-word reads as broken rather
// than as continued.
func TestItCutsAtAWordBoundary(t *testing.T) {
	text := strings.Repeat("maternité fermée ", 40)

	excerpt, truncated := Excerpt(text, 60)
	if !truncated {
		t.Fatal("a long doléance was not cut")
	}
	if strings.HasSuffix(excerpt, "mater") || strings.HasSuffix(excerpt, "ferm") {
		t.Errorf("excerpt ends mid-word: %q", excerpt)
	}
	if utf8.RuneCountInString(excerpt) > 60 {
		t.Errorf("excerpt is %d runes, over the limit", utf8.RuneCountInString(excerpt))
	}
}

// TestItCountsRunesNotBytes is the property that protects the languages this
// register exists to hear from: a byte cut lands inside a character.
func TestItCountsRunesNotBytes(t *testing.T) {
	// Every one of these is multi-byte, and none is a space, so the cut has
	// nowhere to hide behind a word boundary.
	text := strings.Repeat("Тбилиси", 40)

	excerpt, truncated := Excerpt(text, 50)
	if !truncated {
		t.Fatal("a long doléance was not cut")
	}
	if !utf8.ValidString(excerpt) {
		t.Fatal("the cut produced invalid UTF-8")
	}
	if got := utf8.RuneCountInString(excerpt); got != 50 {
		t.Errorf("excerpt is %d runes, want exactly the limit", got)
	}
}

// TestOneEnormousWordDoesNotEmptyTheCard. Backing up to a word boundary is
// right until there is no boundary worth backing up to.
func TestOneEnormousWordDoesNotEmptyTheCard(t *testing.T) {
	text := "Le " + strings.Repeat("a", 500)

	excerpt, truncated := Excerpt(text, 100)
	if !truncated {
		t.Fatal("a long doléance was not cut")
	}
	if utf8.RuneCountInString(excerpt) < 50 {
		t.Errorf("excerpt collapsed to %q — one long word emptied the card", excerpt)
	}
}

// TestParagraphsBecomeOneBlock. A card is one block; ragged internal breaks
// are what make a grid look broken.
func TestParagraphsBecomeOneBlock(t *testing.T) {
	text := "La maternité a fermé.\n\n\tIl faut faire\n cinquante kilomètres."

	excerpt, _ := Excerpt(text, 260)
	if strings.ContainsAny(excerpt, "\n\t") {
		t.Errorf("excerpt kept its line breaks: %q", excerpt)
	}
	if strings.Contains(excerpt, "  ") {
		t.Errorf("excerpt kept a run of spaces: %q", excerpt)
	}
	if !strings.Contains(excerpt, "fermé. Il faut") {
		t.Errorf("excerpt = %q, want the paragraphs joined by one space", excerpt)
	}
}

// TestTheEllipsisFollowsAWord, not a stranded comma.
func TestTheEllipsisFollowsAWord(t *testing.T) {
	text := "un, deux, trois, quatre, cinq, six, sept, huit, neuf, dix, onze, douze"

	for limit := 10; limit < 60; limit++ {
		excerpt, truncated := Excerpt(text, limit)
		if !truncated {
			continue
		}
		if strings.HasSuffix(excerpt, ",") || strings.HasSuffix(excerpt, " ") {
			t.Errorf("limit %d left a dangling %q: %q",
				limit, excerpt[len(excerpt)-1:], excerpt)
		}
	}
}

// TestAnAbsentLimitFallsBackRatherThanCuttingToNothing. A setting that was
// never written reads as zero, and zero here would show an empty card.
func TestAnAbsentLimitFallsBackRatherThanCuttingToNothing(t *testing.T) {
	text := strings.Repeat("mot ", 200)

	for _, limit := range []int{0, -1} {
		excerpt, _ := Excerpt(text, limit)
		if got := utf8.RuneCountInString(excerpt); got == 0 || got > DefaultExcerptRunes {
			t.Errorf("limit %d produced %d runes", limit, got)
		}
	}
}
