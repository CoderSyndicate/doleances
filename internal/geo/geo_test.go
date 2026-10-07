package geo

import (
	"math"
	"strings"
	"testing"
)

// Reference points used across the tests.
const (
	saintJeanLat, saintJeanLng = 43.3883, -1.6626 // Saint-Jean-de-Luz
	houdanLat, houdanLng       = 48.7889, 1.6019  // Houdan
)

func TestEncodeRoundTrip(t *testing.T) {
	hash := Encode(saintJeanLat, saintJeanLng)

	if len(hash) != StoredPrecision {
		t.Errorf("hash %q is %d characters, want %d", hash, len(hash), StoredPrecision)
	}
	if !Valid(hash) {
		t.Errorf("Valid(%q) = false", hash)
	}

	lat, lng := Decode(hash)
	// Stored precision is about 37mm, so a metre of tolerance is generous.
	if Distance(saintJeanLat, saintJeanLng, lat, lng) > 1 {
		t.Errorf("decoded %v,%v is more than a metre from the original", lat, lng)
	}
}

func TestNearbyPointsSharePrefix(t *testing.T) {
	here := Encode(saintJeanLat, saintJeanLng)
	// Roughly 200 metres north.
	near := Encode(saintJeanLat+0.0018, saintJeanLng)
	far := Encode(houdanLat, houdanLng)

	if Prefix(here, 5) != Prefix(near, 5) {
		t.Errorf("points 200m apart do not share a 5-character prefix: %q vs %q", here, near)
	}
	if Prefix(here, 3) == Prefix(far, 3) {
		t.Errorf("points 600km apart share a 3-character prefix: %q vs %q", here, far)
	}
}

// TestCellsCoverTheBoundary is the reason Cells exists. Two points either side
// of a cell edge can be metres apart and share no prefix at all, so a search
// of the single containing cell would silently miss the nearest result.
func TestCellsCoverTheBoundary(t *testing.T) {
	// Walk east in small steps until the cell changes, then check that the
	// point just past the edge is still found from the point just before it.
	const precision = 6
	lat, lng := saintJeanLat, saintJeanLng
	start := Prefix(Encode(lat, lng), precision)

	var crossed bool
	for step := range 2000 {
		probe := lng + float64(step)*0.00002
		if Prefix(Encode(lat, probe), precision) == start {
			continue
		}
		crossed = true

		// The neighbour ring around the original point must include the cell
		// the probe landed in.
		cells := Cells(lat, lng, precision)
		want := Prefix(Encode(lat, probe), precision)

		var found bool
		for _, cell := range cells {
			if cell == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("cell %q just across the boundary is not in the ring %v", want, cells)
		}
		if Distance(lat, lng, lat, probe) > 2000 {
			t.Fatalf("crossed a boundary only after %.0fm, which is not a boundary case",
				Distance(lat, lng, lat, probe))
		}
		break
	}
	if !crossed {
		t.Fatal("never crossed a cell boundary; the test proves nothing")
	}
}

func TestCellsAreUnique(t *testing.T) {
	for _, tt := range []struct {
		name     string
		lat, lng float64
	}{
		{"ordinary", saintJeanLat, saintJeanLng},
		{"north pole", 90, 0},
		{"south pole", -90, 0},
		{"date line", 0, 180},
		{"null island", 0, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cells := Cells(tt.lat, tt.lng, 6)
			if len(cells) == 0 {
				t.Fatal("no cells returned")
			}
			seen := map[string]bool{}
			for _, cell := range cells {
				if cell == "" {
					t.Error("an empty cell would match every row")
				}
				if seen[cell] {
					t.Errorf("duplicate cell %q", cell)
				}
				seen[cell] = true
			}
		})
	}
}

func TestPrecisionForRadiusErrsLarge(t *testing.T) {
	// A larger cell costs extra rows the distance check discards; a smaller
	// one loses results, so every radius must map to a cell at least as wide.
	widths := map[uint]float64{
		1: 5_000_000, 2: 1_250_000, 3: 156_000, 4: 39_000,
		5: 4_900, 6: 1_200, 7: 153, 8: 38,
	}

	radii := []float64{1, 38, 39, 50, 100, 153, 154, 500, 1_000, 1_200, 1_201,
		5_000, 25_000, 39_000, 150_000, 600_000, 1_250_000}
	for _, radius := range radii {
		precision := PrecisionForRadius(radius)
		width, ok := widths[precision]
		if !ok {
			t.Fatalf("radius %.0f gave unknown precision %d", radius, precision)
		}
		if width < radius {
			t.Errorf("radius %.0fm maps to precision %d (~%.0fm cells), which is too small",
				radius, precision, width)
		}
	}
}

func TestDistance(t *testing.T) {
	// Saint-Jean-de-Luz to Houdan is about 640 km.
	got := Distance(saintJeanLat, saintJeanLng, houdanLat, houdanLng)
	if math.Abs(got-640_000) > 20_000 {
		t.Errorf("distance = %.0fm, want about 640000m", got)
	}
	if d := Distance(saintJeanLat, saintJeanLng, saintJeanLat, saintJeanLng); d != 0 {
		t.Errorf("distance to itself = %v, want 0", d)
	}
}

func TestNormalize(t *testing.T) {
	if got := Normalize("  EZZZ  "); got != "ezzz" {
		t.Errorf("Normalize = %q, want %q", got, "ezzz")
	}
}

func TestPrefixNeverGrows(t *testing.T) {
	hash := Encode(saintJeanLat, saintJeanLng)
	if got := Prefix(hash, 20); got != hash {
		t.Errorf("Prefix beyond the length changed the hash: %q", got)
	}
	if got := Prefix(hash, 4); len(got) != 4 || !strings.HasPrefix(hash, got) {
		t.Errorf("Prefix(4) = %q, not a prefix of %q", got, hash)
	}
}
