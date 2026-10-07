package geo

import "testing"

// TestPrecisionForZoomNeverReachesTheStreet is the privacy guarantee stated as
// a test. However far somebody zooms, the register must not learn their road.
func TestPrecisionForZoomNeverReachesTheStreet(t *testing.T) {
	for zoom := range 23 {
		precision := PrecisionForZoom(zoom)
		if precision > FinestPrecision {
			t.Errorf("zoom %d gives precision %d, finer than the %d floor",
				zoom, precision, FinestPrecision)
		}
		if precision < CoarsestPrecision {
			t.Errorf("zoom %d gives precision %d, coarser than %d",
				zoom, precision, CoarsestPrecision)
		}
	}
}

// TestPrecisionForZoomReadsIntent covers the point of the field: a pin dropped
// on an untouched country view means the country, not a village.
func TestPrecisionForZoomReadsIntent(t *testing.T) {
	tests := []struct {
		name string
		zoom int
		want uint
	}{
		{"world", 2, 2},
		{"France as it opens", 5, 2},
		{"region", 7, 3},
		{"department", 9, 4},
		{"town", 12, 5},
		{"village", 13, 6},
		{"street", 17, 6},
		{"building", 19, 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PrecisionForZoom(tt.zoom); got != tt.want {
				t.Errorf("PrecisionForZoom(%d) = %d, want %d", tt.zoom, got, tt.want)
			}
		})
	}
}

// TestCoarsenLosesThePrecisePoint is the other half: the exact coordinates
// must not survive into what is stored, or the guarantee is only a display
// convention.
func TestCoarsenLosesThePrecisePoint(t *testing.T) {
	// A precise point: a house in Saint-Jean-de-Luz.
	const lat, lng = 43.388312, -1.662644

	tests := []struct {
		name     string
		zoom     int
		minShift float64 // metres the stored point must move by, at least
		maxShift float64
	}{
		{"country view", 5, 1_000, 2_000_000},
		{"region", 7, 1_000, 200_000},
		{"town", 12, 100, 10_000},
		{"as close as it gets", 19, 1, 2_000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			precision := PrecisionForZoom(tt.zoom)
			gotLat, gotLng := Coarsen(lat, lng, precision)

			if gotLat == lat && gotLng == lng {
				t.Fatal("the exact point survived coarsening")
			}
			shift := Distance(lat, lng, gotLat, gotLng)
			if shift < tt.minShift || shift > tt.maxShift {
				t.Errorf("stored point moved %.0fm, want between %.0f and %.0f",
					shift, tt.minShift, tt.maxShift)
			}
		})
	}
}

// TestCoarsenIsStable pins that two people pinning the same street get the
// same stored point — which is what makes the coarsening protective rather
// than merely noisy.
func TestCoarsenIsStable(t *testing.T) {
	const precision = FinestPrecision

	aLat, aLng := Coarsen(43.388312, -1.662644, precision)
	bLat, bLng := Coarsen(43.388401, -1.662701, precision)

	if aLat != bLat || aLng != bLng {
		t.Errorf("two points ~15m apart stored differently: %v,%v and %v,%v",
			aLat, aLng, bLat, bLng)
	}
}

// TestADoleanceNeverAsksForAStreet is the invariant that makes "a field that
// cannot hold a street cannot leak one" true rather than aspirational.
//
// It is scoped to the precisions a doléance can reach, which is every
// precision PrecisionForZoom can return. Venues go further on purpose — see
// TestAVenueAsksForTheBuilding — and the two ranges do not overlap, which is
// the whole safety of the arrangement.
func TestADoleanceNeverAsksForAStreet(t *testing.T) {
	// Nominatim's scale: 16 is a street, 18 a building. Asking for either
	// would put one in the answer, and then in the database.
	for zoom := range 22 {
		precision := PrecisionForZoom(zoom)
		if precision > FinestPrecision {
			t.Fatalf("map zoom %d resolves a doléance to precision %d, past the floor",
				zoom, precision)
		}
		if asked := NominatimZoom(precision); asked > 12 {
			t.Errorf("map zoom %d asks Nominatim for zoom %d, which reaches a street",
				zoom, asked)
		}
	}
}

// TestAVenueAsksForTheBuilding is the opposite requirement, for the opposite
// reason: a meeting place nobody can walk to is not a meeting place.
//
// Somebody who zoomed to a building gets building-level detail; somebody who
// clicked a city view does not, because they did not name a building and an
// address invented for them would be precise and wrong.
func TestAVenueAsksForTheBuilding(t *testing.T) {
	for _, tc := range []struct {
		mapZoom int
		want    int
	}{
		{4, 3},   // a country view is still a country
		{9, 8},   // a department
		{11, 10}, // a city
		{14, 14}, // a suburb
		{16, 16}, // a street
		{18, 18}, // the building somebody actually pinned
	} {
		if asked := NominatimZoom(VenuePrecisionForZoom(tc.mapZoom)); asked != tc.want {
			t.Errorf("a venue pinned at map zoom %d asks Nominatim for zoom %d, want %d",
				tc.mapZoom, asked, tc.want)
		}
	}
}

// TestAVenueIsStoredWhereItWasPinned is the bug this split was made for.
//
// A group pinned on a bar was stored as the centre of its geohash-6 cell and
// drawn 300 metres up the street. Half a cell is the error, and for a venue
// the coarsening buys nothing: there is no person in the row to protect.
func TestAVenueIsStoredWhereItWasPinned(t *testing.T) {
	// Destille, Bilker Straße 46, Düsseldorf.
	const lat, lng = 51.218650, 6.773400

	coarsened, _ := Coarsen(lat, lng, FinestPrecision)
	off := Distance(lat, lng, coarsened, lng)
	if off < 50 {
		t.Fatalf("the fixture no longer demonstrates the problem: %.0f m of drift", off)
	}

	// The venue rule keeps the pin, so the stored hash carries real characters
	// all the way down rather than a coarse cell padded out with zeros.
	hash := Encode(lat, lng)
	if len(hash) != StoredPrecision {
		t.Fatalf("hash %q is not stored at full precision", hash)
	}
	if back, _ := Decode(hash); Distance(lat, lng, back, lng) > 1 {
		t.Errorf("a venue's own geohash does not round-trip to within a metre")
	}
}

// TestMappablePrecisionStopsBeforeTheAbsurd is a regression test for a real
// mistake: coarsening a point near Saint-Jean-de-Luz to a country-sized cell
// put it in Spain, and the doléance was labelled "España".
//
// A coordinate below MappablePrecision is not imprecise, it is wrong, so no
// coordinate is stored at all at those levels.
func TestMappablePrecisionStopsBeforeTheAbsurd(t *testing.T) {
	// A house in Saint-Jean-de-Luz, about 15 km from the Spanish border.
	const lat, lng = 43.388312, -1.662644
	const borderDistance = 15_000.0

	for precision := uint(MappablePrecision); precision <= FinestPrecision; precision++ {
		gotLat, gotLng := Coarsen(lat, lng, precision)
		shift := Distance(lat, lng, gotLat, gotLng)
		if shift > borderDistance {
			t.Errorf("precision %d moves the point %.0fm, far enough to cross a border",
				precision, shift)
		}
	}

	// And the levels we refuse to map really would have been that bad.
	coarseLat, coarseLng := Coarsen(lat, lng, CoarsestPrecision)
	if Distance(lat, lng, coarseLat, coarseLng) < borderDistance {
		t.Skip("country-sized cells no longer displace points; the guard may be unnecessary")
	}
}
