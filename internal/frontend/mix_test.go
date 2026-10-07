package frontend

import (
	"strings"
	"testing"
)

// seq builds n entries labelled with a prefix, so a mixed page can be read as
// a string and the shape checked at a glance.
func seq(prefix string, n int) []string {
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, prefix)
	}
	return out
}

func shape(entries []string) string { return strings.Join(entries, "") }

// TestAFullPageIsOnePassageInEveryTen.
//
// The share is the whole reason passages are mixed in rather than filed in a
// section: enough that a reader meets 1789 while reading today, few enough
// that the register is still a register.
func TestAFullPageIsOnePassageInEveryTen(t *testing.T) {
	messages := seq("m", messageSlots())
	voices := seq("v", wantedVoices(len(messages), 50))

	if len(voices) != 4 {
		t.Fatalf("a full page took %d passages, want 4", len(voices))
	}

	page := interleave(messages, voices)
	if len(page) != registerSlots {
		t.Fatalf("page holds %d entries, want %d", len(page), registerSlots)
	}

	// One in each group of ten, and never two together.
	for group := 0; group < registerSlots/voiceShare; group++ {
		block := page[group*voiceShare : (group+1)*voiceShare]
		if n := strings.Count(shape(block), "v"); n != 1 {
			t.Errorf("group %d holds %d passages, want exactly 1: %s", group+1, n, shape(block))
		}
	}
	if strings.Contains(shape(page), "vv") {
		t.Errorf("two passages landed side by side: %s", shape(page))
	}
}

// TestAYoungRegisterIsFilledWithThePast.
//
// The share is a ceiling while there is enough to read and a floor-filler
// when there is not. On the first day this register holds almost nothing, and
// four doléances above a lot of white space would say the opposite of what
// the page is for.
func TestAYoungRegisterIsFilledWithThePast(t *testing.T) {
	for _, written := range []int{0, 1, 4, 20, 35} {
		messages := seq("m", written)
		voices := seq("v", wantedVoices(len(messages), 100))

		page := interleave(messages, voices)
		if len(page) != registerSlots {
			t.Errorf("%d doléances made a page of %d, want it filled to %d",
				written, len(page), registerSlots)
		}
		if got := strings.Count(shape(page), "m"); got != written {
			t.Errorf("%d doléances but %d on the page", written, got)
		}
	}
}

// TestThePastNeverOutgrowsItsShareWhileThereIsEnoughToRead.
func TestThePastNeverOutgrowsItsShareWhileThereIsEnoughToRead(t *testing.T) {
	if got := wantedVoices(messageSlots(), 500); got != maxVoices() {
		t.Errorf("a full page took %d passages from a corpus of 500, want %d", got, maxVoices())
	}
	// And a corpus smaller than the share is not padded out of nothing.
	if got := wantedVoices(messageSlots(), 2); got != 2 {
		t.Errorf("took %d passages from a corpus of 2", got)
	}
	if got := wantedVoices(messageSlots(), 0); got != 0 {
		t.Errorf("took %d passages from an empty corpus", got)
	}
}

// TestTheUnplacedKeepTheirSlots.
//
// This is the reservation that retired the "see everything, even without a
// place" button. A doléance that named only a country cannot answer a map
// viewport, so without slots of its own it is simply missing from the page
// whenever the reader has moved the map — which they always have.
func TestTheUnplacedKeepTheirSlots(t *testing.T) {
	// Far more located doléances than the page can hold, which is the case
	// that would crowd the others out.
	page := fill(seq("p", 200), seq("u", 50))

	if len(page) != messageSlots() {
		t.Fatalf("page holds %d doléances, want %d", len(page), messageSlots())
	}
	if got := strings.Count(shape(page), "u"); got != unplacedSlots {
		t.Errorf("%d unplaced doléances kept their slot, want %d", got, unplacedSlots)
	}
	if got := strings.Count(shape(page), "p"); got != placedSlots() {
		t.Errorf("%d located doléances, want %d", got, placedSlots())
	}
}

// TestTheReservationIsACeilingNotAQuota.
//
// A register with few unplaced doléances should not hold empty slots open for
// them — the located ones take the room.
func TestTheReservationIsACeilingNotAQuota(t *testing.T) {
	page := fill(seq("p", 200), seq("u", 3))

	if len(page) != messageSlots() {
		t.Fatalf("page holds %d, want %d", len(page), messageSlots())
	}
	if got := strings.Count(shape(page), "u"); got != 3 {
		t.Errorf("%d unplaced shown, want the 3 there are", got)
	}
	if got := strings.Count(shape(page), "p"); got != messageSlots()-3 {
		t.Errorf("%d located shown, want %d", got, messageSlots()-3)
	}

	// And none at all is simply a page of located ones.
	page = fill(seq("p", 200), nil)
	if got := strings.Count(shape(page), "p"); got != messageSlots() {
		t.Errorf("with no unplaced, %d located shown, want %d", got, messageSlots())
	}
}

// TestAnEmptyRegisterIsNotAPanic, which is the state every installation
// starts in.
func TestAnEmptyRegisterIsNotAPanic(t *testing.T) {
	if page := interleave[string](nil, nil); len(page) != 0 {
		t.Errorf("an empty register produced %d entries", len(page))
	}
	if page := fill[string](nil, nil); len(page) != 0 {
		t.Errorf("an empty register produced %d doléances", len(page))
	}
	if got := wantedVoices(0, 0); got != 0 {
		t.Errorf("wantedVoices on nothing = %d", got)
	}
}
