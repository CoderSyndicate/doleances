package models

import "time"

// Subject is a theme assigned by the classifier. A message carries several:
// a grievance about a closed rural maternity ward is about healthcare and
// rural isolation and public services at once.
type Subject struct {
	Model

	// Slug is the stable identity: it appears in query strings and must not
	// change for the life of the subject.
	Slug string `gorm:"uniqueIndex;size:64" json:"slug"`

	// Label is what a reader sees, in the language it first arrived in. A
	// curator may change it; the slug stays.
	Label string `gorm:"size:128" json:"label"`

	// Language is what the doléance that first proposed this subject was
	// written in. Kept so a curator merging "santé" and "Gesundheit" can see
	// what they are looking at.
	Language string `gorm:"size:16;index" json:"language,omitempty"`

	// MatchKey is the tolerant form two spellings of one thing share — the
	// first and cheapest layer of deduplication. Unique, because that is the
	// whole point: a second "Santé" cannot become a second row.
	//
	// It is NOT an identifier. Matching collapses differences and identity
	// preserves them; the slug is the identity.
	MatchKey string `gorm:"uniqueIndex;size:128" json:"-"`

	// FoldKey is the match key with a trailing plural marker removed, the
	// second layer. Indexed but not unique: "transport" and "transports"
	// intentionally share one, and the first to arrive wins the row.
	FoldKey string `gorm:"index;size:128" json:"-"`

	// QIDConfirmedAt is when a person confirmed the entity. Nil means no
	// entity, because an unconfirmed one is not attributed at all: see
	// models.SubjectEntity for what an unconfirmed pipeline did to this
	// column, and what it cost.
	QIDConfirmedAt *time.Time `gorm:"index" json:"qid_confirmed_at,omitempty"`

	// QID is the Wikidata identity of this subject, when one was found —
	// "Q12147" for health, whatever language the label is in. Two subjects
	// sharing a QID are the same subject, which is the only thing this column
	// is used to decide: an absent or differing QID says nothing, because
	// plenty of real subjects have no Wikidata entry at all.
	//
	// Set only once a curator has confirmed it, never by the pipeline alone.
	//
	// Indexed, not unique: two rows may briefly share one before the merge
	// that the sharing triggers.
	QID string `gorm:"index;size:32" json:"qid,omitempty"`

	// Embedding is the vector for the third layer, stored as raw float32s.
	// Empty when the embedding call failed — a subject with no vector simply
	// takes no part in similarity matching rather than blocking anything.
	Embedding []byte `json:"-"`

	// EmbeddingModel records which model produced the vector. Changing models
	// changes the space, and comparing across two of them silently produces
	// nonsense — so a vector from an unknown model is ignored rather than
	// trusted.
	EmbeddingModel string `gorm:"size:128" json:"-"`
}

// Reasons a submission was refused before any classifier saw it.
const (
	DropDuplicate = "duplicate"
	DropPayload   = "payload"
)

// Grounds the classifier reports a submission cannot be published on. They are
// written to the same column as the two above, so one field answers "why is
// this on the dropped page" whoever decided it.
const (
	DropThreat     = "threat"
	DropContact    = "contact"
	DropIdentifies = "identifies"
)

// WithholdsText says the dropped sample must not show this submission's words
// on a public page.
//
// # The page is public, and two of these categories exist to protect somebody
//
// A dropped submission is listed publicly so that anybody can see what the
// filter refused and say it got it wrong. That is worth having, and it cannot
// extend to every category, because two of them are refusals *about
// publication*:
//
//   - contact and identifies are refused precisely because publishing the
//     text would put a real person's telephone number, address or identity in
//     front of everybody. Reproducing it on the page that explains the refusal
//     would perform the exact harm the refusal prevented — and would do it
//     with no consent from the person named, who is not the author.
//   - payload is an attack rather than a text. There is nothing in it for a
//     reader to judge, and the sample stores it exactly as sent so that a
//     curator can check the filter: that is a working specimen, and it belongs
//     in the console rather than on a page anybody can fetch.
//
// Everything else shows its words, including threat — behind a press, never
// by surprise, but shown. That is the category most likely to be applied
// wrongly to an angry doléance about immigration or religion, which this
// register explicitly protects, so it is the one where a reader most needs to
// be able to look and disagree.
func WithholdsText(dropReason string) bool {
	switch dropReason {
	case DropPayload, DropContact, DropIdentifies:
		return true
	}
	return false
}

// HidesTextUntilAsked says the words are shown only when a reader presses for
// them: refused as an attack on people, which nobody should meet unwarned.
func HidesTextUntilAsked(dropReason string) bool {
	return dropReason == DropThreat
}

// Message is a doléance.
//
// A doléance is dated by when it was written, and by nothing else. It is a
// snapshot of how somebody felt on the day they wrote it: if a closure from
// four years ago still shapes their life today, that is a grievance of today,
// and asking them to date the underlying event would only invite the reader to
// discount it as old news. CreatedAt is therefore the date, and there is no
// separate "when did this happen" to disagree with it.
//
// Anonymity is the default: Nickname is optional and nothing else identifies
// the author. The deletion token is the only thing tying a person to their
// message, and only a hash of it is kept — so the site itself cannot edit or
// delete on their behalf.
type Message struct {
	Model

	// Nickname is what the contributor chose to be called, if anything.
	Nickname string `gorm:"size:128" json:"nickname,omitempty"`

	// BirthYear and Activity are optional, and they are the only things the
	// register asks about the person rather than the grievance. They exist
	// because a register is read in aggregate: "this is what people of this
	// age, doing this, are saying" is the finding, and 1789 is legible today
	// largely because we know who was holding the pen.
	//
	// It is **Activity**, never "profession" or "occupation". A large part of
	// the people this register is for are retired, unemployed, studying, or
	// caring for somebody unpaid; a field called profession tells them the
	// question is not about them, and the answer it invites — "none" — is both
	// false and insulting. What somebody's days are made of is the question.
	//
	// These are also the fields that most weaken anonymity: a trade plus an
	// age plus a village can identify somebody with no name attached at all.
	// Both default to unset, neither is ever required, and nothing in the flow
	// should push for them.
	BirthYear int    `gorm:"index" json:"birth_year,omitempty"`
	Activity  string `gorm:"size:128;index" json:"activity,omitempty"`

	// Location is where the grievance belongs, if the contributor pinned one.
	Location *Location `gorm:"embedded;embeddedPrefix:location_" json:"location,omitempty"`

	// Text is the grievance, or the story. There is no length limit by
	// design: some people will write a complaint, some will write their life.
	Text string `gorm:"type:text" json:"text"`

	// Language is the language the text is written in, which is separate from
	// the UI language of whoever wrote or reads it.
	//
	// It is **empty until the classifier reports it**, and deliberately not
	// taken from the page the contributor used. Those differ: somebody writing
	// German on the French interface is writing German, and only something
	// that has read the text can say so. A guess here would be worse than a
	// blank, because it travels — into the subject vocabulary, into the
	// similarity thresholds that depend on knowing the language, and into
	// every snapshot.
	//
	// So a submission carries no language until it has been classified, and
	// anything reading this field has to tolerate an empty one.
	Language string `gorm:"size:16;index" json:"language,omitempty"`

	Status ReviewStatus `gorm:"size:16;index" json:"status"`

	// Confidence is the curation assessment's score, 0-100.
	Confidence int `json:"confidence"`

	// AssessedAt and AssessedBy record that a model ruled on this, and which
	// one. They are here rather than in the audit log because that log is a
	// record about privileged humans — a machine decision is not one, and
	// mixing them would make "who decided this" unanswerable.
	//
	// AssessedBy is the model name, so a decision can be traced to a model
	// version when thresholds are tuned or a model is changed.
	AssessedAt *time.Time `gorm:"index" json:"assessed_at,omitempty"`
	AssessedBy string     `gorm:"size:128" json:"assessed_by,omitempty"`

	// AssessmentAttempts counts how often assessment has failed for this
	// submission. Past a cap it goes to a human rather than waiting for ever
	// on a model that cannot answer.
	AssessmentAttempts int `json:"-"`

	// ClassifiedAt records that the subjects were successfully read off this
	// doléance. It is the *only* mark of that, and it is set nowhere else:
	// having no subjects is not evidence of failure, because "this text is
	// about nothing in particular" is a real answer the classifier gives.
	//
	// A published doléance with no classification time is therefore one whose
	// classification failed — the model was unreachable, or answered with
	// something unusable — and the retry sweep is what finds it. Without this
	// column a failure is indistinguishable from a legitimately subject-less
	// message, and the doléance stays unfindable for ever with nothing to say
	// why.
	ClassifiedAt *time.Time `gorm:"index" json:"classified_at,omitempty"`

	// ClassificationAttempts stops a text the classifier can never handle from
	// being retried every quarter of an hour for the life of the register.
	ClassificationAttempts int `json:"-"`

	// Subjects come from the classifier, never from the contributor.
	Subjects []Subject `gorm:"many2many:message_subjects" json:"subjects,omitempty"`

	// TokenHash authenticates the contributor for editing and deletion. The
	// token itself is shown once, at submission, and never stored.
	TokenHash string `gorm:"size:64;index" json:"-"`

	// ContentHash identifies the text, for recognising the same doléance
	// submitted again under another name. Derived, never supplied — the same
	// rule as the geohash, and for the same reason: a hash that disagrees with
	// its own text silently stops matching anything.
	//
	// Indexed, not unique. A duplicate is a row like any other; what differs
	// is that it never reaches the register. Making the column unique would
	// refuse the write, and the contributor would learn from an error message
	// that their text was already here — which is exactly what the flood is
	// probing for.
	ContentHash string `gorm:"size:64;index" json:"-"`

	// Likes counts the readers who pressed "me too".
	//
	// It is a count of presses, not of people, and it cannot be anything else:
	// a register whose readers are anonymous has nobody to attribute a like
	// to, and giving it somebody would mean an identity this project refuses
	// to hold. No address, no session, no address hash — the browser
	// remembers what it pressed and the server remembers only how often.
	//
	// That makes it soft, and it is worth being honest about what it is for
	// rather than pretending otherwise. It is not a ranking: nothing sorts by
	// it, and nothing should. It is the reader's half of the sentence the
	// whole register is built on — *this happened to me too* — and its value
	// is to the person who wrote the doléance, who finds out they were not
	// the only one.
	Likes int `gorm:"not null;default:0" json:"likes"`

	// Pleas counts the readers who saw this on the dropped page and said the
	// filter got it wrong.
	//
	// A count of presses rather than of people, on exactly the terms the "me
	// too" count is: the register's readers are anonymous, so there is nobody
	// to attribute one to, and giving it somebody would mean holding the one
	// record this project refuses — a list of which refusals a named reader
	// objected to. The browser remembers what it pressed so nobody presses
	// twice by accident; somebody determined can press again.
	//
	// Nothing is decided by the number. It sorts a dropped submission to the
	// top of the curator's page and does nothing else: a refusal overturned by
	// a show of hands would be a register where the loudest group decides what
	// may be written, which is the opposite of what the curation queue is for.
	Pleas int `gorm:"not null;default:0" json:"pleas"`

	// Excerpt is the card-sized version of the text, and Truncated says
	// whether making it cost anything.
	//
	// Stored rather than computed when a page is drawn, for the reason the
	// geohash and the content hash are stored: a listing reads a column
	// instead of doing work per row, per visitor. Derived in BeforeSave so no
	// write path can forget — a seed, a snapshot restore and an accepted
	// revision all produce a row whose excerpt matches its own text.
	Excerpt   string `gorm:"type:text" json:"excerpt"`
	Truncated bool   `json:"truncated"`
	// DuplicateOf names the earlier doléance this one repeats, and is empty
	// for everything else. It is what lets a curator looking at the dropped
	// sample see *why* this one is there and read the original beside it —
	// two people writing the same sentence is the register working, and two
	// hundred copies of one paragraph is not.
	DuplicateOf string `gorm:"size:36;index" json:"duplicate_of,omitempty"`

	// Why a submission was refused before any classifier saw it. Empty means
	// the classifier's score was the whole story.
	//
	//	DropDuplicate  the same text had been sent here before
	//	DropPayload    it carried content whose purpose is to run, not be read

	// AssessmentReason is the model's own sentence about its verdict, kept
	// because a score on its own cannot be argued with.
	//
	// The dropped sample exists so somebody notices the filter eating real
	// doléances, and "30" does not tell them anything: "reads as advertising
	// for a named company" does. The same holds in the queue — a curator
	// deciding in seconds is deciding about a text they have not read twice,
	// and the model's reason is the fastest way in.
	//
	// It is a note about a decision, never shown to the contributor: telling
	// somebody why a machine doubted their words would be worse than saying
	// nothing.
	AssessmentReason string `gorm:"size:512" json:"-"`

	// DropReason says which guard refused a submission before any classifier
	// saw it, and is empty when the classifier's score is the whole story.
	//
	// It matters on the dropped sample: a score of 0 beside a submission
	// nothing ever scored reads as "the model hated this", and a curator
	// deciding whether the filter is eating real doléances needs to know the
	// difference between a judgement and an arithmetic fact.
	DropReason string `gorm:"size:16" json:"drop_reason,omitempty"`

	// ExpiresAt deletes the message on that date. Nil means the contributor
	// accepted permanent storage, which is the default the form states plainly.
	ExpiresAt *time.Time `gorm:"index" json:"expires_at,omitempty"`

	PublishedAt *time.Time `json:"published_at,omitempty"`

	// Verified says a human has read this doléance and let it stand.
	//
	// It is set where a curator accepts and nowhere else, which is what makes
	// it mean something: most of the register publishes on a score above the
	// accept threshold with nobody involved, so "verified" is the difference
	// between a machine's confidence and a person's judgement. A reader can
	// see which they are looking at.
	//
	// It is deliberately **not** a quality mark and must never be ranked by.
	// The register is ordered by when a doléance was written and by nothing
	// else; sorting unverified text below verified would mean a doléance
	// nobody had got round to reading counted for less than its neighbour,
	// which is the opposite of what the aggregation is for.
	//
	// A verified doléance also cannot be reported — the control is replaced by
	// the mark — so this is what stops a reader sending the same text back to
	// a curator who has already ruled on it.
	Verified bool `json:"verified"`
}

// MessageRevision is an edit waiting to be reviewed.
//
// It lives apart from the published message so that an edit never blanks a
// doléance out of the register while it waits for a curator: the published
// version stays visible and untouched until the revision is accepted.
type MessageRevision struct {
	Model

	// MessageID is unique: a second edit overwrites the pending one rather
	// than queueing behind it, so what gets reviewed is the latest intent.
	MessageID string `gorm:"uniqueIndex;size:36" json:"message_id"`

	Nickname  string     `gorm:"size:128" json:"nickname,omitempty"`
	BirthYear int        `json:"birth_year,omitempty"`
	Activity  string     `gorm:"size:128" json:"activity,omitempty"`
	Location  *Location  `gorm:"embedded;embeddedPrefix:location_" json:"location,omitempty"`
	Text      string     `gorm:"type:text" json:"text"`
	Language  string     `gorm:"size:16" json:"language,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`

	Status     ReviewStatus `gorm:"size:16;index" json:"status"`
	Confidence int          `json:"confidence"`
}

// HistoricalText is a doléance from the past, shown alongside the present-day
// register. Translations are generated so every text reaches every audience.
type HistoricalText struct {
	Model

	// Title is the label of this passage.
	Title string `gorm:"size:256" json:"title"`
	Text  string `gorm:"type:text" json:"text"`

	// DocumentTitle names the document the passage was taken from. It belongs
	// with the date, not buried in the citation: a reader should know what
	// they are reading before they read it.
	DocumentTitle string `gorm:"size:256" json:"document_title"`

	// Source is the bibliographic reference — edition, pages, archive.
	Source string `gorm:"size:512" json:"source"`

	// Language is the language of the original text.
	Language string `gorm:"size:16;index" json:"language"`

	// Period and Region place the text in words: "1789", "Bretagne".
	Period string `gorm:"size:64;index" json:"period,omitempty"`
	Region string `gorm:"size:128;index" json:"region,omitempty"`

	// Location places it on the map, which is what lets a passage sit in the
	// register beside the doléances written where it was written.
	//
	// It is the **commune the document belongs to**, not a building: a cahier
	// was written for a whole parish and signed by its inhabitants, so there
	// is no door to point at and inventing one would be precise and wrong.
	// The coarsening that protects a living contributor has no purchase here
	// either — there is nobody behind this coordinate, and the people who
	// wrote it put the name of their town at the top of the page themselves.
	Location *Location `gorm:"embedded;embeddedPrefix:location_" json:"location,omitempty"`

	// Placeholder marks seeded example content, so it can never be mistaken
	// for real archival material.
	Placeholder bool `gorm:"index" json:"placeholder"`

	// Likes counts the readers who recognised themselves in it.
	//
	// The same count as a doléance's, and the same reasons — presses rather
	// than people, nothing ranked by it. It means something slightly different
	// here and the difference is the point of the corpus: somebody saying
	// "this happened to me too" about 1789 is saying the two registers are one
	// act performed twice, which is the whole claim this project makes.
	Likes int `gorm:"not null;default:0" json:"likes"`

	Translations []HistoricalTranslation `gorm:"constraint:OnDelete:CASCADE" json:"translations,omitempty"`
}

// HistoricalTranslation is one rendering of a historical text into another
// language — generated, or supplied with the corpus.
type HistoricalTranslation struct {
	Model

	HistoricalTextID string `gorm:"index;size:36" json:"historical_text_id"`
	Language         string `gorm:"size:16;index" json:"language"`

	Title string `gorm:"size:256" json:"title"`
	Text  string `gorm:"type:text" json:"text"`

	// DocumentTitle, Region and Source are translated too, so a reader is not
	// handed a citation in a language they do not read.
	DocumentTitle string `gorm:"size:256" json:"document_title,omitempty"`
	Region        string `gorm:"size:128" json:"region,omitempty"`
	Source        string `gorm:"size:512" json:"source,omitempty"`
}
