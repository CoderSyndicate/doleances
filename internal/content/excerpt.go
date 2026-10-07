package content

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// DefaultExcerptRunes is how much of a doléance a card shows before it stops.
//
// A starting value, and configuration rather than a constant, because it is a
// judgement about a shape on a screen and not a fact: long enough that the
// shortest doléances are shown whole and the longer ones say something real,
// short enough that a card is a card. Measured against the corpus, the
// expected-accept cases run from 33 runes upward and the median sits near two
// hundred, so this shows most of them entire.
const DefaultExcerptRunes = 260

// Excerpt cuts a doléance down to a card, and reports whether it cut.
//
// # Why the register truncates at all
//
// Reluctantly. Somebody wrote their life and this shows three lines of it,
// which is the opposite of the care the rest of this project takes. It is done
// because the alternative is worse: a list where one entry is forty lines and
// the next is one cannot be scanned, and a register that cannot be scanned is
// a register nobody reads *alongside* the others — which is the entire point.
// The full text is one click away from every card, and the card says plainly
// that it has been cut.
//
// # How it cuts
//
//   - **Runes, never bytes.** Cutting a doléance mid-character would corrupt
//     the very languages this register exists to hear from.
//   - **At a word boundary**, searching backwards from the limit, so a card
//     never ends mid-word. A single word longer than the whole allowance is
//     cut hard rather than shown entire — otherwise one pathological token
//     would decide the shape of every card on the page.
//   - **Whitespace is collapsed.** A doléance may have paragraphs; a card has
//     one block, and ragged internal breaks are what make a grid of cards look
//     broken. This is the one change made to somebody's words here, it applies
//     to the excerpt only, and the page it links to shows the text as written.
//   - **Trailing punctuation is trimmed**, so the ellipsis follows a word
//     rather than a stranded comma.
func Excerpt(text string, limit int) (excerpt string, truncated bool) {
	if limit <= 0 {
		limit = DefaultExcerptRunes
	}

	flat := strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(flat) <= limit {
		return flat, false
	}

	// Walk to the limit in runes, remembering where the last word ended.
	var (
		count    int
		cut      int
		lastWord int
	)
	for offset, r := range flat {
		if count == limit {
			cut = offset
			break
		}
		if unicode.IsSpace(r) {
			lastWord = offset
		}
		count++
	}
	if cut == 0 {
		cut = len(flat)
	}

	// Back up to the word boundary, unless that would throw away most of the
	// allowance — one very long word should not empty the card.
	if lastWord > cut/2 {
		cut = lastWord
	}

	return strings.TrimRight(flat[:cut], " \t,;:-–—"), true
}
