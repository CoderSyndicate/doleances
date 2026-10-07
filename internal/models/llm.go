package models

// LLMSettingsID is the fixed identifier of the single settings row. The
// service has one LLM configuration, not a collection of them.
const LLMSettingsID = "llm"

// LLMSettings is how the backend reaches the language model.
//
// The project talks to a Synergia instance rather than to OpenAI directly —
// it exposes an OpenAI-compatible API, so an OpenAI client pointed at the
// configured base URL is the right shape.
type LLMSettings struct {
	Model

	// BaseURL is the OpenAI-compatible endpoint, e.g. a Synergia instance.
	BaseURL string `gorm:"size:512" json:"base_url"`

	// APIKey authenticates to it. It is never returned by the API: the
	// console shows only whether one is set, and writes a replacement.
	//
	// The key lives in the database, so a database backup contains it. Treat
	// backups accordingly.
	APIKey string `gorm:"size:512" json:"-"`

	// ClassificationModel scores submissions and assigns subjects.
	ClassificationModel string `gorm:"size:128" json:"classification_model"`

	// TranslationModel translates historical texts. Usually the same model,
	// kept separate so one can be changed without the other.
	TranslationModel string `gorm:"size:128" json:"translation_model"`

	// TimeoutSeconds bounds a single call.
	TimeoutSeconds int `json:"timeout_seconds"`

	// AcceptThreshold and CurateThreshold are the confidence boundaries of the
	// curation assessment, on a 0-100 scale: above accept it is published,
	// above curate a human decides, below it is dropped as spam.
	//
	// They are settings rather than constants because they are placed against
	// a particular model's score distribution, and no two models put their
	// steps in the same places.
	//
	// # Put a threshold in a gap, never on a value
	//
	// Measured over three consecutive corpus runs, this model emits only 0,
	// 10, 20, 30, 60, 65, 85, 95 and 98, and it wobbles between adjacent
	// values on identical text.
	//
	// The curate threshold was 60 — a value the model actually emits. With the
	// rule being `score > threshold`, a 60 dropped and a 65 reached a human, so
	// a five-point wobble decided between "somebody reads this" and "nobody
	// ever does". Two abuse cases flipped between runs for no other reason.
	//
	// It is now 45: the middle of the empty band between 30 and 60, so the
	// largest wobble observed cannot flip a decision either way. Where the move
	// changes an outcome at all it sends a submission to a human, which is the
	// direction this project prefers by its own rule — a wrongly refused
	// doléance is somebody's words thrown away, a wrongly queued one costs a
	// curator a moment.
	//
	// Three further runs confirmed it: cases with an identical outcome in all
	// three went from 48 of 53 to 50, and both cases that had been flipping
	// settled. The totals barely moved (49/48/50 before, 48/49/49 after), and
	// that is the point — **the change bought determinism, not accuracy.**
	// Nothing became more correct; the same decisions stopped being taken at
	// random.
	//
	// The accept threshold stays at 90. It already sits in the gap between 85
	// and 95, and the ±10 wobble straddles it: that is the model declining to
	// be more precise than "confident" and "very confident", and the queue is
	// where that belongs. No number fixes it.
	AcceptThreshold int `json:"accept_threshold"`
	CurateThreshold int `json:"curate_threshold"`

	// GroupAcceptThreshold and GroupCurateThreshold are the same boundaries
	// for groups, and they are their own settings rather than the message
	// pair reused.
	//
	// A group is judged on far less: a name, a place and a few words from
	// somebody who has already proved they own the contact address. There is
	// little to get wrong and a great deal to lose by being strict — a
	// wrongly refused group is a meeting nobody hears about — so the accept
	// bar sits lower and most groups never reach a human.
	//
	// Separate fields because a threshold that has to serve two questions
	// gets tuned for whichever one is noticed first. They start where the
	// message pair starts; they are expected to diverge.
	GroupAcceptThreshold int `json:"group_accept_threshold"`
	GroupCurateThreshold int `json:"group_curate_threshold"`

	// ActionAcceptThreshold and ActionCurateThreshold are the same boundaries
	// again, for the third submittable type.
	//
	// They start where the group ones do and are expected to move apart: an
	// action is judged on whether it serves people coming together to discover
	// they are not alone, which is a narrower question than whether a group is
	// real, and a narrower question usually needs a different line.
	ActionAcceptThreshold int `json:"action_accept_threshold"`
	ActionCurateThreshold int `json:"action_curate_threshold"`

	// EmbeddingModel deduplicates subjects. It is multilingual on purpose:
	// "santé", "Gesundheit" and "health" are one subject, and no amount of
	// normalising would ever discover that.
	EmbeddingModel string `gorm:"size:128" json:"embedding_model"`

	// SubjectMergeThreshold and SubjectSuggestThreshold are cosine
	// boundaries. Above merge, a proposed subject joins an existing one
	// silently; between the two it is created and a curator is asked whether
	// they are the same; below, it is simply new.
	//
	// The defaults come from measuring bge-m3 on real subject labels rather
	// than from intuition, and the measurement said something worth writing
	// down: **there is no cosine that separates "the same subject" from "a
	// different subject".** The classes overlap.
	//
	//	transport ~ transports      0.958   same
	//	accès soins ~ accès aux soins 0.922  same
	//	santé ~ health              0.899   same, another language
	//	cheval ~ chevaux            0.881   same, irregular plural
	//	école ~ éducation           0.777   arguably same
	//	travail ~ travaux           0.755   same, irregular plural
	//	transport ~ bus             0.737   one contains the other
	//	santé ~ accès aux soins     0.647   one contains the other
	//	logement ~ habitat          0.601   same
	//	transport ~ école           0.567   unrelated
	//	santé ~ logement            0.518   unrelated
	//
	// Note what the middle of that list is: "transport ~ bus" and "santé ~
	// accès aux soins" are not different subjects, they are a subject and
	// something inside it. A doléance about a bus is a doléance about
	// transport. So the question a cosine cannot answer is not only "are these
	// the same?" but "is one of these inside the other?", and the answer
	// changes what should happen — merging into the broader term is right,
	// merging into the narrower one is wrong, and the number says nothing
	// about which is which.
	//
	// The classifier is therefore asked to write the **broader** term in the
	// first place, which removes most of these pairs before they exist. What
	// still gets through is a suggestion for a person, not an automatic merge:
	// "logement ~ habitat" at 0.601 sits below "transport ~ bus" at 0.737, so
	// any threshold that merges the first also merges the second.
	//
	// The suggest threshold sits just above where unrelated pairs topped out
	// (0.567). Below it, two labels are confidently different; above, it is a
	// judgement about language, and a judgement belongs to a curator.
	//
	// Both are settings, and both should be re-measured if the embedding model
	// changes: these numbers describe bge-m3 on short labels and nothing else.
	SubjectMergeThreshold   float64 `json:"subject_merge_threshold"`
	SubjectSuggestThreshold float64 `json:"subject_suggest_threshold"`

	// SubjectSuggestCrossLanguage is the suggest threshold used when the two
	// labels were written in different languages.
	//
	// It is lower, and measurement says it has to be. Across 276 measured
	// pairs, bge-m3 scores cross-language comparisons systematically lower
	// than same-language ones — both the matches and the mismatches shift
	// down — so one number applied to both either floods a curator with
	// same-language noise or never notices a translation:
	//
	//	same subject, cross language   n=24   min 0.653  median 0.789
	//	different subject, same lang   n=84   max 0.725
	//	different subject, cross lang  n=168  max 0.680
	//
	// Cross-language separates almost cleanly at 0.68, and the same-language
	// population does not separate at all: "health ~ safety" (different) beats
	// "logement ~ habitat" (same). So the same-language threshold is set where
	// only a near-duplicate phrasing reaches it, and the cross-language one
	// where a translation does.
	SubjectSuggestCrossLanguage float64 `json:"subject_suggest_cross_language"`
}

// DefaultLLMSettings is what a fresh installation starts from.
func DefaultLLMSettings() LLMSettings {
	return LLMSettings{
		Model:               Model{ID: LLMSettingsID},
		ClassificationModel: "Ministral-3-14B-Instruct-2512",
		TranslationModel:    "Ministral-3-14B-Instruct-2512",
		TimeoutSeconds:      60,
		AcceptThreshold:     90,
		CurateThreshold:     45,

		// Lower than the message bar on purpose: see the field comment.
		GroupAcceptThreshold:  80,
		GroupCurateThreshold:  45,
		ActionAcceptThreshold: 80,
		ActionCurateThreshold: 45,
		EmbeddingModel:        "bge-m3",

		SubjectMergeThreshold:   0.95,
		SubjectSuggestThreshold: 0.85,

		SubjectSuggestCrossLanguage: 0.60,
	}
}

// WithThresholdDefaults fills in any similarity threshold that arrived unset.
//
// A threshold read as zero is not a permissive setting, it is a missing one —
// and the difference matters because "merge anything scoring above 0" merges
// everything. That is how a field added to this struct but forgotten in the
// configuration path turns into a vocabulary collapsing into one subject, with
// no error anywhere to say why.
//
// So zero, negative and out-of-range values are replaced by the default rather
// than used. An operator who genuinely wants a layer switched off sets it to a
// value above 1, which nothing can score.
func (s LLMSettings) WithThresholdDefaults() LLMSettings {
	defaults := DefaultLLMSettings()

	// The message pair is seeded from configuration on first start, so in
	// practice it is always set — but "in practice" is what the rest of this
	// function exists to stop relying on. An accept threshold of zero means
	// `score > 0`, which publishes everything a model does not score at
	// exactly zero, and it would look identical to a working register.
	if s.AcceptThreshold <= 0 || s.AcceptThreshold > 100 {
		s.AcceptThreshold = defaults.AcceptThreshold
	}
	if s.CurateThreshold <= 0 || s.CurateThreshold > 100 {
		s.CurateThreshold = defaults.CurateThreshold
	}

	// Group thresholds were added after the settings row existed, so an
	// installation that predates them holds zeros — which would mean "accept
	// everything". Zero is a missing setting here as everywhere else.
	if s.GroupAcceptThreshold <= 0 || s.GroupAcceptThreshold > 100 {
		s.GroupAcceptThreshold = defaults.GroupAcceptThreshold
	}
	if s.GroupCurateThreshold <= 0 || s.GroupCurateThreshold > 100 {
		s.GroupCurateThreshold = defaults.GroupCurateThreshold
	}
	if s.ActionAcceptThreshold <= 0 || s.ActionAcceptThreshold > 100 {
		s.ActionAcceptThreshold = defaults.ActionAcceptThreshold
	}
	if s.ActionCurateThreshold <= 0 || s.ActionCurateThreshold > 100 {
		s.ActionCurateThreshold = defaults.ActionCurateThreshold
	}

	if s.SubjectMergeThreshold <= 0 {
		s.SubjectMergeThreshold = defaults.SubjectMergeThreshold
	}
	if s.SubjectSuggestThreshold <= 0 {
		s.SubjectSuggestThreshold = defaults.SubjectSuggestThreshold
	}
	if s.SubjectSuggestCrossLanguage <= 0 {
		s.SubjectSuggestCrossLanguage = defaults.SubjectSuggestCrossLanguage
	}
	if s.EmbeddingModel == "" {
		s.EmbeddingModel = defaults.EmbeddingModel
	}
	return s
}
