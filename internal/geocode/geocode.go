// Package geocode turns coordinates into the name of a place.
//
// It talks to Nominatim (nominatim.org), the OpenStreetMap geocoder, and it
// does so **from the backend, never from the browser**. That is the whole
// shape of this package: a reverse lookup made by the contributor's own
// browser would hand their IP address and their coordinates to a third party
// at the moment they are writing a grievance, which is exactly the trade this
// project refuses elsewhere. The backend asks on their behalf, so Nominatim
// learns that this site looked up a cell, and nothing about who.
//
// Two further consequences follow from that decision and are implemented here:
// the lookup is cached by cell, so the same neighbourhood is asked about once
// rather than once per doléance; and it is rate limited, because Nominatim's
// usage policy allows one request a second and a register that ignored that
// would be cut off precisely when it got busy.
package geocode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/CoderSyndicate/doleances/internal/geo"
)

// DefaultEndpoint is the public Nominatim instance.
//
// It is a default, not a constant: the usage policy for the public server
// forbids heavy use, so any real deployment points this at its own instance.
// Changing that is configuration, never a code change.
const DefaultEndpoint = "https://nominatim.openstreetmap.org"

// nominatimPolicy is what the public instance's usage policy allows.
//
// One request a second is the tightest rate of any provider here, which is why
// a second provider exists. The daily figure is **86,400** — a second's worth
// for every second of the day — which is to say no daily limit at all, stated
// in the same units as every other provider rather than as a zero meaning
// something different. A number that cannot bind is easier to read than a
// special case, and it keeps `Remaining()` answering with a count for every
// provider instead of a sentinel for one.
var nominatimPolicy = Limits{PerSecond: 1, PerDay: 86400}

// lookupTimeout bounds one call. A place name is a nicety; a submission must
// never wait on it.
const lookupTimeout = 5 * time.Second

// Place is what a lookup yields.
type Place struct {
	// Label is what a reader sees: "Saint-Jean-de-Luz", "Paris 11e",
	// "Nouvelle-Aquitaine", depending on how precise the contributor was.
	Label string

	// CountryCode is the ISO 3166-1 alpha-2 code.
	CountryCode string
}

// Geocoder resolves coordinates to a place name.
type Geocoder interface {
	Reverse(ctx context.Context, lat, lng float64, precision uint) (Place, error)
}

// Nominatim is the OpenStreetMap-backed provider.
//
// It holds no cache and no rate limiter any more: both moved to Service, which
// owns them for every provider at once. What is left here is what is actually
// Nominatim's — its endpoint, the User-Agent its usage policy demands, and the
// shape of its answer.
type Nominatim struct {
	endpoint  string
	userAgent string
	http      *http.Client
}

// New returns a service that uses Nominatim and nothing else, at its default
// limits.
//
// Kept because it is the shape every caller and every test already uses, and
// because one provider is still the common case. A deployment with a Geoapify
// key, or a plan of its own, builds the service with NewService instead.
func New(endpoint, userAgent string) *Service {
	return NewService(nil, NewNominatim(endpoint, userAgent))
}

// NewNominatim returns the provider on its own.
//
// userAgent is required by Nominatim's usage policy: a request without one
// identifying the application is blocked, and rightly — an anonymous flood is
// indistinguishable from abuse.
func NewNominatim(endpoint, userAgent string) *Nominatim {
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	return &Nominatim{
		endpoint:  endpoint,
		userAgent: userAgent,
		http:      &http.Client{Timeout: lookupTimeout},
	}
}

// Name identifies it in logs and configuration.
func (n *Nominatim) Name() string { return NominatimName }

// Defaults are what the public instance's usage policy allows: one request a
// second, and no daily ceiling — the policy bounds the rate and forbids heavy
// use in words rather than in a number. An operator running their own instance
// raises the rate and still has no daily cap, which is the point of running
// one.
func (n *Nominatim) Defaults() Limits { return nominatimPolicy }

// Reverse names the place a point falls in, at the requested granularity.
func (n *Nominatim) Reverse(ctx context.Context, lat, lng float64, precision uint) (Place, error) {

	query := url.Values{}
	query.Set("lat", strconv.FormatFloat(lat, 'f', 6, 64))
	query.Set("lon", strconv.FormatFloat(lng, 'f', 6, 64))
	// Nominatim's own zoom decides how much address detail comes back. Asking
	// for a coarse level is not merely a display choice: a street we never
	// requested is a street we never hold.
	query.Set("zoom", strconv.Itoa(geo.NominatimZoom(precision)))
	query.Set("format", "jsonv2")
	query.Set("addressdetails", "1")

	endpoint := n.endpoint + "/reverse?" + query.Encode()

	// TRACE, because this is a data dump and because the coordinates logged
	// here are the point the contributor actually pinned, before coarsening —
	// the one place in the system that still holds it. Never on in production.
	log.Trace().
		Str("url", endpoint).
		Str("user_agent", n.userAgent).
		Msg("nominatim: request")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Place{}, fmt.Errorf("geocode: build request: %w", err)
	}
	req.Header.Set("User-Agent", n.userAgent)
	req.Header.Set("Accept", "application/json")

	started := time.Now()
	resp, err := n.http.Do(req)
	if err != nil {
		log.Trace().Str("url", endpoint).Err(err).Msg("nominatim: call failed")
		return Place{}, fmt.Errorf("geocode: call nominatim: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	// Read the body out rather than decoding from the stream, so the raw
	// answer can be logged. Without it, debugging a wrong label means guessing
	// which of Nominatim's twenty address fields came back.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return Place{}, fmt.Errorf("geocode: read answer: %w", err)
	}

	log.Trace().
		Str("url", endpoint).
		Int("status", resp.StatusCode).
		Dur("took", time.Since(started)).
		Str("response_body", string(body)).
		Msg("nominatim: response")

	// A 429 is reported as itself. The queue treats it differently from an
	// ordinary failure: backed off rather than retried immediately, and after
	// five minutes of them taken as the day's allowance being gone — which is
	// the provider's own count, and the only one that is right across restarts.
	if refused := rateLimited("nominatim", resp); refused != nil {
		return Place{}, refused
	}
	if resp.StatusCode != http.StatusOK {
		return Place{}, fmt.Errorf("geocode: nominatim answered %s", resp.Status)
	}

	var payload struct {
		Address map[string]string `json:"address"`
		Name    string            `json:"name"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return Place{}, fmt.Errorf("geocode: decode answer: %w", err)
	}

	place := Place{
		Label:       label(payload.Address, precision, payload.Name),
		CountryCode: upper(payload.Address["country_code"]),
	}

	// DEBUG carries the decision without the dump: which address fields were
	// available and which label was built from them is what you need when a
	// place reads wrong, and it is safe to leave on while tuning.
	log.Debug().
		Uint("precision", precision).
		Int("nominatim_zoom", geo.NominatimZoom(precision)).
		Str("label", place.Label).
		Str("country", place.CountryCode).
		Strs("address_fields", addressKeys(payload.Address)).
		Msg("nominatim: named a place")
	return place, nil
}

// maxResponseBytes bounds one answer. A reverse lookup returns an address, not
// a document; anything larger is not something to hold in memory or a log.
const maxResponseBytes = 64 << 10

// addressKeys lists what came back, sorted, so a log line shows which fields
// were on offer when a label was chosen.
func addressKeys(address map[string]string) []string {
	keys := make([]string, 0, len(address))
	for key := range address {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// label builds the name to show at a given granularity.
//
// At the finest level it is **the town and then the district** — "Düsseldorf,
// Stadtbezirk 7", not "Stadtbezirk 7". A district name alone is unreadable to
// anybody who does not already live there, which defeats the purpose: the
// label exists so a reader knows where a doléance comes from, and a register
// organised by place cannot afford a place nobody can locate.
//
// The road is in none of these lists, at any precision. A field that can never
// hold a street cannot leak one.
func label(address map[string]string, precision uint, fallback string) string {
	first := func(keys ...string) string {
		for _, key := range keys {
			if value := address[key]; value != "" {
				return value
			}
		}
		return ""
	}

	town := first("city", "town", "village", "municipality")

	switch precision {
	case 2:
		return orFallback(first("country"), fallback)
	case 3:
		return orFallback(first("state", "region", "country"), fallback)
	case 4:
		return orFallback(first("county", "state_district", "state", "country"), fallback)
	case 5:
		return orFallback(first("city", "town", "village", "municipality",
			"county", "state"), fallback)
	}

	district := first("city_district", "suburb", "borough", "quarter")

	// A quarter within its town: the finest a doléance is ever named at.
	// Nominatim calls a Paris arrondissement a city_district and a Düsseldorf
	// Stadtbezirk the same.
	if precision <= geo.FinestPrecision {
		switch {
		case town != "" && district != "" && district != town:
			return town + ", " + district
		case town != "":
			return town
		case district != "":
			return district
		}
		return fallback
	}

	// Past that we are naming a venue rather than a person's neighbourhood,
	// and the road comes back into the list — see geo.VenuePrecision for why
	// that is a different rule rather than the same one loosened.
	//
	// The town is always appended, for the reason the doléance label appends
	// it too: "Bilker Straße 46" is unreadable to anybody who does not already
	// live there, and somebody deciding whether to attend a meeting is by
	// definition not there yet.
	street := address["road"]
	if number := address["house_number"]; street != "" && number != "" {
		street += " " + number
	}

	// Nominatim's `name` at building zoom is the venue itself — "Destille",
	// "Salle des fêtes". It is the most useful thing on the page when it
	// exists, because it is what will be written on the door.
	var venue string
	if precision >= 11 {
		venue = fallback
	}

	return joinPlace(venue, street, orFallback(town, district))
}

// joinPlace assembles the parts of a venue label, skipping the empty ones and
// never repeating a part that is already there.
func joinPlace(parts ...string) string {
	var out []string
	for _, part := range parts {
		if part == "" {
			continue
		}
		if slices.Contains(out, part) {
			continue
		}
		out = append(out, part)
	}
	return strings.Join(out, ", ")
}

func orFallback(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func upper(code string) string {
	if len(code) != 2 {
		return ""
	}
	return string([]byte{upperByte(code[0]), upperByte(code[1])})
}

func upperByte(b byte) byte {
	if b >= 'a' && b <= 'z' {
		return b - 32
	}
	return b
}
