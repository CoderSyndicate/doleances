package geo

import (
	"strings"
	"testing"
)

// TestCoverIsComplete is the property that matters: a cover may return more
// than the viewport, never less. A missing cell is a doléance the reader can
// see on the map but not in the list beside it.
func TestCoverIsComplete(t *testing.T) {
	boxes := []struct {
		name string
		box  Box
	}{
		{"a town", Box{North: 43.42, South: 43.36, East: -1.62, West: -1.70}},
		{"a department", Box{North: 43.6, South: 43.1, East: -0.9, West: -1.8}},
		{"a country", Box{North: 51.1, South: 41.3, East: 9.6, West: -5.2}},
		{"a continent", Box{North: 71, South: 35, East: 40, West: -25}},
		{"southern hemisphere", Box{North: -20, South: -35, East: 20, West: 15}},
		{"equator", Box{North: 5, South: -5, East: 5, West: -5}},
	}

	for _, tt := range boxes {
		t.Run(tt.name, func(t *testing.T) {
			cells := Cover(tt.box)
			if len(cells) == 0 {
				t.Fatal("no cells returned")
			}
			if len(cells) > MaxCoverCells*4 {
				t.Errorf("%d cells, far past the budget of %d", len(cells), MaxCoverCells)
			}

			// Sample the box densely; every point inside it must fall in a cell
			// the cover named.
			const steps = 12
			latStep := (tt.box.North - tt.box.South) / steps
			lngStep := (tt.box.East - tt.box.West) / steps

			for i := 0; i <= steps; i++ {
				for j := 0; j <= steps; j++ {
					lat := tt.box.South + float64(i)*latStep
					lng := tt.box.West + float64(j)*lngStep
					hash := Encode(lat, lng)

					var covered bool
					for _, cell := range cells {
						if strings.HasPrefix(hash, cell) {
							covered = true
							break
						}
					}
					if !covered {
						t.Fatalf("point %.4f,%.4f inside the box is in no cell (%d cells at precision %d)",
							lat, lng, len(cells), len(cells[0]))
					}
				}
			}
		})
	}
}

// TestCoverTightensWithZoom: a smaller viewport should produce finer cells, or
// the query would return the same broad slab however far somebody zoomed in.
func TestCoverTightensWithZoom(t *testing.T) {
	wide := Cover(Box{North: 51.1, South: 41.3, East: 9.6, West: -5.2})
	narrow := Cover(Box{North: 43.42, South: 43.36, East: -1.62, West: -1.70})

	if len(wide[0]) >= len(narrow[0]) {
		t.Errorf("a country covers at precision %d and a town at %d; the town should be finer",
			len(wide[0]), len(narrow[0]))
	}
}

// TestCoverHandlesTheDateLine: Leaflet reports a Pacific viewport with west
// greater than east, and a cover built without noticing spans the globe the
// wrong way or returns nothing.
func TestCoverHandlesTheDateLine(t *testing.T) {
	box := Box{North: 10, South: -10, East: -170, West: 170}
	if !box.CrossesDateLine() {
		t.Fatal("the test box does not cross the date line")
	}

	cells := Cover(box)
	if len(cells) == 0 {
		t.Fatal("no cells for a viewport across the date line")
	}

	for _, lng := range []float64{175, 179.9, -179.9, -175} {
		hash := Encode(0, lng)
		var covered bool
		for _, cell := range cells {
			if strings.HasPrefix(hash, cell) {
				covered = true
				break
			}
		}
		if !covered {
			t.Errorf("longitude %.1f inside the viewport is in no cell", lng)
		}
	}

	// And it must not have quietly covered the other side of the planet.
	if strings.HasPrefix(Encode(0, 0), cells[0]) && len(cells) == 1 {
		t.Error("the cover spans the globe the long way round")
	}
}

func TestBoxContains(t *testing.T) {
	box := Box{North: 44, South: 43, East: -1, West: -2}

	if !box.Contains(43.5, -1.5) {
		t.Error("a point inside was reported outside")
	}
	if box.Contains(45, -1.5) || box.Contains(43.5, 0) {
		t.Error("a point outside was reported inside")
	}

	wrapped := Box{North: 10, South: -10, East: -170, West: 170}
	if !wrapped.Contains(0, 175) || !wrapped.Contains(0, -175) {
		t.Error("a point in a date-line-crossing box was reported outside")
	}
	if wrapped.Contains(0, 0) {
		t.Error("a point on the far side was reported inside a date-line box")
	}
}

// TestCoverOfTheWholeWorldFiltersNothing: a prefix that matches everything is
// worse than no filter, because it looks like one.
func TestCoverOfTheWholeWorldFiltersNothing(t *testing.T) {
	if cells := Cover(Box{North: 90, South: -90, East: 180, West: -180}); len(cells) != 0 {
		t.Errorf("the whole world covered as %v, want no filter at all", cells)
	}
}
