package geo

import "fmt"

// ExampleCover shows what a viewport becomes on the server: a handful of
// geohash prefixes, finer as the reader zooms in.
func ExampleCover() {
	for _, tt := range []struct {
		name string
		box  Box
	}{
		{"one town", Box{North: 43.42, South: 43.36, East: -1.62, West: -1.70}},
		{"a department", Box{North: 43.6, South: 43.1, East: -0.9, West: -1.8}},
		{"all of France", Box{North: 51.1, South: 41.3, East: 9.6, West: -5.2}},
	} {
		cells := Cover(tt.box)
		fmt.Printf("%-14s %2d cells of %d characters, first: %s\n",
			tt.name, len(cells), len(cells[0]), cells[0])
	}
	// Output:
	// one town        9 cells of 5 characters, first: ezwy9
	// a department   16 cells of 4 characters, first: ezwt
	// all of France   9 cells of 2 characters, first: ez
}
