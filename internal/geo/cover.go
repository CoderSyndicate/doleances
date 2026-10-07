package geo

import (
	"math"
	"sort"

	"github.com/mmcloughlin/geohash"
	"github.com/rs/zerolog/log"
)

// MaxCoverCells bounds how many prefixes a viewport is turned into.
//
// Every cell becomes one condition in the query, so this is the trade: more
// cells hug the viewport more tightly and return fewer rows to discard, but a
// query with hundreds of OR'd conditions costs more to plan than the rows it
// saves. Forty is comfortably inside what either engine plans well and still
// tracks a viewport closely.
const MaxCoverCells = 40

// Box is a map viewport.
type Box struct {
	North, South float64
	East, West   float64
}

// CrossesDateLine reports whether the box spans the antimeridian, which Leaflet
// reports as a west edge greater than the east one. A query built without
// noticing returns nothing at all for anybody looking at the Pacific.
func (b Box) CrossesDateLine() bool { return b.West > b.East }

// Cover turns a viewport into the geohash prefixes that cover it.
//
// This is what lets a viewport be answered by the same index as everything
// else: a prefix is a range scan on location_geohash, where a pair of
// latitude/longitude comparisons is two half-open ranges the planner has to
// intersect. The cells always cover at least the box — never less — so the
// answer may include rows just outside the edge, which the caller filters or
// accepts as the cost of a cheap query.
func Cover(box Box) []string {
	// A box straddling the date line is two boxes. Covering it as one would
	// span the entire globe the long way round.
	if box.CrossesDateLine() {
		east := Box{North: box.North, South: box.South, West: box.West, East: 180}
		west := Box{North: box.North, South: box.South, West: -180, East: box.East}
		return dedupe(append(Cover(east), Cover(west)...))
	}

	precision := precisionForBox(box)
	if precision == 0 {
		// The viewport is the world; no prefix narrows anything, and saying so
		// is better than returning a cover that pretends to.
		log.Debug().
			Float64("north", box.North).Float64("south", box.South).
			Float64("east", box.East).Float64("west", box.West).
			Msg("geo: viewport too wide to narrow; no cell filter applied")
		return nil
	}

	cells := dedupe(walk(box, precision))
	log.Debug().
		Float64("north", box.North).Float64("south", box.South).
		Float64("east", box.East).Float64("west", box.West).
		Uint("precision", precision).
		Int("cells", len(cells)).
		Strs("sample", sample(cells, 6)).
		Msg("geo: viewport covered")
	return cells
}

// precisionForBox picks the finest precision whose cells still cover the box
// within the cell budget.
func precisionForBox(box Box) uint {
	for precision := uint(StoredPrecision); precision >= 1; precision-- {
		latStep, lngStep := cellSize(box, precision)
		if latStep == 0 || lngStep == 0 {
			continue
		}

		rows := math.Ceil((box.North-box.South)/latStep) + 1
		cols := math.Ceil((box.East-box.West)/lngStep) + 1
		if rows*cols <= float64(MaxCoverCells) {
			return precision
		}
	}
	return 0
}

// cellSize measures a cell at a precision, in degrees, by asking the library
// for the bounding box of a real cell rather than hard-coding a table that
// would drift from it.
func cellSize(box Box, precision uint) (lat, lng float64) {
	centre := geohash.EncodeWithPrecision(
		(box.North+box.South)/2, (box.East+box.West)/2, precision)
	bounds := geohash.BoundingBox(centre)
	return bounds.MaxLat - bounds.MinLat, bounds.MaxLng - bounds.MinLng
}

// walk steps across the box cell by cell, collecting the prefixes.
func walk(box Box, precision uint) []string {
	latStep, lngStep := cellSize(box, precision)
	if latStep == 0 || lngStep == 0 {
		return nil
	}

	var cells []string
	// Stepping by the cell size from the south-west corner and stopping past
	// the far edge is what guarantees the cover is complete: a cell partly
	// inside the viewport still holds rows the reader should see.
	for lat := box.South; lat < box.North+latStep; lat += latStep {
		for lng := box.West; lng < box.East+lngStep; lng += lngStep {
			cells = append(cells, geohash.EncodeWithPrecision(
				clampLat(lat), wrapLng(lng), precision))
			if len(cells) > MaxCoverCells*4 {
				// A guard, not an expectation: precisionForBox already sized
				// this. Returning a partial cover would silently lose rows, so
				// fall back to no filter rather than to a wrong one.
				return nil
			}
		}
	}
	return cells
}

func clampLat(lat float64) float64 {
	return math.Max(-90, math.Min(90, lat))
}

func wrapLng(lng float64) float64 {
	for lng > 180 {
		lng -= 360
	}
	for lng < -180 {
		lng += 360
	}
	return lng
}

func dedupe(cells []string) []string {
	seen := make(map[string]bool, len(cells))
	unique := make([]string, 0, len(cells))
	for _, cell := range cells {
		if cell == "" || seen[cell] {
			continue
		}
		seen[cell] = true
		unique = append(unique, cell)
	}
	// Sorted so a query is stable between calls, which makes it cacheable and
	// makes a slow query log readable.
	sort.Strings(unique)
	return unique
}

// Contains reports whether a point is inside the box, for the refinement pass
// that trims the cells' overhang back to the viewport.
func (b Box) Contains(lat, lng float64) bool {
	if lat < b.South || lat > b.North {
		return false
	}
	if b.CrossesDateLine() {
		return lng >= b.West || lng <= b.East
	}
	return lng >= b.West && lng <= b.East
}

// sample returns the first few cells, for a log line that shows the shape of a
// cover without printing forty prefixes.
func sample(cells []string, n int) []string {
	if len(cells) <= n {
		return cells
	}
	return cells[:n]
}
