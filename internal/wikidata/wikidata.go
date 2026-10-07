// Package wikidata resolves a subject label to a language-neutral identity.
//
// A QID is what "santé", "Gesundheit" and "health" have in common: all three
// resolve to Q12147, so they are provably one subject rather than three
// labels that scored highly against each other. That is a different kind of
// answer from an embedding — an identity rather than a similarity — and it is
// why this layer sits in front of the embedding one.
//
// # What this signal can and cannot say
//
// Measuring it on real subject labels showed it is precise but incomplete, so
// it is used in one direction only:
//
//	a shared QID is strong evidence that two subjects are the same
//	a different QID is NO evidence that they differ
//
// Both halves come from measuring it on real subject labels. Of five concepts
// tried in French, German and English, two unified exactly:
//
//	santé / Gesundheit / health         all Q12147
//	éducation / Bildung / education     all Q8434
//
// and three did not, because a bare common noun is heavily ambiguous and
// wbsearchentities ranks by search relevance rather than by domain:
//
//	logement / Wohnraum / housing       dwelling, apartment, housing
//	transport / Verkehr / transport     English "transport" is the *cellular* kind
//	retraite / Rente / pension          English "pension" is a guest house
//
// So roughly two in five, with occasional nonsense among the misses. That is
// a poor test of difference and an excellent test of sameness: the nonsense is
// harmless because a merge needs *two* labels to resolve to the same entity,
// and two unrelated labels essentially never land on the same guest house.
//
// A shared QID is therefore evidence, not a verdict: it raises a question for
// a curator rather than merging anything, and the embedding score for the same
// pair is attached to that question so a person sees two independent readings.
// When they disagree it is usually this layer that is wrong, in the way the
// "pension" line above shows — and that is precisely the merge nobody would
// ever have caught afterwards.
//
// It is worth the call despite the hit rate, because it also solves a problem
// the embedding layer could not touch: a QID carries Wikidata's own label in
// every language, so the register can show a French reader "santé" and a
// German reader "Gesundheit" for the same filter, instead of a list mixing
// three languages in whichever spelling happened to arrive first.
//
// The approach is taken from Sophia (tools/api/internal/wikidata),
// including the rule that matters most: a failed lookup is "no QID this time",
// never a hard failure. A subject with no QID simply falls through to the next
// layer.
package wikidata

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// DefaultEndpoint is the public Wikidata API.
const DefaultEndpoint = "https://www.wikidata.org/w/api.php"

// How fast we may ask, which depends entirely on how we identify ourselves.
//
// Wikimedia's published limits (mediawiki.org/wiki/Wikimedia_APIs/Rate_limits,
// deployed 2026 and explicitly subject to change) are per minute:
//
//	10    a client with "no identifying characteristics other than IP address"
//	200   an unauthenticated client sending a compliant User-Agent
//	3     concurrent requests, whoever you are
//
// The twentyfold difference is the whole reason isCompliant exists below: the
// same code is either comfortable or throttled depending on one header. So the
// pace is derived from the header we actually send rather than assumed, and it
// stays well under whichever ceiling applies — the published numbers are new
// and described as experimental, and this is somebody else's infrastructure
// answering questions we could also live without.
//
// It also explains an earlier measurement that read as mysterious: 300ms
// produced 429s within a dozen calls. 300ms is 200/minute — exactly the
// compliant ceiling, with no headroom at all.
const (
	// pacedInterval: 150/min, three quarters of the compliant ceiling.
	pacedInterval = 400 * time.Millisecond

	// anonymousInterval: 8/min, under the 10 an unidentified client gets.
	anonymousInterval = 7500 * time.Millisecond
)

// maxAttempts bounds a retry after a 429. One retry, because the caller is
// classifying a doléance and the answer is optional: a QID that needed three
// attempts is not worth the delay it costs.
const maxAttempts = 2

// defaultBackoff is how long to wait when a 429 arrives without a Retry-After
// header. Wikimedia's guidance is at least five seconds.
const defaultBackoff = 5 * time.Second

// lookupTimeout bounds one call. A QID is an improvement, not a requirement:
// a submission must never wait long for one.
const lookupTimeout = 8 * time.Second

// Result is one candidate entity.
type Result struct {
	// QID is the language-neutral identity, e.g. "Q12147".
	QID string

	// Label is Wikidata's own label in the language asked for, and
	// Description disambiguates it — "état d'isolement d'une personne" against
	// "strict form of imprisonment" is the whole difference between the right
	// entity and a prison cell.
	Label       string
	Description string

	// MatchType is how the search hit this entity: "label", "alias" or
	// "description". MatchText is the string that matched.
	//
	// It is the signal that would have caught the worst failure this package
	// has produced: searching "transports" returned spaceflight, because
	// "transports spatiaux" is one of its French aliases. The entity never
	// claimed to be called "transports", and the API said so in this field
	// while nothing read it.
	MatchType string
	MatchText string
}

// Client queries the Wikidata search API.
type Client struct {
	endpoint  string
	userAgent string
	interval  time.Duration
	http      *http.Client

	mu       sync.Mutex
	cache    map[string][]Result
	lastCall time.Time
}

// New returns a client.
//
// The user agent is not decoration: it decides which rate limit applies, and a
// client that cannot be contacted is rationed to a twentieth of the requests
// one that can gets. So the pace is chosen from the agent rather than
// configured separately — the two cannot be allowed to disagree.
func New(endpoint, userAgent string) *Client {
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}

	interval := anonymousInterval
	if isCompliant(userAgent) {
		interval = pacedInterval
	} else {
		log.Warn().Str("user_agent", userAgent).
			Msg("wikidata: the user agent carries no contact; requests will be paced for the anonymous rate limit")
	}

	if userAgent == "" {
		userAgent = "doleances"
	}
	return &Client{
		endpoint:  endpoint,
		userAgent: userAgent,
		interval:  interval,
		http:      &http.Client{Timeout: lookupTimeout},
		cache:     map[string][]Result{},
	}
}

// isCompliant reports whether a user agent carries what Wikimedia's policy
// asks for: a way to reach the operator, as an email address or a full URL.
//
// Deliberately crude. Getting this wrong in the strict direction costs a
// slower pace on a request we make once per new subject; getting it wrong in
// the permissive direction means being throttled, or blocked, with nobody able
// to tell us first.
func isCompliant(userAgent string) bool {
	if len(strings.TrimSpace(userAgent)) < 8 {
		return false
	}
	return strings.Contains(userAgent, "@") ||
		strings.Contains(userAgent, "http://") ||
		strings.Contains(userAgent, "https://")
}

// EntityURL is where a curator reads the entity itself — every label, every
// language, and the statements that say what it is.
func EntityURL(qid string) string {
	if qid == "" {
		return ""
	}
	return "https://www.wikidata.org/wiki/" + qid
}

// ArticleURL is where a curator reads what the entity actually means.
//
// A Wikidata page is a list of statements; a curator asked whether "Verkehr"
// and "transport" are one subject wants the encyclopedia article, in a
// language they read. Special:GoToLinkedPage redirects to it without a lookup
// on our side, and shows a plain "no such page" when the entity has no article
// in that language — a dead end the curator can see, rather than a link we
// would have had to spend a request to discover was missing.
func ArticleURL(qid, language string) string {
	if qid == "" {
		return ""
	}
	if len(language) != 2 {
		language = "en"
	}
	return "https://www.wikidata.org/wiki/Special:GoToLinkedPage/" +
		language + "wiki/" + qid
}

type searchResponse struct {
	Search []struct {
		ID          string `json:"id"`
		Label       string `json:"label"`
		Description string `json:"description"`
		Match       struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"match"`
	} `json:"search"`
}

// candidateLimit is how many entities a search asks for.
//
// Seven was too few, and was chosen when this package picked an answer by
// itself. Measured against real subject labels the right entity was seventh
// for "laïcité" and absent for "impôts" — and something must be in the list
// before anything can choose it. The cost is the same single request either
// way; Wikidata's ceiling is 50.
const candidateLimit = 20

// Search returns the entities Wikidata offers for a label, best first.
//
// It no longer picks. Choosing between "isolement — état d'isolement d'une
// personne" and "isolement — strict form of imprisonment" is a judgement about
// meaning, and the two things qualified to make it are a model reading the
// descriptions and a person reading them afterwards. This package's job is to
// put the options in front of them.
//
// An empty slice with a nil error is an ordinary answer: plenty of real
// subjects — "désertification médicale", "pouvoir d'achat" — have no Wikidata
// entry at all.
func (c *Client) Search(ctx context.Context, label, language string) ([]Result, error) {
	label = strings.TrimSpace(label)
	if label == "" {
		return nil, nil
	}
	if language == "" {
		language = "en"
	}

	key := language + "/" + strings.ToLower(label)
	c.mu.Lock()
	if cached, ok := c.cache[key]; ok {
		c.mu.Unlock()
		return cached, nil
	}
	c.mu.Unlock()

	results, err := c.fetch(ctx, label, language)
	if err != nil {
		return nil, err
	}

	// A miss is cached too. The same label arrives repeatedly, and asking
	// Wikidata the same unanswerable question every time is the behaviour
	// their policy asks us not to have.
	c.mu.Lock()
	c.cache[key] = results
	c.mu.Unlock()
	return results, nil
}

func (c *Client) fetch(ctx context.Context, label, language string) ([]Result, error) {
	params := url.Values{
		"action":   {"wbsearchentities"},
		"search":   {label},
		"language": {language},
		// uselang brings the labels and descriptions back in the same
		// language, which is what lets a model and then a curator read them.
		"uselang": {language},
		"format":  {"json"},
		"limit":   {strconv.Itoa(candidateLimit)},
	}
	endpoint := c.endpoint + "?" + params.Encode()

	log.Trace().Str("url", endpoint).Str("label", label).Str("language", language).
		Msg("wikidata: request")

	body, err := c.get(ctx, endpoint)
	if err != nil {
		return nil, err
	}

	var decoded searchResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("wikidata: decode: %w", err)
	}

	results := make([]Result, 0, len(decoded.Search))
	for _, candidate := range decoded.Search {
		if candidate.ID == "" {
			continue
		}
		results = append(results, Result{
			QID:         candidate.ID,
			Label:       candidate.Label,
			Description: candidate.Description,
			MatchType:   candidate.Match.Type,
			MatchText:   candidate.Match.Text,
		})
	}

	log.Debug().Str("label", label).Str("language", language).
		Int("candidates", len(results)).Msg("wikidata: searched")
	return results, nil
}

const maxLanguagesPerRequest = 50

// Labels fetches a QID's label in each language asked for.
//
// This is what makes a subject readable to everybody rather than only to
// whoever wrote it first: one filter, shown as "santé" in French and
// "Gesundheit" in German, because both are the same Q12147.
//
// An entity simply has fewer labels than were asked for, which is ordinary
// rather than a failure — Q12147 answers in 49 of 50 languages, a narrow
// concept in a dozen.
func (c *Client) Labels(ctx context.Context, qid string, languages []string) (map[string]string, error) {
	if qid == "" || len(languages) == 0 {
		return nil, nil
	}

	labels := map[string]string{}
	for start := 0; start < len(languages); start += maxLanguagesPerRequest {
		end := min(start+maxLanguagesPerRequest, len(languages))

		batch, err := c.labelBatch(ctx, qid, languages[start:end])
		if err != nil {
			// What was already fetched is kept: a subject that learned forty
			// of its names is better off than one that learned none because
			// the second request timed out.
			if len(labels) > 0 {
				log.Debug().Err(err).Str("qid", qid).Int("kept", len(labels)).
					Msg("wikidata: keeping the labels fetched before the failure")
				return labels, nil
			}
			return nil, err
		}
		for language, label := range batch {
			labels[language] = label
		}
	}
	return labels, nil
}

func (c *Client) labelBatch(ctx context.Context, qid string, languages []string) (map[string]string, error) {
	params := url.Values{
		"action":    {"wbgetentities"},
		"ids":       {qid},
		"props":     {"labels"},
		"languages": {strings.Join(languages, "|")},
		"format":    {"json"},
	}
	endpoint := c.endpoint + "?" + params.Encode()

	body, err := c.get(ctx, endpoint)
	if err != nil {
		return nil, err
	}

	var decoded struct {
		Entities map[string]struct {
			Labels map[string]struct {
				Language string `json:"language"`
				Value    string `json:"value"`
			} `json:"labels"`
		} `json:"entities"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("wikidata: decode: %w", err)
	}

	entity, ok := decoded.Entities[qid]
	if !ok {
		return nil, nil
	}

	labels := make(map[string]string, len(entity.Labels))
	for language, label := range entity.Labels {
		if label.Value != "" {
			labels[language] = label.Value
		}
	}
	return labels, nil
}

// The blocklist that used to live here has been removed, deliberately.
//
// It tested an entity's description for markers like "personne" and "ville",
// to reject people, places and works. Measured against real traffic it did the
// opposite of its job. Wikidata's best answer for "transports" is "transport
// en commun — moyen de transporter plusieurs personnes ensemble"; for
// "isolement" it is "état d'isolement d'une personne"; for "services publics",
// "service fourni par un gouvernement aux personnes vivant dans...". All three
// were rejected on the substring "personne", and the search fell through to
// spaceflight, solitary confinement and a job-vacancy agency.
//
// The filter was inverted by construction: descriptions of concepts that
// affect people naturally mention people. Nothing replaces it here. The
// candidates go to a model with their descriptions, and then to a person.

func (c *Client) get(ctx context.Context, endpoint string) ([]byte, error) {
	for attempt := 1; ; attempt++ {
		c.wait(ctx, c.interval)

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, fmt.Errorf("wikidata: build request: %w", err)
		}
		req.Header.Set("User-Agent", c.userAgent)
		req.Header.Set("Accept", "application/json")

		started := time.Now()
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("wikidata: call: %w", err)
		}

		if resp.StatusCode == http.StatusTooManyRequests && attempt < maxAttempts {
			// Being throttled is a message about our own behaviour, so it is
			// worth seeing in production rather than only under TRACE.
			delay := retryAfter(resp.Header.Get("Retry-After"))
			resp.Body.Close() //nolint:errcheck
			log.Warn().Dur("backoff", delay).Int("attempt", attempt).
				Msg("wikidata: rate limited")
			c.wait(ctx, delay)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close() //nolint:errcheck
			return nil, fmt.Errorf("wikidata: answered %s", resp.Status)
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close() //nolint:errcheck
		if err != nil {
			return nil, fmt.Errorf("wikidata: read: %w", err)
		}

		log.Trace().Dur("took", time.Since(started)).Int("bytes", len(body)).
			Msg("wikidata: answered")
		return body, nil
	}
}

// retryAfter reads the header Wikimedia sends with a 429, in either of the
// forms HTTP allows, and falls back to their published guidance of at least
// five seconds when it is absent or unreadable.
func retryAfter(header string) time.Duration {
	header = strings.TrimSpace(header)
	if header == "" {
		return defaultBackoff
	}
	if seconds, err := strconv.Atoi(header); err == nil && seconds > 0 {
		return min(time.Duration(seconds)*time.Second, time.Minute)
	}
	if when, err := http.ParseTime(header); err == nil {
		if delay := time.Until(when); delay > 0 {
			return min(delay, time.Minute)
		}
	}
	return defaultBackoff
}

// wait spaces requests out, and is where the concurrency limit is honoured
// too: one request in flight is trivially within the three Wikimedia allows.
func (c *Client) wait(ctx context.Context, interval time.Duration) {
	c.mu.Lock()
	gap := time.Until(c.lastCall.Add(interval))
	c.lastCall = time.Now().Add(max(gap, 0))
	c.mu.Unlock()

	if gap <= 0 {
		return
	}
	timer := time.NewTimer(gap)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}
