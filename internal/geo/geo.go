// Package geo turns coordinates into geohashes and back.
//
// A geohash is a string whose prefix is a place: two points in the same cell
// share a prefix, and a longer prefix is a smaller cell. That makes proximity
// a string comparison, which is the one kind of query PostgreSQL and SQLite
// index identically — no PostGIS, no engine-specific SQL, which the database
// decision requires.
//
// The library is mmcloughlin/geohash: pure Go with no dependencies, so it
// costs nothing in a CGO_ENABLED=0 build. (H3 would be the other candidate and
// is ruled out by needing cgo; S2 via golang/geo is pure Go and arguably has
// better-shaped cells, but its cell ids want range queries rather than a
// prefix, which is more machinery for no gain at this scale.)
package geo

import (
	"math"
	"strings"

	"github.com/mmcloughlin/geohash"
)

// StoredPrecision is how many characters are kept in the database: the full
// 12, about 37mm across.
//
// Precision is chosen once, at the longest, because a shorter cell is a prefix
// of a longer one. Storing 12 lets any coarser search be a prefix of what is
// already there; storing 6 would mean a migration the first time somebody
// wants a finer one.
const StoredPrecision = 12

// Encode renders coordinates at the stored precision.
func Encode(lat, lng float64) string {
	return geohash.EncodeWithPrecision(lat, lng, StoredPrecision)
}

// Decode returns the centre of a cell.
func Decode(hash string) (lat, lng float64) {
	return geohash.DecodeCenter(hash)
}

// Valid reports whether a string is a geohash.
func Valid(hash string) bool {
	return hash != "" && geohash.Validate(hash) == nil
}

// PrecisionForRadius picks a cell size for a search radius, in metres.
//
// The table is the standard geohash cell widths. The choice errs towards the
// larger cell — searching a cell bigger than asked for returns extra rows that
// a distance check in Go then discards, while searching a smaller one misses
// results outright, and a silent miss is much worse than a wasted row.
func PrecisionForRadius(metres float64) uint {
	// The invariant: a cell must be at least as wide as the radius. The nine
	// cells searched are three cells across, so a point anywhere in the middle
	// one — including at a corner — has a full cell of cover in every
	// direction. Pick a narrower cell and the ring stops reaching the edge of
	// the circle, which loses results without any sign that it did.
	switch {
	case metres <= 38:
		return 8
	case metres <= 153:
		return 7
	case metres <= 1_200:
		return 6
	case metres <= 4_900:
		return 5
	case metres <= 39_000:
		return 4
	case metres <= 156_000:
		return 3
	case metres <= 1_250_000:
		return 2
	default:
		return 1 // ~5000 km; past this the answer is "the planet"
	}
}

// Cells returns the prefixes to search for a point: the cell it falls in, plus
// its eight neighbours.
//
// The neighbours are the whole point. A geohash prefix is a grid, and two
// points either side of a cell boundary can be a metre apart and share nothing
// — so a query on the single containing cell silently misses the nearest
// results whenever somebody stands near an edge. Searching the ring of nine
// makes the boundary stop mattering.
func Cells(lat, lng float64, precision uint) []string {
	if precision == 0 || precision > StoredPrecision {
		precision = StoredPrecision
	}

	centre := geohash.EncodeWithPrecision(lat, lng, precision)
	cells := append([]string{centre}, geohash.Neighbors(centre)...)

	// A pole or the date line can hand back duplicates or empties; a query
	// built from those would be wrong rather than merely wasteful.
	seen := make(map[string]bool, len(cells))
	unique := make([]string, 0, len(cells))
	for _, cell := range cells {
		if cell == "" || seen[cell] {
			continue
		}
		seen[cell] = true
		unique = append(unique, cell)
	}
	return unique
}

// Prefix shortens a stored geohash to a coarser cell.
func Prefix(hash string, precision uint) string {
	if uint(len(hash)) <= precision {
		return hash
	}
	return hash[:precision]
}

// Distance is the great-circle distance between two points, in metres.
//
// A geohash query answers "in roughly this area"; this is what turns that into
// "within this many metres", because the cell ring always returns more than
// was asked for.
func Distance(lat1, lng1, lat2, lng2 float64) float64 {
	const earthRadius = 6_371_000 // metres

	φ1, φ2 := radians(lat1), radians(lat2)
	Δφ := radians(lat2 - lat1)
	Δλ := radians(lng2 - lng1)

	a := math.Sin(Δφ/2)*math.Sin(Δφ/2) +
		math.Cos(φ1)*math.Cos(φ2)*math.Sin(Δλ/2)*math.Sin(Δλ/2)
	return earthRadius * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

func radians(degrees float64) float64 { return degrees * math.Pi / 180 }

// Normalize lowercases and trims a geohash arriving from outside. Geohash
// alphabet is lowercase; an uppercase one from a query string would silently
// match nothing.
func Normalize(hash string) string {
	return strings.ToLower(strings.TrimSpace(hash))
}

// ---------------------------------------------------------------------------
// Zoom — the contributor's own statement of how precise they meant to be
// ---------------------------------------------------------------------------

// Zoom levels, as Leaflet counts them: 0 is the whole world, 5 a country, 13 a
// village, 18 a building.
const (
	// FinestPrecision is the most precise location this register will ever
	// store: geohash 6, cells about 1.2 km across.
	//
	// Street level is deliberately unreachable. A doléance carries an age, an
	// activity and a place, and a street address turns that combination into a
	// name — so the finest the site records is a quarter or an arrondissement,
	// no matter how far somebody zoomed in. Nothing in the interface offers
	// more, and the coarsening happens on the server, so nothing a browser
	// sends can defeat it.
	FinestPrecision = 6

	// CoarsestPrecision is a country-sized cell, about 1250 km.
	CoarsestPrecision = 2

	// MappablePrecision is the coarsest granularity that still means a point.
	//
	// Below it, a coordinate is a lie dressed as data. A geohash cell at
	// country precision is about 1250 km across, so snapping a point to its
	// centre can move it hundreds of kilometres — a house near Saint-Jean-de-
	// Luz lands in Spain. Somebody who pinned an untouched country view was
	// never naming a point in the first place: they meant "France", and France
	// is not a latitude. So at country and region granularity the register
	// keeps the name and no coordinates, and the map simply has nothing to
	// show, which is the truth.
	MappablePrecision = 4
)

// PrecisionForZoom reads a map zoom as an intention about granularity.
//
// Somebody who never moved the map off its country view and dropped a pin is
// not telling us their village — they are saying "France". Somebody who zoomed
// to their town is saying the town. Treating both as an exact coordinate would
// invent a precision neither of them offered, and then publish it.
func PrecisionForZoom(zoom int) uint {
	switch {
	case zoom <= 5:
		return 2 // country
	case zoom <= 7:
		return 3 // region
	case zoom <= 9:
		return 4 // department
	case zoom <= 12:
		return 5 // town or commune, about 5 km
	default:
		return FinestPrecision // quarter or arrondissement — the floor
	}
}

// Coarsen snaps a point to the centre of its cell at the given precision.
//
// This is what makes the precision real rather than advisory. Keeping the
// exact coordinates and merely displaying them roughly would leave the precise
// point in the database, in the API, and in every snapshot ever exported —
// one query away from being precise again. The centre of the cell is all the
// site ever learns.
func Coarsen(lat, lng float64, precision uint) (float64, float64) {
	if precision == 0 {
		precision = FinestPrecision
	}
	if precision > FinestPrecision {
		precision = FinestPrecision
	}
	if precision < CoarsestPrecision {
		precision = CoarsestPrecision
	}
	return geohash.DecodeCenter(geohash.EncodeWithPrecision(lat, lng, precision))
}

// NominatimZoom translates a geohash precision into the zoom the Nominatim
// reverse endpoint expects, which is its own scale: 3 country, 5 state, 8
// county, 10 city, 12 town or borough, 14 suburb, 16 street, 18 building.
//
// It is capped at 12 for the same reason as FinestPrecision: asking for a
// street is asking to be told one, and then the answer is in the database
// whether or not anything displays it.
func NominatimZoom(precision uint) int {
	switch {
	case precision <= 2:
		return 3 // country
	case precision == 3:
		return 5 // region
	case precision == 4:
		return 8 // department
	case precision == 5:
		return 10 // city
	case precision <= FinestPrecision:
		return 12 // town or borough — the finest a doléance ever asks for
	case precision <= 8:
		return 14 // suburb
	case precision <= 10:
		return 16 // street
	default:
		return 18 // building
	}
}

// ---------------------------------------------------------------------------
// Venues — a meeting place is not a person's location
// ---------------------------------------------------------------------------

// VenuePrecision is the granularity a group's or an action's meeting place is
// stored at: the full StoredPrecision, which is to say the point exactly as it
// was pinned.
//
// This is the deliberate opposite of FinestPrecision above, and the difference
// is not a relaxation of the same rule — it is a different rule, because the
// thing being located is different.
//
// A doléance's place is coarsened because a doléance also carries a birth year
// and an activity, and a trade plus an age plus a street is a name with no
// name attached. A meeting place has none of that: there is no person in the
// row. What there is instead is a pub, a hall, a square — and an address that
// somebody has to be able to walk to. A coarsened venue is not a private
// venue, it is a **wrong** one: half a geohash-6 cell is about 300 metres, so
// the register would send people to the wrong end of the street and call it
// privacy.
//
// The trade is real and belongs on the form rather than in the code: a group
// that meets in somebody's front room is publishing that address. The form
// says so, and a curator reads every group before it reaches the map.
const VenuePrecision = StoredPrecision

// VenuePrecisionForZoom reads a map zoom for a venue.
//
// The coordinates are stored as pinned whatever this returns — coarsening is
// what we are getting rid of. What it decides is how much *address detail* to
// ask Nominatim for, and the answer still has to follow the map the person was
// looking at: asking for a building when somebody clicked a city view produces
// a precise-sounding address they never chose.
//
// It is also the cache key, so two groups pinned on the same city view share
// one lookup while two pinned on the same building do not.
func VenuePrecisionForZoom(zoom int) uint {
	switch {
	case zoom <= 5:
		return 2 // country
	case zoom <= 7:
		return 3 // region
	case zoom <= 9:
		return 4 // department
	case zoom <= 12:
		return 5 // city
	case zoom <= 14:
		return 7 // suburb
	case zoom <= 16:
		return 9 // street
	default:
		return VenuePrecision // the building
	}
}
