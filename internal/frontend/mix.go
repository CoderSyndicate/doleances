package frontend

// How the register fills a page.
//
// # The rules, and what each is for
//
// The register shows registerSlots entries at a time. Most are doléances;
// a few are passages from 1789, mixed in rather than filed in a section of
// their own — because the claim this whole project rests on is that the two
// are one act performed twice, and a claim is better shown than asserted.
//
// Two of the three numbers are reservations, and both exist to stop a
// majority crowding out something the register needs to keep visible:
//
//   - at most one passage in every voiceShare entries, so the corpus seasons
//     the register rather than becoming it;
//   - unplacedSlots of the doléance slots kept for the ones that named a
//     country or a region and sit on no point of the map.
//
// That second reservation is what retired a control. The register used to
// carry a button reading "see everything, even without a place", which was
// there to undo a filter the reader never chose: panning the map silently
// excluded every doléance that had no point, and the only way back was to
// notice the button. Keeping them a few slots means they are always there,
// whatever the map is showing, and there is nothing left for the button to do.
//
// Both reservations are ceilings, not quotas. Fewer passages than the share
// allows is fine; fewer unplaced doléances than the slots allow simply means
// located ones take the rest. And when the register is young and has fewer
// doléances than its slots, the passages fill what is left rather than
// leaving a short page — which is the one case where the corpus is most of
// what there is to read, and rightly so.
const (
	registerSlots = 40
	voiceShare    = 10
	unplacedSlots = 10
)

// maxVoices is the ceiling the share implies.
func maxVoices() int { return registerSlots / voiceShare }

// messageSlots is what is left for doléances once the passages have theirs.
func messageSlots() int { return registerSlots - maxVoices() }

// placedSlots is how many of those go to doléances with a point on the map.
func placedSlots() int { return messageSlots() - unplacedSlots }

// wantedVoices says how many passages a page of this many doléances takes.
//
// The share is a ceiling while there are doléances to fill the page, and a
// floor-filler when there are not: a register holding four doléances shows
// them and as much of the corpus as the page has room for, rather than four
// cards and a lot of white space.
func wantedVoices(messages, available int) int {
	want := maxVoices()
	if short := registerSlots - messages; short > want {
		want = short
	}
	if want > available {
		want = available
	}
	if want < 0 {
		return 0
	}
	return want
}

// voiceSlot is the position of the jth passage among n entries holding k of
// them — the midpoint of its share, so they land spread out rather than
// bunched at one end.
//
// With the defaults that is entries 6, 16, 26 and 36 of forty: one in each
// group of ten, near the middle, where a reader scrolling past meets one
// without ever meeting two together.
func voiceSlot(j, n, k int) int {
	if k <= 0 {
		return -1
	}
	return (2*j + 1) * n / (2 * k)
}

// interleave lays the passages through the doléances at those positions.
//
// It is generic over the entry type so the rule can be tested on something
// simpler than a card, and so the same arithmetic serves the page the server
// renders and the list the map rebuilds.
func interleave[T any](messages, voices []T) []T {
	total := len(messages) + len(voices)
	out := make([]T, 0, total)

	nextVoice, nextMessage := 0, 0
	for slot := 0; slot < total; slot++ {
		if nextVoice < len(voices) && slot == voiceSlot(nextVoice, total, len(voices)) {
			out = append(out, voices[nextVoice])
			nextVoice++
			continue
		}
		if nextMessage < len(messages) {
			out = append(out, messages[nextMessage])
			nextMessage++
			continue
		}
		// The doléances ran out before the slots did, which happens when the
		// arithmetic puts a passage's position past where the messages reach.
		// Whatever is left is a passage.
		if nextVoice < len(voices) {
			out = append(out, voices[nextVoice])
			nextVoice++
		}
	}
	return out
}

// fill takes the unplaced doléances first, up to their reservation, and lets
// the located ones have whatever is left.
//
// The order is deliberate: the reservation only means anything if the ones it
// is for are taken before the ones it is protecting them from.
func fill[T any](placed, unplaced []T) []T {
	keep := unplaced
	if len(keep) > unplacedSlots {
		keep = keep[:unplacedSlots]
	}

	room := messageSlots() - len(keep)
	if room > len(placed) {
		room = len(placed)
	}
	if room < 0 {
		room = 0
	}

	out := make([]T, 0, len(keep)+room)
	out = append(out, placed[:room]...)
	out = append(out, keep...)
	return out
}
