package harness

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/CoderSyndicate/doleances/internal/content"
	"github.com/CoderSyndicate/doleances/internal/passkey"
)

// fixtures is what --seeding puts into a fresh run.
//
// A file rather than Go literals, because it is content: somebody adding a
// doléance to it is thinking about what people write, not about structs. It is
// tracked, unlike `.local/tests/classifier-cases.yaml`, and the difference is
// the point — the corpus holds working abuse payloads and belongs nowhere near
// a repository, while this is eight plausible grievances and four groups that
// meet.
//
//go:embed fixtures.json
var fixtures []byte

// SeedMessage is one doléance to put in.
type SeedMessage struct {
	Text      string  `json:"text"`
	Nickname  string  `json:"nickname,omitempty"`
	BirthYear int     `json:"birth_year,omitempty"`
	Activity  string  `json:"activity,omitempty"`
	Latitude  float64 `json:"latitude,omitempty"`
	Longitude float64 `json:"longitude,omitempty"`
	Zoom      int     `json:"zoom,omitempty"`
	Country   string  `json:"country,omitempty"`
}

// SeedAction is one thing a seeded group does.
type SeedAction struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Type        string `json:"type"`

	// InDays dates a one-off relative to the run, so a seeded demonstration is
	// always in the future however old the fixture file gets.
	InDays     int    `json:"in_days,omitempty"`
	StartsTime string `json:"starts_time,omitempty"`

	Repeat   string `json:"repeat,omitempty"`
	Interval int    `json:"interval,omitempty"`
	Weekdays []int  `json:"weekdays,omitempty"`
	Monthly  string `json:"monthly,omitempty"`
	Week     int    `json:"week,omitempty"`
	Weekday  int    `json:"weekday,omitempty"`
	Month    int    `json:"month,omitempty"`
	Note     string `json:"note,omitempty"`
}

// SeedGroup is one group and what it does.
type SeedGroup struct {
	Name        string  `json:"name"`
	Description string  `json:"description,omitempty"`
	Latitude    float64 `json:"latitude,omitempty"`
	Longitude   float64 `json:"longitude,omitempty"`
	Zoom        int     `json:"zoom,omitempty"`
	Country     string  `json:"country,omitempty"`

	Actions []SeedAction `json:"actions,omitempty"`
}

type seedFile struct {
	Messages []SeedMessage `json:"messages"`
	Groups   []SeedGroup   `json:"groups"`
}

// SeedSummary is what a seeding run did.
type SeedSummary struct {
	Messages, Groups, Actions int
	Skipped                   int
}

// Seed fills a run with something to look at.
//
// Everything goes through the API rather than into the database, for the
// reason the corpus runner does the same: a fixture written straight to a
// table exercises nothing, and would keep working long after the path a real
// submission takes had broken. This goes in the front door, so a seeded run
// proves the submission path, the assessment sweep, the group confirmation
// flow and the action pipeline on every start.
//
// **Running it twice is safe, but not free.** A repeated group name is refused
// outright. A repeated doléance is not refused — the register answers every
// submission the same way on purpose, so that a flooder cannot learn which of
// their variants got through — so it is accepted, recognised as a repeat and
// dropped to the sample. That leaves rows behind.
//
// So this skips what it can see: the published register is read first and any
// fixture already in it is left alone. It can only see what is *published*,
// which is the honest limit — a doléance still waiting on the classifier is
// invisible here and would be sent again, and the guard would catch it.
//
// Assessment is left to the sweep. Seeding does not wait for a model to score
// eight submissions — the run is usable immediately and fills in over the next
// minute or so, which is what happens with real traffic too.
func Seed(ctx context.Context, backendURL, origin string, out io.Writer) (SeedSummary, error) {
	var file seedFile
	if err := json.Unmarshal(fixtures, &file); err != nil {
		return SeedSummary{}, fmt.Errorf("read the fixtures: %w", err)
	}

	seeder := &seeder{base: strings.TrimRight(backendURL, "/"), origin: origin, out: out,
		http: &http.Client{Timeout: 20 * time.Second}}

	published, err := seeder.published(ctx)
	if err != nil {
		// Not fatal. Worst case a doléance is sent twice and the duplicate
		// guard catches it, which is a tidier failure than refusing to seed.
		_, _ = fmt.Fprintf(out, "  could not read the register first: %v\n", err)
	}

	var summary SeedSummary
	for _, message := range file.Messages {
		if published[content.Hash(message.Text)] {
			summary.Skipped++
			continue
		}
		if err := seeder.message(ctx, message); err != nil {
			_, _ = fmt.Fprintf(out, "  message: %v\n", err)
			summary.Skipped++
			continue
		}
		summary.Messages++
	}

	// A group needs an admin, and an admin needs an account — so seeding
	// registers a passkey with a software authenticator and signs in with it.
	// That is deliberately the long way round: the registration and sign-in
	// paths are the two most security-sensitive things this backend does and
	// the two least visible from a Go test, because everything interesting
	// normally happens in a browser. This puts them on the critical path of
	// every local run.
	if len(file.Groups) > 0 {
		if err := seeder.signUp(ctx); err != nil {
			// The messages are already in, and they are most of the value.
			// Said plainly rather than failing the whole seed.
			_, _ = fmt.Fprintf(out, "  cannot make an account, so no groups: %v\n", err)
			return summary, nil
		}
	}

	for _, group := range file.Groups {
		id, err := seeder.group(ctx, group)
		if err != nil {
			_, _ = fmt.Fprintf(out, "  group %q: %v\n", group.Name, err)
			summary.Skipped++
			continue
		}
		summary.Groups++

		for _, action := range group.Actions {
			if err := seeder.action(ctx, id, action); err != nil {
				_, _ = fmt.Fprintf(out, "  action %q: %v\n", action.Title, err)
				summary.Skipped++
				continue
			}
			summary.Actions++
		}
	}
	return summary, nil
}

type seeder struct {
	base string
	out  io.Writer
	http *http.Client

	// origin is what the software authenticator claims the page's origin was.
	// It has to be the frontend's real public address, because the backend
	// checks it against its own configuration — a mismatch here earns exactly
	// the refusal a phishing site would get.
	origin string

	// session is the seeded account's own token, once it has one.
	session string
}

// signUp makes an account with a passkey and keeps its session.
func (s *seeder) signUp(ctx context.Context) error {
	device, err := passkey.NewSoftKey(s.origin)
	if err != nil {
		return err
	}

	var begun passkey.Ceremony
	body := map[string]any{"name": "Camille"}
	if err := s.post(ctx, "/v1/accounts/register/begin", body, &begun); err != nil {
		return fmt.Errorf("begin: %w", err)
	}

	credential, err := device.Create(begun.Options)
	if err != nil {
		return err
	}

	var signedIn struct {
		Session string `json:"session"`
	}
	finish := map[string]any{
		"ceremony": begun.Ceremony, "credential": credential,
		"device_label": "the seeder",
	}
	if err := s.post(ctx, "/v1/accounts/register/finish", finish, &signedIn); err != nil {
		return fmt.Errorf("finish: %w", err)
	}
	if signedIn.Session == "" {
		return fmt.Errorf("no session came back")
	}

	s.session = signedIn.Session
	return nil
}

// published reads what is already in the register, by the same hash the
// duplicate guard uses — so "already there" here means exactly what it means
// to the backend, rather than something this file decides for itself.
func (s *seeder) published(ctx context.Context) (map[string]bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		s.base+"/v1/messages?limit=500", nil)
	if err != nil {
		return nil, err
	}

	resp, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close() //nolint:errcheck

	var answer struct {
		Messages []struct {
			Text string `json:"text"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		return nil, err
	}

	seen := make(map[string]bool, len(answer.Messages))
	for _, message := range answer.Messages {
		seen[content.Hash(message.Text)] = true
	}
	return seen, nil
}

func (s *seeder) message(ctx context.Context, message SeedMessage) error {
	body := map[string]any{
		"text": message.Text, "nickname": message.Nickname,
		"birth_year": message.BirthYear, "activity": message.Activity,
		"latitude": message.Latitude, "longitude": message.Longitude,
		"zoom": message.Zoom, "country": message.Country,
		// The checkbox a contributor ticks. Seeding cannot pretend it was
		// ticked by leaving it out: the API refuses a submission without it,
		// and that refusal is one of the things worth exercising.
		"agreement": true,
	}
	return s.post(ctx, "/v1/messages", body, nil)
}

// group proposes one, with the seeded account as its first admin.
func (s *seeder) group(ctx context.Context, group SeedGroup) (id string, err error) {
	body := map[string]any{
		"name": group.Name, "description": group.Description,
		"latitude": group.Latitude, "longitude": group.Longitude,
		"zoom": group.Zoom, "country": group.Country,
	}

	var answer struct {
		ID string `json:"id"`
	}
	if err := s.post(ctx, "/v1/groups", body, &answer); err != nil {
		return "", err
	}
	if answer.ID == "" {
		return "", fmt.Errorf("no group identifier came back")
	}
	return answer.ID, nil
}

func (s *seeder) action(ctx context.Context, id string, action SeedAction) error {
	body := map[string]any{
		"title": action.Title, "description": action.Description,
		"type": action.Type, "starts_time": action.StartsTime,
		"repeat": action.Repeat, "interval": action.Interval,
		"weekdays": action.Weekdays, "monthly": action.Monthly,
		"week": action.Week, "weekday": action.Weekday, "month": action.Month,
		"note": action.Note,
	}

	// Dated from the run rather than from the fixture, so a seeded
	// demonstration is always still to come however old this file gets.
	days := action.InDays
	if days == 0 {
		days = 1
	}
	body["starts_on"] = time.Now().AddDate(0, 0, days).Format("2006-01-02")

	return s.post(ctx, "/v1/groups/"+url.PathEscape(id)+"/actions", body, nil)
}

func (s *seeder) post(ctx context.Context, path string, body any, out any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.base+path,
		bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.session != "" {
		req.Header.Set("Authorization", "Bearer "+s.session)
	}

	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode >= http.StatusBadRequest {
		answer, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(answer)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
