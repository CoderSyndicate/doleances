package geocode

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/CoderSyndicate/doleances/internal/geo"
)

// nominatimStub answers like the real service, and records what was asked.
func nominatimStub(t *testing.T, calls *atomic.Int32, body string) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)

		// The usage policy requires an identifying agent; a client that did
		// not send one would be blocked in production, silently, later.
		if r.Header.Get("User-Agent") == "" {
			t.Error("no User-Agent was sent")
		}
		// Asking for a street is the thing this must never do.
		if zoom, _ := strconv.Atoi(r.URL.Query().Get("zoom")); zoom > 12 {
			t.Errorf("asked Nominatim for zoom %d, which reaches a street", zoom)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

const saintJean = `{
  "name": "Saint-Jean-de-Luz",
  "address": {
    "road": "Rue Gambetta",
    "city_district": "Centre",
    "city": "Saint-Jean-de-Luz",
    "county": "Pyrénées-Atlantiques",
    "state": "Nouvelle-Aquitaine",
    "country": "France",
    "country_code": "fr"
  }
}`

// TestLabelNamesTheTownAsWellAsTheDistrict: "Stadtbezirk 7" tells a reader
// nothing unless they already live in Düsseldorf. The town has to be there.
func TestLabelNamesTheTownAsWellAsTheDistrict(t *testing.T) {
	dusseldorf := map[string]string{
		"road":          "Kronprinzenstraße",
		"city_district": "Stadtbezirk 7",
		"city":          "Düsseldorf",
		"state":         "Nordrhein-Westfalen",
		"country":       "Deutschland",
		"country_code":  "de",
	}
	if got := label(dusseldorf, 6, "fallback"); got != "Düsseldorf, Stadtbezirk 7" {
		t.Errorf("label = %q, want %q", got, "Düsseldorf, Stadtbezirk 7")
	}

	paris := map[string]string{
		"city_district": "11e Arrondissement",
		"city":          "Paris",
		"country_code":  "fr",
	}
	if got := label(paris, 6, ""); got != "Paris, 11e Arrondissement" {
		t.Errorf("label = %q, want %q", got, "Paris, 11e Arrondissement")
	}

	// A town with no district of its own is not doubled up.
	village := map[string]string{"village": "Houdan", "country_code": "fr"}
	if got := label(village, 6, ""); got != "Houdan" {
		t.Errorf("label = %q, want %q", got, "Houdan")
	}

	// Nor when Nominatim repeats the town as its own district.
	same := map[string]string{"city": "Bayonne", "city_district": "Bayonne"}
	if got := label(same, 6, ""); got != "Bayonne" {
		t.Errorf("label = %q, want %q", got, "Bayonne")
	}

	// A district with no town at all is still better than nothing.
	orphan := map[string]string{"suburb": "Oberkassel"}
	if got := label(orphan, 6, ""); got != "Oberkassel" {
		t.Errorf("label = %q, want %q", got, "Oberkassel")
	}
}

func TestReverseNamesThePlaceAtTheRightGranularity(t *testing.T) {
	var calls atomic.Int32
	server := nominatimStub(t, &calls, saintJean)
	geocoder := New(server.URL, "doleances/test")

	tests := []struct {
		precision uint
		want      string
	}{
		{2, "France"},
		{3, "Nouvelle-Aquitaine"},
		{4, "Pyrénées-Atlantiques"},
		{5, "Saint-Jean-de-Luz"},
		{6, "Saint-Jean-de-Luz, Centre"},
	}
	for _, tt := range tests {
		place, err := geocoder.Reverse(context.Background(), 43.3883, -1.6626, tt.precision)
		if err != nil {
			t.Fatalf("Reverse(precision %d): %v", tt.precision, err)
		}
		if place.Label != tt.want {
			t.Errorf("precision %d gave %q, want %q", tt.precision, place.Label, tt.want)
		}
		if place.CountryCode != "FR" {
			t.Errorf("country code = %q, want FR", place.CountryCode)
		}
	}
}

// TestADoleanceLabelNeverReturnsAStreet is the guarantee that matters most
// here: the answer contains a road, and no granularity a doléance can reach
// may ever choose it.
func TestADoleanceLabelNeverReturnsAStreet(t *testing.T) {
	address := map[string]string{
		"road":         "Rue Gambetta",
		"house_number": "12",
		"city":         "Saint-Jean-de-Luz",
		"country":      "France",
		"country_code": "fr",
	}
	for precision := uint(1); precision <= geo.FinestPrecision; precision++ {
		got := label(address, precision, "fallback")
		if strings.Contains(got, "Gambetta") || strings.Contains(got, "12") {
			t.Errorf("precision %d chose %q, which is a street address", precision, got)
		}
	}
}

// TestAVenueLabelNamesTheDoor is the other half, and the reason the two
// granularity ranges are separate rather than one scale with a cap.
//
// A group's meeting place is a pub, not a person's neighbourhood: somebody
// deciding whether to turn up needs the street and, where Nominatim knows it,
// the name over the door. "Düsseldorf, Stadtbezirk 1" is what this used to
// say, and it is unwalkable.
func TestAVenueLabelNamesTheDoor(t *testing.T) {
	address := map[string]string{
		"road":          "Bilker Straße",
		"house_number":  "46",
		"city":          "Düsseldorf",
		"city_district": "Stadtbezirk 1",
		"country":       "Germany",
		"country_code":  "de",
	}

	// Pinned on the building: the venue name comes from Nominatim's own
	// `name`, which is what the fallback argument carries.
	got := label(address, geo.VenuePrecision, "Destille")
	for _, want := range []string{"Destille", "Bilker Straße 46", "Düsseldorf"} {
		if !strings.Contains(got, want) {
			t.Errorf("label %q is missing %q", got, want)
		}
	}

	// Pinned on the street: the road, without inventing a building that was
	// never chosen.
	got = label(address, 9, "Destille")
	if !strings.Contains(got, "Bilker Straße 46") || strings.Contains(got, "Destille") {
		t.Errorf("a street-level venue read %q", got)
	}

	// Pinned on the city: unchanged from a doléance, because nothing finer
	// was asked for.
	if got := label(address, 5, "Destille"); got != "Düsseldorf" {
		t.Errorf("a city-level venue read %q, want the city alone", got)
	}
}

// TestAVenueLabelNeverRepeatsItself: Nominatim answers a bare village with the
// same string in `name`, `road` and `city`, and "Guéret, Guéret, Guéret" is
// what a naive join produces.
func TestAVenueLabelNeverRepeatsItself(t *testing.T) {
	address := map[string]string{
		"road":         "Guéret",
		"village":      "Guéret",
		"country_code": "fr",
	}
	if got := label(address, geo.VenuePrecision, "Guéret"); got != "Guéret" {
		t.Errorf("label = %q, want %q", got, "Guéret")
	}
}

// TestReverseCachesByCell keeps the register inside Nominatim's usage policy:
// a hundred doléances from one town must not be a hundred requests.
func TestReverseCachesByCell(t *testing.T) {
	var calls atomic.Int32
	server := nominatimStub(t, &calls, saintJean)
	geocoder := New(server.URL, "doleances/test")

	ctx := context.Background()
	for range 5 {
		// Points a few metres apart, all in the same cell.
		if _, err := geocoder.Reverse(ctx, 43.38831, -1.66264, 5); err != nil {
			t.Fatalf("Reverse: %v", err)
		}
		if _, err := geocoder.Reverse(ctx, 43.38835, -1.66269, 5); err != nil {
			t.Fatalf("Reverse: %v", err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("made %d requests for one cell, want 1", got)
	}

	// A different granularity is a different question and does cost a request.
	if _, err := geocoder.Reverse(ctx, 43.38831, -1.66264, 3); err != nil {
		t.Fatalf("Reverse: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("made %d requests, want 2", got)
	}
}

func TestReverseReportsFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	geocoder := New(server.URL, "doleances/test")
	if _, err := geocoder.Reverse(context.Background(), 43.3883, -1.6626, 5); err == nil {
		t.Error("a rate-limited answer should be reported, not swallowed")
	}
}
