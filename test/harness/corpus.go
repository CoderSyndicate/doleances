// Corpus running — the second half of the test run cycle.
//
// `go -C ./test run .` brings the three services up; this drives the
// classifier corpus through the real API against the real model. Nothing here
// talks to the database: the point is to exercise the path a contributor's
// text actually takes, including every part that calling the store directly
// would skip.
//
// It lives in the harness rather than in a Go test because it needs a running
// system, takes minutes, and costs model calls — a test that does all three is
// a test people learn to skip. The corpus file and the command that runs it
// are both under .local, which is gitignored; this half is here so the code is
// reviewed, compiled and kept in step with the API it calls.
package harness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Case is one entry in the corpus.
//
// The fields beyond the text are there so a run is repeatable. A doléance is
// not only its text — it carries a nickname, a birth year, an activity and a
// pinned place, and all four reach the page. Generating them per run would
// mean two runs of the same corpus were not the same input, and a difference
// in the results could not be attributed to anything.
//
// They also widen what is actually tested. The injection cases put their
// payloads in the nickname and the activity as well, because those are
// rendered beside the text on every card: a register that escapes the long
// field and not the short ones is escaping the wrong field.
type Case struct {
	ID       string   `yaml:"id"`
	Lang     string   `yaml:"lang"`
	Category string   `yaml:"category"`
	Expect   string   `yaml:"expect"`
	AlsoOK   []string `yaml:"also_ok"`
	Why      string   `yaml:"why"`
	Text     string   `yaml:"text"`

	Nickname  string `yaml:"nickname"`
	BirthYear int    `yaml:"birth_year"`
	Activity  string `yaml:"activity"`

	// Place is the pin, with the zoom the contributor supposedly placed it at
	// — which is what decides how precisely it is stored. A case with no place
	// is a doléance with no location, which is both allowed and common.
	Place *Place `yaml:"place"`
}

// Place is a pinned location in the corpus.
type Place struct {
	Name      string  `yaml:"name"`
	Latitude  float64 `yaml:"latitude"`
	Longitude float64 `yaml:"longitude"`
	Zoom      int     `yaml:"zoom"`
	Country   string  `yaml:"country"`
}

// Corpus is the file.
type Corpus struct {
	Thresholds struct {
		Accept int `yaml:"accept"`
		Curate int `yaml:"curate"`
	} `yaml:"thresholds"`
	Cases []Case `yaml:"cases"`
}

// The three outcomes a case can have.
const (
	outcomeAccept = "accept"
	outcomeCurate = "curate"
	outcomeDrop   = "drop"
)

// Result is what happened to one case.
type Result struct {
	Case       Case
	MessageID  string
	Outcome    string
	Confidence int
	Assessed   bool
	AssessedBy string
	Language   string
	Decision   string // what the harness did as curator, for curated cases

	// DropReason is set when a guard refused the submission on arrival, before
	// any classifier saw it. Distinct from "the model never answered", which
	// is the one thing in this report that must never be confused with
	// anything else.
	DropReason string

	Err error
}

// OK reports whether the outcome is one the corpus allows.
func (r Result) OK() bool {
	if r.Err != nil {
		return false
	}
	if r.Outcome == r.Case.Expect {
		return true
	}
	for _, allowed := range r.Case.AlsoOK {
		if r.Outcome == allowed {
			return true
		}
	}
	return false
}

// RunOptions is one corpus run.
type RunOptions struct {
	// Backend is the application URL of a running backend.
	Backend string

	// Cases is the corpus file.
	Cases string

	// Timeout bounds the wait for one submission to stop being in flight.
	Timeout time.Duration

	// Only selects cases whose id or category contains it; empty runs all.
	Only string

	// Curate decides the curated cases as a curator would, which is the only
	// way the accept and reject paths get exercised.
	Curate bool
}

// RunCorpus pushes the corpus through a running backend and prints what it
// decided, returning the number of cases outside expectation.
func RunCorpus(options RunOptions) (int, error) {
	corpus, err := LoadCorpus(options.Cases)
	if err != nil {
		return 0, err
	}

	cases := filter(corpus.Cases, options.Only)
	if len(cases) == 0 {
		return 0, fmt.Errorf("no cases selected")
	}

	if options.Timeout <= 0 {
		options.Timeout = 90 * time.Second
	}

	client := &client{
		base: strings.TrimRight(options.Backend, "/"),
		http: &http.Client{Timeout: 30 * time.Second},
	}
	if err := client.wait(30 * time.Second); err != nil {
		return 0, fmt.Errorf("backend not reachable: %w", err)
	}

	fmt.Printf("%d cases against %s\n\n", len(cases), client.base)

	results := make([]Result, 0, len(cases))
	for _, test := range cases {
		result := client.run(test, options.Timeout, options.Curate)
		results = append(results, result)
		report(result)
	}

	fmt.Println()
	return summarise(results, corpus), nil
}

// LoadCorpus reads the corpus file.
func LoadCorpus(path string) (Corpus, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Corpus{}, err
	}
	var corpus Corpus
	if err := yaml.Unmarshal(raw, &corpus); err != nil {
		return Corpus{}, err
	}
	return corpus, nil
}

func filter(cases []Case, only string) []Case {
	if only == "" {
		return cases
	}
	kept := make([]Case, 0, len(cases))
	for _, test := range cases {
		if strings.Contains(test.ID, only) || strings.Contains(test.Category, only) {
			kept = append(kept, test)
		}
	}
	return kept
}

// client is the backend's API, as a contributor and then as a curator.
type client struct {
	base string
	http *http.Client
}

func (c *client) wait(within time.Duration) error {
	deadline := time.Now().Add(within)
	var last error
	for time.Now().Before(deadline) {
		resp, err := c.http.Get(c.base + "/v1/messages?limit=1")
		if err == nil {
			resp.Body.Close() //nolint:errcheck
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			last = fmt.Errorf("answered %s", resp.Status)
		} else {
			last = err
		}
		time.Sleep(500 * time.Millisecond)
	}
	return last
}

// run submits one case and follows it to a resting state.
func (c *client) run(test Case, timeout time.Duration, decide bool) Result {
	result := Result{Case: test}

	id, err := c.submit(test)
	if err != nil {
		result.Err = err
		return result
	}
	result.MessageID = id

	item, err := c.settle(id, timeout)
	if err != nil {
		result.Err = err
		return result
	}

	result.Outcome = outcomeFor(item.Status)
	result.Confidence = item.Confidence
	result.Assessed = item.Assessed
	result.AssessedBy = item.AssessedBy
	result.DropReason = item.DropReason
	result.Language = item.Language

	// A curated case is only half a test until somebody decides it: accepting
	// and rejecting are the two paths that write to the audit log and, for a
	// rejection, delete the text. Exercising them here is how we find out they
	// still work end to end.
	if decide && result.Outcome == outcomeCurate {
		verb := "reject"
		if test.Expect == outcomeAccept || contains(test.AlsoOK, outcomeAccept) {
			verb = "accept"
		}
		if err := c.decide(id, verb); err != nil {
			result.Err = fmt.Errorf("%s: %w", verb, err)
			return result
		}
		result.Decision = verb
	}

	return result
}

type submission struct {
	Text      string  `json:"text"`
	Nickname  string  `json:"nickname,omitempty"`
	BirthYear int     `json:"birth_year,omitempty"`
	Activity  string  `json:"activity,omitempty"`
	Place     string  `json:"place,omitempty"`
	Latitude  float64 `json:"latitude,omitempty"`
	Longitude float64 `json:"longitude,omitempty"`
	Zoom      int     `json:"zoom,omitempty"`
	Country   string  `json:"country,omitempty"`
	Agreement bool    `json:"agreement"`
}

type receipt struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Token  string `json:"token"`
}

func (c *client) submit(test Case) (string, error) {
	payload := submission{
		Text:      strings.TrimSpace(test.Text),
		Nickname:  test.Nickname,
		BirthYear: test.BirthYear,
		Activity:  test.Activity,
		Agreement: true,
	}
	if place := test.Place; place != nil {
		payload.Place = place.Name
		payload.Latitude = place.Latitude
		payload.Longitude = place.Longitude
		payload.Zoom = place.Zoom
		payload.Country = place.Country
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	resp, err := c.http.Post(c.base+"/v1/messages", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("submit answered %s: %s", resp.Status, strings.TrimSpace(string(detail)))
	}

	var out receipt
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.ID, nil
}

type curationItem struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	Assessed    bool   `json:"assessed"`
	Confidence  int    `json:"confidence"`
	AssessedBy  string `json:"assessed_by"`
	Language    string `json:"language"`
	DropReason  string `json:"drop_reason"`
	DuplicateOf string `json:"duplicate_of"`
}

// settle polls until the submission stops being in flight.
//
// `pending` and `assessing` are both transient; everything else is an answer.
// A submission still pending at the deadline is reported as such rather than
// guessed at — "the model never answered" is a result worth seeing, and it is
// what an unconfigured or unreachable instance looks like.
func (c *client) settle(id string, timeout time.Duration) (curationItem, error) {
	deadline := time.Now().Add(timeout)
	var last curationItem

	for time.Now().Before(deadline) {
		item, err := c.read(id)
		if err != nil {
			return last, err
		}
		last = item

		if item.Status != "pending" && item.Status != "assessing" {
			return item, nil
		}
		time.Sleep(time.Second)
	}
	return last, fmt.Errorf("still %q after %s", last.Status, timeout)
}

func (c *client) read(id string) (curationItem, error) {
	var item curationItem

	resp, err := c.http.Get(c.base + "/v1/curation/" + id)
	if err != nil {
		return item, err
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		return item, fmt.Errorf("read answered %s", resp.Status)
	}
	return item, json.NewDecoder(resp.Body).Decode(&item)
}

func (c *client) decide(id, verb string) error {
	body, err := json.Marshal(map[string]string{
		"actor":  "corpus run (automated)",
		"reason": "decided by the test run cycle",
	})
	if err != nil {
		return err
	}

	resp, err := c.http.Post(c.base+"/v1/curation/"+id+"/"+verb, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("answered %s: %s", resp.Status, strings.TrimSpace(string(detail)))
	}
	return nil
}

// outcomeFor maps a stored status onto the three outcomes the corpus talks
// about. A submission a human still has to decide is `curate` whether it got
// there by score or because no model answered — which is the point of the
// `assessed` flag being reported separately.
func outcomeFor(status string) string {
	switch status {
	case "accepted":
		return outcomeAccept
	case "curating":
		return outcomeCurate
	case "dropped":
		return outcomeDrop
	default:
		return status
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func report(r Result) {
	mark := "ok  "
	if !r.OK() {
		mark = "FAIL"
	}

	line := fmt.Sprintf("%s  %-28s %-22s got %-7s want %s", mark, r.Case.ID, r.Case.Category,
		r.Outcome, r.Case.Expect)
	if len(r.Case.AlsoOK) > 0 {
		line += " (or " + strings.Join(r.Case.AlsoOK, ", ") + ")"
	}
	switch {
	case r.Assessed:
		line += fmt.Sprintf("  score %d", r.Confidence)
	case r.DropReason != "":
		// Refused on arrival. Not scored because it never needed to be.
		line += "  refused: " + r.DropReason
	case r.Err == nil:
		line += "  UNASSESSED"
	}
	if r.Decision != "" {
		line += "  curator: " + r.Decision
	}
	if r.Err != nil {
		line += "  error: " + r.Err.Error()
	}
	fmt.Println(line)
}

func summarise(results []Result, corpus Corpus) int {
	var (
		failures   []Result
		assessed   int
		refused    int
		duplicates int
		byExpect   = map[string][2]int{} // expected -> [passed, total]
		scores     = map[string][]int{}  // outcome -> scores, for tuning
		models     = map[string]int{}
		languages  = map[string]int{}
	)

	for _, r := range results {
		counts := byExpect[r.Case.Expect]
		counts[1]++
		if r.OK() {
			counts[0]++
		} else {
			failures = append(failures, r)
		}
		byExpect[r.Case.Expect] = counts

		if r.DropReason != "" {
			refused++
		}
		if r.DropReason == "duplicate" {
			duplicates++
		}
		if r.Assessed {
			assessed++
			scores[r.Case.Expect] = append(scores[r.Case.Expect], r.Confidence)
			models[r.AssessedBy]++
		}
		if r.Language != "" {
			languages[r.Language]++
		}
	}

	fmt.Printf("%d of %d cases within expectation\n", len(results)-len(failures), len(results))

	if refused > 0 {
		// Working as designed, and cheaper than the alternative: a submission
		// refused on arrival costs no model call at all.
		fmt.Printf("%d refused on arrival, before any classifier\n", refused)
	}

	// A run against a database that has already seen the corpus is not a
	// measurement of anything: the duplicate guard drops the lot and the
	// report fills with confident nonsense. It has happened — three cycles
	// were run against a stale backend because a driver script checked that
	// *something* answered on the port rather than that its own services had
	// started.
	if duplicates > len(results)/2 {
		fmt.Printf("\nWARNING: %d of %d were dropped as duplicates.\n", duplicates, len(results))
		fmt.Println("  This database has seen the corpus before, so nothing below means anything.")
		fmt.Println("  Start the services on a fresh run directory and try again.")
	}

	// The single most important line in the report, which is why it counts only
	// what genuinely went unanswered. Everything below it is meaningless if a
	// model was not replying: the "results" would be a measurement of the
	// fallback rather than of the classifier.
	if silent := len(results) - assessed - refused; silent > 0 {
		fmt.Printf("WARNING: %d of %d were never assessed — no model answered for them\n",
			silent, len(results))
	}

	fmt.Println()
	for _, expect := range []string{outcomeAccept, outcomeCurate, outcomeDrop} {
		counts := byExpect[expect]
		if counts[1] == 0 {
			continue
		}
		fmt.Printf("  expected %-7s %d/%d", expect, counts[0], counts[1])
		if values := scores[expect]; len(values) > 0 {
			sort.Ints(values)
			fmt.Printf("   scores %d..%d, median %d",
				values[0], values[len(values)-1], values[len(values)/2])
		}
		fmt.Println()
	}

	// The score ranges are what the thresholds are tuned against: an expected
	// `accept` scoring below the accept threshold is the number to move, not
	// the prompt to rewrite.
	fmt.Printf("\n  thresholds in the corpus: accept > %d, curate > %d\n",
		corpus.Thresholds.Accept, corpus.Thresholds.Curate)
	for model, n := range models {
		fmt.Printf("  model: %s (%d)\n", model, n)
	}
	if len(languages) > 0 {
		fmt.Printf("  languages detected: %v\n", languages)
	}

	if len(failures) > 0 {
		fmt.Println("\nfailures:")
		for _, r := range failures {
			fmt.Printf("  %s (%s)\n    got %s, want %s\n", r.Case.ID, r.Case.Category,
				r.Outcome, r.Case.Expect)
			if why := strings.TrimSpace(r.Case.Why); why != "" {
				fmt.Printf("    %s\n", strings.Join(strings.Fields(why), " "))
			}
			if r.Err != nil {
				fmt.Printf("    error: %v\n", r.Err)
			}
		}
	}
	return len(failures)
}
