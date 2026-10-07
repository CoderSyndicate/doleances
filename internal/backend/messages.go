package backend

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/danielgtaylor/huma/v2"
	"github.com/rs/zerolog/log"

	"github.com/CoderSyndicate/doleances/internal/cache"
	"github.com/CoderSyndicate/doleances/internal/content"
	"github.com/CoderSyndicate/doleances/internal/geo"
	"github.com/CoderSyndicate/doleances/internal/models"
	"github.com/CoderSyndicate/doleances/internal/store"
	"github.com/CoderSyndicate/doleances/internal/token"
)

// maxMessageRunes bounds a submission.
//
// It is deliberately generous. The design says some people will write a
// complaint and some will write their life, and the UI must not be shaped for
// only the short one — so this exists to stop a machine filling the database,
// not to tell a person they have said enough.
const maxMessageRunes = 100_000

// maxNicknameRunes and maxActivityRunes bound the two short free-text fields.
const (
	maxNicknameRunes = 128
	maxActivityRunes = 128
)

func (a *API) registerMessageRoutes(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "submit-message",
		Method:      http.MethodPost,
		Path:        "/v1/messages",
		Summary:     "Submit a doléance",
		Description: "Records a grievance and returns the permalink and the deletion token. " +
			"The token is returned once and never again: only its hash is stored, so the " +
			"site cannot edit or delete on the contributor's behalf.",
		Tags: []string{"Messages"},
	}, a.submitMessage)

	huma.Register(api, huma.Operation{
		OperationID: "get-message",
		Method:      http.MethodGet,
		Path:        "/v1/messages/{id}",
		Summary:     "Read one doléance",
		Tags:        []string{"Messages"},
	}, a.getMessage)

	huma.Register(api, huma.Operation{
		OperationID: "list-messages",
		Method:      http.MethodGet,
		Path:        "/v1/messages",
		Summary:     "List the published register",
		Tags:        []string{"Messages"},
	}, a.listMessages)

	huma.Register(api, huma.Operation{
		OperationID: "list-nearby-messages",
		Method:      http.MethodGet,
		Path:        "/v1/messages/nearby",
		Summary:     "List published doléances near a point",
		Description: "Geohash prefix search over the cell containing the point and its eight " +
			"neighbours, refined to an actual radius in Go. Nearest first.",
		Tags: []string{"Messages"},
	}, a.listNearbyMessages)

	huma.Register(api, huma.Operation{
		OperationID: "like-message",
		Method:      http.MethodPost,
		Path:        "/v1/messages/{id}/like",
		Summary:     "Say this happened to you too",
		Description: "Anonymous by construction, and therefore a count of presses rather than " +
			"of people: the register's readers have no identity and this endpoint asks for " +
			"none. Nothing is ranked by it. Its worth is to whoever wrote the doléance, " +
			"who finds out they were not the only one.",
		Tags: []string{"Messages"},
	}, a.likeMessage)

	huma.Register(api, invalidates(cache.Messages)(huma.Operation{
		OperationID: "report-message",
		Method:      http.MethodPost,
		Path:        "/v1/messages/{id}/report",
		Summary:     "Ask a human to look at this doléance",
		Description: "Takes it off the register and puts it in the curation queue. The " +
			"removal is temporary and the text is untouched: a curator accepting it " +
			"publishes it again, marked as read by a person. Only a published, " +
			"unverified doléance can be reported — one a curator has already let " +
			"stand answers the same way as one that does not exist.",
		Tags: []string{"Messages"},
	}), a.reportMessage)

	huma.Register(api, invalidates(cache.Messages)(huma.Operation{
		OperationID: "delete-message",
		Method:      http.MethodDelete,
		Path:        "/v1/messages/{id}",
		Summary:     "Delete a doléance",
		Description: "Authorised by the deletion token issued at submission. Deletion is real: " +
			"the message, its classification and any pending revision are removed.",
		Tags: []string{"Messages"},
	}), a.deleteMessage)
}

// MessageSubmission is what a contributor sends.
//
// There is no language field. The language is what the text is written in, not
// what page it was typed on, and only something that has read the text can say
// which — so it is filled in when the classifier reports it, and is empty
// until then. See models.Message.Language.
type MessageSubmission struct {
	Body struct {
		Text      string  `json:"text" doc:"the grievance, or the story"`
		Nickname  string  `json:"nickname,omitempty"`
		BirthYear int     `json:"birth_year,omitempty"`
		Activity  string  `json:"activity,omitempty"`
		Latitude  float64 `json:"latitude,omitempty"`
		Longitude float64 `json:"longitude,omitempty"`
		Place     string  `json:"place,omitempty"`
		Country   string  `json:"country,omitempty"`

		// Zoom is the map zoom when the pin was placed — the contributor's own
		// statement of how precise they meant to be. The location is coarsened
		// to match, and the coarsening happens here rather than in the browser,
		// so nothing a client sends can ask for more precision than this.
		Zoom      int    `json:"zoom,omitempty"`
		ExpiresOn string `json:"expires_on,omitempty" doc:"YYYY-MM-DD; empty means keep permanently"`

		// Agreement records that the contributor ticked the box. It is checked
		// here as well as in the browser: consent that only the form enforces
		// is not consent, it is a rendering detail.
		Agreement bool `json:"agreement"`
	}
}

// MessageReceipt is what the contributor gets back, once.
type MessageReceipt struct {
	Body struct {
		ID     string `json:"id"`
		Status string `json:"status"`

		// Token authorises editing and deletion. It appears in this response
		// and nowhere else, ever: only its hash is stored.
		Token string `json:"token"`
	}
}

func (a *API) submitMessage(ctx context.Context, in *MessageSubmission) (*MessageReceipt, error) {
	body := in.Body

	if !body.Agreement {
		return nil, huma.Error422UnprocessableEntity(
			"the submission agreement has to be accepted")
	}

	// Sanitation first, so everything downstream — the hash, the classifier,
	// the register — sees the same text a reader will. See content.Sanitise
	// for why this is the one edit made to a contributor's words.
	text, stripped := content.Sanitise(body.Text)
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, huma.Error422UnprocessableEntity("a doléance needs some text")
	}
	if stripped {
		log.Info().Msg("submission: invisible formatting characters removed before storing")
	}
	if utf8.RuneCountInString(text) > maxMessageRunes {
		return nil, huma.Error422UnprocessableEntity("that is longer than this register accepts")
	}

	// The nickname and the activity are printed beside the text on every card,
	// so they get the same treatment. A register that cleans the long field
	// and not the short ones is cleaning the wrong field.
	nickname, _ := content.Sanitise(body.Nickname)
	activity, _ := content.Sanitise(body.Activity)

	message := models.Message{
		Text:      text,
		Nickname:  trimTo(nickname, maxNicknameRunes),
		Activity:  trimTo(activity, maxActivityRunes),
		BirthYear: body.BirthYear,
		Status:    models.StatusPending,
	}

	// Two structural refusals, both before the classifier sees anything and
	// before either costs a model call. Neither is a judgement about the text:
	// one is arithmetic, the other is the presence of content that exists to
	// run rather than to be read.
	switch {
	case a.carriesPayload(message):
		message.Status = models.StatusDropped
		message.DropReason = models.DropPayload

	default:
		if original := a.duplicateOf(ctx, text); original != "" {
			message.Status = models.StatusDropped
			message.DropReason = models.DropDuplicate
			message.DuplicateOf = original
		}
	}

	// A birth year outside the plausible range is dropped rather than
	// refused: it is an optional detail somebody mistyped, not a reason to
	// hand back a form and risk losing what they wrote.
	if year := body.BirthYear; year != 0 {
		thisYear := time.Now().Year()
		if year < thisYear-110 || year > thisYear-7 {
			message.BirthYear = 0
		}
	}

	if body.Latitude != 0 || body.Longitude != 0 {
		message.Location = a.resolveLocation(ctx, body.Latitude, body.Longitude, body.Zoom,
			trimTo(body.Place, maxNicknameRunes), strings.ToUpper(trimTo(body.Country, 2)))
	}

	if body.ExpiresOn != "" {
		expiry, err := time.Parse("2006-01-02", body.ExpiresOn)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("that expiry date is not a date")
		}
		if expiry.Before(time.Now()) {
			return nil, huma.Error422UnprocessableEntity("that expiry date has already passed")
		}
		message.ExpiresAt = &expiry
	}

	value, hash, err := token.New()
	if err != nil {
		log.Error().Err(err).Msg("cannot issue a deletion token")
		return nil, huma.Error500InternalServerError("cannot record the doléance")
	}
	message.TokenHash = hash

	if err := a.store.CreateMessage(ctx, &message); err != nil {
		log.Error().Err(err).Msg("cannot record a doléance")
		return nil, huma.Error500InternalServerError("cannot record the doléance")
	}

	// INFO, not DEBUG: one line per submission is a slow-frequency state
	// change, and it carries no content — an identifier and a length.
	log.Info().Str("message", message.ID).Int("runes", utf8.RuneCountInString(text)).
		Msg("doléance recorded")

	out := &MessageReceipt{}
	out.Body.ID = message.ID
	out.Body.Token = value

	// Always "pending", including for a duplicate that has just been dropped.
	//
	// This is the one place this API says something other than what happened,
	// and it is deliberate. Telling somebody their text was recognised as a
	// repeat tells a flooder exactly which of their five hundred variants got
	// through, which turns the guard into a tuning instrument for the thing it
	// is guarding against. There is nothing to gain by answering it honestly:
	// a person who submitted the same doléance twice by accident is not
	// waiting for a verdict on the second one.
	//
	// Nothing else about the response is false. The identifier is real, the
	// token is real and still authorises deletion, and the permalink shows
	// what it shows for every submission awaiting review — which is what makes
	// a dropped duplicate indistinguishable from an ordinary queue, rather
	// than a special case somebody can probe for.
	out.Body.Status = string(models.StatusPending)
	return out, nil
}

// carriesPayload reports whether any field of a submission holds content whose
// purpose is to run in a reader's browser.
//
// Every field is checked, not only the text: a nickname and an activity are
// rendered on every card that shows the doléance, and an attack does not care
// which box it arrived in.
//
// A hit refuses the whole submission rather than cleaning it. Stripping the
// payload and publishing the rest would mean an attacker gets their text into
// the register every time and loses only the tag — and removing content, as
// opposed to removing invisible control codes, is editing somebody's
// submission, which nobody here does to anyone.
func (a *API) carriesPayload(message models.Message) bool {
	for field, value := range map[string]string{
		"text":     message.Text,
		"nickname": message.Nickname,
		"activity": message.Activity,
	} {
		if content.ContainsExecutablePayload(value) {
			log.Warn().Str("field", field).
				Msg("submission: refused before assessment, it carries executable content")
			return true
		}
	}
	return false
}

// duplicateOfFloorRunes is the length below which identical text is not
// treated as a repeat.
//
// Two people writing the same short sentence is this register working, not an
// attack: "the same complaint recurring parish after parish is proof that the
// cause is structural" is the whole reason the thing exists, and a sentence is
// exactly the length at which two strangers plausibly reach for the same
// words. Two people writing the same four hundred characters did not both
// write them.
//
// The floor is what keeps a guard against flooding from quietly deleting the
// signal the register is built to collect. A short text repeated in bulk is
// still caught — by the classifier, which is what scores content.
const duplicateOfFloorRunes = 100

// duplicateOf returns the doléance this text repeats, or "" if it is new,
// too short to judge, or the check could not run.
//
// A failure here returns "", which lets the submission through as an ordinary
// one. That is the right way round: the cost of missing a duplicate is a row a
// curator deletes, and the cost of a database hiccup rejecting real text is
// somebody's grievance discarded on a technicality.
func (a *API) duplicateOf(ctx context.Context, text string) string {
	if utf8.RuneCountInString(text) < duplicateOfFloorRunes {
		return ""
	}

	original, err := a.store.FindDuplicate(ctx, content.Hash(text))
	if errors.Is(err, store.ErrMessageNotFound) {
		return ""
	}
	if err != nil {
		log.Error().Err(err).Msg("cannot check a submission for duplicates")
		return ""
	}

	// INFO: a slow-frequency state change carrying no content, and the one
	// line that makes a flood visible in the logs while it is happening.
	log.Info().Str("original", original.ID).
		Int("runes", utf8.RuneCountInString(text)).
		Msg("a doléance repeating an earlier one was dropped before assessment")
	return original.ID
}

// MessageItem is a doléance as the API reports it. It is a deliberate
// allowlist rather than the model: the model carries a token hash.
type MessageItem struct {
	ID        string     `json:"id"`
	Text      string     `json:"text"`
	Nickname  string     `json:"nickname,omitempty"`
	BirthYear int        `json:"birth_year,omitempty"`
	Activity  string     `json:"activity,omitempty"`
	Language  string     `json:"language,omitempty"`
	Status    string     `json:"status"`
	Subjects  []string   `json:"subjects,omitempty"`
	Place     string     `json:"place,omitempty"`
	Latitude  float64    `json:"latitude,omitempty"`
	Longitude float64    `json:"longitude,omitempty"`
	Geohash   string     `json:"geohash,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`

	// Likes is how many readers pressed "me too" — a count of presses, not of
	// people. See models.Message.Likes for why it cannot be anything else and
	// why nothing sorts by it.
	Likes int `json:"likes"`

	// Excerpt is the card-sized version of the text, derived on write and
	// stored, so a listing reads a column rather than cutting every row for
	// every visitor. Truncated says whether it cost anything.
	Excerpt   string `json:"excerpt"`
	Truncated bool   `json:"truncated"`

	// Verified says a human read this doléance and let it stand — set where a
	// curator accepts and nowhere else, so it is the difference between a
	// machine's confidence and a person's judgement.
	//
	// Always sent, never omitted, for the reason `kept` below is: a boolean
	// that disappears when false makes "no" and "not asked" one answer, and a
	// reader of this API cannot tell an unverified doléance from an older
	// server that did not have the column.
	Verified bool `json:"verified"`

	// Kept says whether the signed-in reader has this in their own list.
	//
	// **Not `omitempty`**, deliberately, where almost everything else here is.
	// A boolean that disappears when false makes "no" and "not asked"
	// the same answer on the wire, and a client decoding into a struct it
	// reused then keeps whatever was there before. That is not hypothetical:
	// it was found this way, by a checker that reported one reader seeing
	// another's bookmark when the field was simply absent.
	Kept bool `json:"kept"`
}

// MessageOutput is one doléance.
type MessageOutput struct {
	Body MessageItem
}

// MessageIDInput addresses one doléance.
type MessageIDInput struct {
	ID string `path:"id"`
}

// LikeOutput is the count after a press.
type LikeOutput struct {
	Body struct {
		Likes int `json:"likes"`
	}
}

// likeMessage records one more "me too".
//
// No identity is asked for and none is kept: the register's readers are
// anonymous, and giving a like somebody to belong to would mean holding an
// identity this project refuses. The browser remembers what it pressed; the
// server remembers only how often.
func (a *API) likeMessage(ctx context.Context, in *MessageIDInput) (*LikeOutput, error) {
	likes, err := a.store.LikeMessage(ctx, in.ID)
	if errors.Is(err, store.ErrMessageNotFound) {
		// The same answer for "no such doléance" and "not published", so this
		// cannot be used to find out that a submission exists.
		return nil, huma.Error404NotFound("no such doléance")
	}
	if err != nil {
		log.Error().Err(err).Str("message", in.ID).Msg("cannot record a like")
		return nil, huma.Error500InternalServerError("cannot record that")
	}

	out := &LikeOutput{}
	out.Body.Likes = likes
	return out, nil
}

// getMessage is deliberately **not** cached, and it is the one read on a hot
// path that is not.
//
// Two reasons, and the second is the one that decided it. It is a single row
// by primary key — the cheapest query this backend makes, where a listing
// joins the subjects, counts the unplaced and sorts — so there is very little
// to save. And it is where a "me too" lands: the reader is sent back to the
// card they pressed, and when that card is the doléance's own page they are
// looking straight at the number. Everywhere else a count may trail the TTL,
// because nothing is ranked by it and nobody memorised it; here somebody just
// did.
// reportMessage asks a human to look at a published doléance.
//
// # This is the only way somebody who did not write it can unpublish it
//
// Worth stating plainly, because the register is built against exactly that.
// The rule is that publication is immediate and irreversible by the operator:
// a curator may reject and nobody may quietly shelve. A report does not shelve —
// the text is untouched, the permalink keeps working, a curator decides in
// seconds and acceptance puts it back marked as read — but it does take a
// doléance off the register on one anonymous press, and that is a lever.
//
// What bounds it today is the frontend's per-address write limit and the
// curation queue behind it. That is enough for a mistaken press and **not**
// enough for somebody determined: at the default allowance one address can
// empty a page of the register in a minute, and a report costs nothing where
// writing a doléance costs somebody their afternoon.
//
// The missing piece is a count. Requiring several independent reports before a
// doléance comes off the register would turn one person's objection into a
// signal, and it is a threshold rather than a rewrite — the shape every other
// number in this project already has, configuration rather than a constant.
// Until that exists the honest description of this control is that it trusts
// its readers, which is a decision for whoever runs the register rather than
// one to bury in a handler.
//
// # The answer says nothing about what happened
//
// Not published, already reported, already verified, never existed: one 404
// for all of them. A reader learning which applied would learn the state of a
// submission that is not on the register, and reporting identifiers until one
// answered differently is how somebody maps the queue.
func (a *API) reportMessage(ctx context.Context, in *MessageIDInput) (*DoneOutput, error) {
	if err := a.store.ReportMessage(ctx, in.ID); err != nil {
		if errors.Is(err, store.ErrMessageNotFound) {
			return nil, huma.Error404NotFound("no such doléance")
		}
		log.Error().Err(err).Str("message", in.ID).Msg("cannot record a report")
		return nil, huma.Error500InternalServerError("cannot record that")
	}

	// WARN, not INFO. A published doléance leaving the register is the most
	// consequential thing an anonymous reader can cause, and a run where this
	// line appears in bulk is an attack in progress rather than a busy day.
	log.Warn().Str("message", in.ID).
		Msg("doléance reported by a reader: off the register until a curator looks")
	return done(), nil
}

func (a *API) getMessage(ctx context.Context, in *MessageIDInput) (*MessageOutput, error) {
	message, err := a.store.GetMessage(ctx, in.ID)
	if errors.Is(err, store.ErrMessageNotFound) {
		return nil, huma.Error404NotFound("no such doléance")
	}
	if err != nil {
		log.Error().Err(err).Str("message", in.ID).Msg("cannot read a doléance")
		return nil, huma.Error500InternalServerError("cannot read the doléance")
	}
	item := toMessageItem(message)
	item.Kept = a.markKept(ctx, models.BookmarkMessage, []string{message.ID})[message.ID]
	return &MessageOutput{Body: item}, nil
}

// MessageListInput bounds a listing.
//
// The bounds are a map viewport: the register page filters as the reader pans
// and zooms, so the list answers "what are people saying here" rather than
// "what has been said anywhere".
type MessageListInput struct {
	Limit int `query:"limit" default:"50"`

	// Bounds is "north,south,east,west" in degrees. One parameter rather than
	// four, because the four are meaningless apart: three edges describe no
	// box, and a missing one cannot be guessed. An empty value is the whole
	// register, which is what the page shows before anybody moves the map.
	//
	// (It is also a string because Huma does not accept pointer query
	// parameters — it panics at registration — so there is no *float64 to
	// tell "absent" from "zero" with.)
	Bounds string `query:"bounds" doc:"north,south,east,west in degrees"`

	Subjects []string `query:"subject" doc:"repeatable; a message carrying any of them matches"`

	// Unplaced asks only for the doléances that sit on no point of the map.
	// Exclusive with bounds, which is the opposite question — see
	// store.MessageQuery.Unplaced for why the register asks it separately.
	Unplaced bool `query:"unplaced" doc:"only doléances that named a country or region and no point"`

	// Order is "random" for an arbitrary selection, anything else for newest
	// first. The register offers both because they are exclusive: a shuffled
	// read cannot be paged — `random()` draws a fresh order per query, and
	// seeding it is not portable between the two engines — while a paged read
	// buries everything past the first page for ever.
	Order string `query:"order" doc:"random for an arbitrary selection, otherwise newest first"`

	// Offset pages through a newest-first read. Ignored with order=random,
	// where a second page would overlap the first with nothing to show it had.
	Offset int `query:"offset" doc:"ignored when order=random"`
}

// MessageListOutput is the published register.
type MessageListOutput struct {
	Body struct {
		Messages []MessageItem `json:"messages"`

		// Unplaced counts published doléances with no coordinates — somebody
		// who meant "France" gave a name, not a point. They cannot fall inside
		// any viewport, so the page says how many it is not showing rather
		// than hiding them silently.
		Unplaced int64 `json:"unplaced"`

		// More says a next page exists, inferred from this one being full.
		// Always false for a shuffled read, which has no pages.
		More bool `json:"more"`
	}
}

// OrderRandom asks for an arbitrary selection rather than the newest.
const OrderRandom = "random"

// MaxRegisterPage is how many doléances one read of the register returns.
//
// Forty is what somebody scrolls, and the ceiling matters more than the
// number: without one, a reader on a slow connection pays for a thousand rows
// they will never reach, and the shuffle — which cannot be paged — would be
// drawn from a pool big enough to make the page unusable rather than varied.
const MaxRegisterPage = 40

func (a *API) listMessages(ctx context.Context, in *MessageListInput) (*MessageListOutput, error) {
	// Forty is the ceiling whatever is asked for: this is a page somebody
	// reads, not an export — the register has its own download for that.
	limit := in.Limit
	if limit <= 0 || limit > MaxRegisterPage {
		limit = MaxRegisterPage
	}

	shuffled := in.Order == OrderRandom
	query := store.MessageQuery{
		Limit:    limit,
		Subjects: in.Subjects,
		Shuffled: shuffled,
		Offset:   in.Offset,
	}

	query.Unplaced = in.Unplaced
	if in.Bounds != "" && !in.Unplaced {
		box, err := parseBounds(in.Bounds)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity(err.Error())
		}
		query.Bounds = &box
	}

	// Everything the answer varies by, and the subjects sorted so that asking
	// for the same two in the other order is the same question.
	subjects := append([]string(nil), in.Subjects...)
	sort.Strings(subjects)
	key := cache.Keyed("messages.list", in.Bounds, strconv.Itoa(limit),
		strconv.Itoa(in.Offset), strings.Join(subjects, ","))

	type listing struct {
		messages []models.Message
		unplaced int64
	}
	read := func() (listing, error) {
		messages, unplaced, err := a.store.FindMessages(ctx, query)
		return listing{messages, unplaced}, err
	}

	// # A shuffled read is never cached
	//
	// The cache is keyed on what was asked for, and two readers asking for a
	// shuffle are asking the same question and want different answers. Served
	// from the cache they would get the same forty for the entry's lifetime,
	// and a reload — the whole point of offering a shuffle — would show
	// exactly what it showed a moment ago.
	//
	// It costs a query per shuffled request, which is what a shuffle is.
	var found listing
	var err error
	if shuffled {
		found, err = read()
	} else {
		found, err = cache.Fetch(a.cache, key, cache.Messages, read)
	}
	messages, unplaced := found.messages, found.unplaced
	if err != nil {
		log.Error().Err(err).Msg("cannot list the register")
		return nil, huma.Error500InternalServerError("cannot read the register")
	}

	ids := make([]string, 0, len(messages))
	for _, message := range messages {
		ids = append(ids, message.ID)
	}
	kept := a.markKept(ctx, models.BookmarkMessage, ids)

	out := &MessageListOutput{}
	out.Body.Unplaced = unplaced
	// A full page is the only evidence of a next one that does not cost a
	// COUNT over the whole register on every request. One empty "next" at the
	// exact multiple is a cheaper mistake than counting every time.
	out.Body.More = !shuffled && len(messages) == limit
	out.Body.Messages = make([]MessageItem, 0, len(messages))
	for _, message := range messages {
		item := toMessageItem(message)
		item.Kept = kept[message.ID]
		out.Body.Messages = append(out.Body.Messages, item)
	}
	return out, nil
}

// NearbyInput is a point and how far around it to look.
type NearbyInput struct {
	Latitude  float64 `query:"lat" required:"true"`
	Longitude float64 `query:"lng" required:"true"`
	Radius    float64 `query:"radius" default:"10000" doc:"metres"`
	Limit     int     `query:"limit" default:"50"`
}

func (a *API) listNearbyMessages(ctx context.Context, in *NearbyInput) (*MessageListOutput, error) {
	if in.Latitude < -90 || in.Latitude > 90 || in.Longitude < -180 || in.Longitude > 180 {
		return nil, huma.Error422UnprocessableEntity("that is not a point on Earth")
	}
	// An unbounded radius would be a table scan wearing a proximity query's
	// clothes; past this, the register listing is the right endpoint.
	if in.Radius <= 0 || in.Radius > 1_000_000 {
		return nil, huma.Error422UnprocessableEntity("the radius must be between 1 and 1000000 metres")
	}

	messages, err := a.store.NearbyMessages(ctx, in.Latitude, in.Longitude, in.Radius, in.Limit)
	if err != nil {
		log.Error().Err(err).Msg("cannot search near a point")
		return nil, huma.Error500InternalServerError("cannot search the register")
	}

	out := &MessageListOutput{}
	out.Body.Messages = make([]MessageItem, 0, len(messages))
	for _, message := range messages {
		out.Body.Messages = append(out.Body.Messages, toMessageItem(message))
	}
	return out, nil
}

// MessageDeleteInput carries the token that authorises removal.
type MessageDeleteInput struct {
	ID    string `path:"id"`
	Token string `query:"token" doc:"the deletion token issued at submission"`
}

// MessageDeleteOutput is empty; the status is the answer.
type MessageDeleteOutput struct {
	Status int
}

func (a *API) deleteMessage(ctx context.Context, in *MessageDeleteInput) (*MessageDeleteOutput, error) {
	message, err := a.store.GetMessage(ctx, in.ID)
	if errors.Is(err, store.ErrMessageNotFound) {
		return nil, huma.Error404NotFound("no such doléance")
	}
	if err != nil {
		return nil, huma.Error500InternalServerError("cannot read the doléance")
	}

	// The same answer for a wrong token and a missing message, so that this
	// endpoint cannot be used to discover which identifiers exist.
	if !token.Verify(in.Token, message.TokenHash) {
		log.Warn().Str("message", in.ID).Msg("deletion refused: token does not match")
		return nil, huma.Error404NotFound("no such doléance")
	}

	if err := a.store.DeleteMessage(ctx, in.ID); err != nil {
		log.Error().Err(err).Str("message", in.ID).Msg("cannot delete a doléance")
		return nil, huma.Error500InternalServerError("cannot delete the doléance")
	}

	log.Info().Str("message", in.ID).Msg("doléance deleted by its author")
	return &MessageDeleteOutput{Status: http.StatusNoContent}, nil
}

// resolveLocation coarsens a pinned point to the precision the contributor's
// zoom implies, and names the place at that same granularity.
//
// The coarsening is the privacy guarantee, and it is done here — on the server,
// before anything is written — because a browser's promise to be imprecise is
// not one anybody has to keep. What reaches the database is the centre of a
// cell, so the exact point is not stored, not returned by the API, and not in
// any snapshot: it is not kept anywhere to be leaked later.
func (a *API) resolveLocation(ctx context.Context, lat, lng float64, zoom int, place, country string) *models.Location {
	precision := geo.PrecisionForZoom(zoom)

	location := &models.Location{Label: place, CountryCode: country}

	// Coordinates are kept only where they still mean a point. Coarser than
	// that, the pin is dropped and the name is the whole of the answer:
	// somebody who meant "France" gave us a country, not a latitude, and
	// storing the centre of a 1250 km cell would put them in another one.
	if precision >= geo.MappablePrecision {
		location.Latitude, location.Longitude = geo.Coarsen(lat, lng, precision)
	}

	if a.geocoder == nil {
		return location
	}

	// The lookup uses the point the contributor actually pinned, not the
	// coarsened one. Naming the coarsened point would answer a question
	// nobody asked — the centre of the cell is in a different commune, and
	// near a border, a different country.
	named, err := a.geocoder.Reverse(ctx, lat, lng, precision)
	if err != nil {
		log.Warn().Err(err).Msg("reverse geocoding failed; storing the location unnamed")
		return location
	}
	if named.Label != "" {
		location.Label = named.Label
	}
	if named.CountryCode != "" {
		location.CountryCode = named.CountryCode
	}
	return location
}

func toMessageItem(m models.Message) MessageItem {
	item := MessageItem{
		ID:        m.ID,
		Text:      m.Text,
		Nickname:  m.Nickname,
		BirthYear: m.BirthYear,
		Activity:  m.Activity,
		Language:  m.Language,
		Status:    string(m.Status),
		CreatedAt: m.CreatedAt,
		ExpiresAt: m.ExpiresAt,
		Likes:     m.Likes,
		Excerpt:   m.Excerpt,
		Truncated: m.Truncated,
		Verified:  m.Verified,
	}
	for _, subject := range m.Subjects {
		item.Subjects = append(item.Subjects, subject.Slug)
	}
	if m.Location != nil {
		item.Place = m.Location.Label
		item.Latitude = m.Location.Latitude
		item.Longitude = m.Location.Longitude
		item.Geohash = m.Location.Geohash
	}
	return item
}

// parseBounds reads "north,south,east,west".
func parseBounds(raw string) (geo.Box, error) {
	parts := strings.Split(raw, ",")
	if len(parts) != 4 {
		return geo.Box{}, errors.New("bounds must be north,south,east,west")
	}

	var edges [4]float64
	for i, part := range parts {
		value, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err != nil {
			return geo.Box{}, errors.New("bounds must be four numbers")
		}
		edges[i] = value
	}

	box := geo.Box{North: edges[0], South: edges[1], East: edges[2], West: edges[3]}
	if box.North < box.South {
		return geo.Box{}, errors.New("north is south of south")
	}
	if box.North > 90 || box.South < -90 {
		return geo.Box{}, errors.New("bounds are off the planet")
	}
	return box, nil
}

// trimTo trims whitespace and caps a field at a rune count, so a long value is
// shortened rather than rejected.
func trimTo(value string, limit int) string {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	return string([]rune(value)[:limit])
}
