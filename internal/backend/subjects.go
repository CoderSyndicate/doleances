package backend

import (
	"context"
	"errors"

	"github.com/rs/zerolog/log"

	"github.com/CoderSyndicate/doleances/internal/llm"
	"github.com/CoderSyndicate/doleances/internal/models"
	"github.com/CoderSyndicate/doleances/internal/store"
	"github.com/CoderSyndicate/doleances/internal/subjects"
)

// resolveSubjects turns the labels a model proposed into rows in the
// vocabulary, merging what is already there.
//
// # What merges by itself, and what does not
//
// Only string identity merges without asking: the same label, and the same
// label in the plural. Everything else — a shared Wikidata entity, a close
// vector, anything across two languages — becomes a question for a curator,
// and the subject is created meanwhile so no doléance loses its classification
// waiting for an answer.
//
// That line is drawn where it is because the two mistakes are not symmetrical.
// An unanswered question is a duplicate in a filter list, visible to anybody
// who looks and fixed in seconds. A wrong merge silently rewrites what the
// register appears to say, and nobody ever notices, because the evidence that
// it was wrong is exactly the row that was deleted.
//
// The cost of asking falls away on its own: questions arise only for labels
// the vocabulary has never seen, and by the second week most of what a model
// proposes already exists and exits at layer one for free.
func (a *API) resolveSubjects(ctx context.Context, client *llm.Client, message models.Message,
	labels []string, language string, settings models.LLMSettings) []string {

	if len(labels) == 0 {
		return nil
	}

	var (
		resolved []string
		unknown  []candidate // labels no exact layer recognised
	)

	for _, label := range labels {
		key := subjects.MatchKey(label)
		if key == "" {
			continue
		}

		// Layer 1: the exact match key. One indexed lookup, and the common
		// case once the vocabulary has settled.
		if subject, err := a.store.FindSubjectByMatchKey(ctx, key); err == nil {
			log.Debug().Str("label", label).Str("matched", subject.Label).
				Str("layer", "match-key").Msg("subjects: resolved")
			resolved = append(resolved, subject.ID)
			continue
		} else if !errors.Is(err, store.ErrSubjectNotFound) {
			log.Error().Err(err).Msg("subjects: cannot search by match key")
			continue
		}

		// Still layer 1: the same lookup against the other spellings a subject
		// is known by, in the language this doléance was written in. This is
		// where the pre-filled Wikidata names are collected, and it is why the
		// expensive layers get quieter over time rather than busier — the
		// first German doléance about health matches "Gesundheit" here, for
		// one index read, because some earlier French one taught the
		// vocabulary what Q12147 is called.
		//
		// A doléance whose language the classifier could not name skips this
		// layer rather than searching every language at once: see
		// store.FindSubjectByAlias.
		if subject, err := a.store.FindSubjectByAlias(ctx, key, language); err == nil {
			log.Debug().Str("label", label).Str("matched", subject.Label).
				Str("layer", "alias").Msg("subjects: resolved")
			resolved = append(resolved, subject.ID)
			continue
		} else if !errors.Is(err, store.ErrSubjectNotFound) {
			log.Error().Err(err).Msg("subjects: cannot search by alias")
			continue
		}

		// Layer 2: the same lookup with a trailing plural marker removed.
		fold := subjects.FoldPlural(key)
		if subject, err := a.store.FindSubjectByFoldKey(ctx, fold); err == nil {
			log.Debug().Str("label", label).Str("matched", subject.Label).
				Str("layer", "plural").Msg("subjects: resolved")
			resolved = append(resolved, subject.ID)
			continue
		} else if !errors.Is(err, store.ErrSubjectNotFound) {
			log.Error().Err(err).Msg("subjects: cannot search by fold key")
			continue
		}

		// Layer 3: a language-neutral identity — but only one a person has
		// confirmed. An unconfirmed QID is not consulted and not carried
		// forward, because it is not an identity yet: see
		// models.SubjectEntity.
		//
		// The proposal for this label is raised after the subject exists,
		// since it is the subject the entity would belong to.
		found := candidate{label: label, proposeEntity: true}
		unknown = append(unknown, found)
	}

	if len(unknown) == 0 {
		return resolved
	}
	return append(resolved, a.resolveByEmbedding(ctx, client, message, unknown, language, settings)...)
}

// candidate is a label that no exact layer recognised.
type candidate struct {
	label string

	// proposeEntity asks for a Wikidata identity to be looked up and put to a
	// curator once the subject exists. The subject is created either way — a
	// doléance never waits on an identity, and most subjects never get one.
	proposeEntity bool
}

// proposeEntity looks a subject up in Wikidata and puts the best candidate to
// a curator.
//
// Nothing is attributed. The subject already exists and already classifies
// doléances; what this adds, if a person confirms it, is an identity that
// names the subject in fifty languages and lets two spellings of it be
// recognised as one.
//
// Every failure here is silence. Wikidata being slow, rate-limiting us, not
// knowing the word, the model declining to choose — all the same outcome: no
// proposal this time. The register must not depend on a third party to
// classify a doléance, and it does not: the subject was created before this
// ran.
func (a *API) proposeEntity(ctx context.Context, client *llm.Client,
	settings models.LLMSettings, subject models.Subject, message models.Message) {

	if a.wikidata == nil || client == nil {
		return
	}

	candidates, err := a.wikidata.Search(ctx, subject.Label, subject.Language)
	if err != nil {
		log.Debug().Err(err).Str("label", subject.Label).
			Msg("subjects: no Wikidata candidates this time")
		return
	}
	if len(candidates) == 0 {
		return
	}

	offered := make([]llm.EntityCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		offered = append(offered, llm.EntityCandidate{
			QID:         candidate.QID,
			Label:       candidate.Label,
			Description: candidate.Description,
			MatchType:   candidate.MatchType,
		})
	}

	// The doléance goes with the label: "isolement" in a text about an elderly
	// person alone in a village is a different entity from "isolement" in one
	// about a prison, and the candidate list contains both.
	pick, err := client.PickEntity(ctx, settings.ClassificationModel,
		subject.Label, message.Text, offered)
	if err != nil {
		log.Debug().Err(err).Str("label", subject.Label).
			Msg("subjects: the entity could not be picked")
		return
	}
	if pick.QID == "" {
		// "None of these" is the right answer often enough that it is not a
		// failure: plenty of real subjects have no Wikidata entry.
		log.Debug().Str("label", subject.Label).Str("reason", pick.Reason).
			Msg("subjects: no Wikidata entity fits")
		return
	}

	chosen := candidates[0]
	for _, candidate := range candidates {
		if candidate.QID == pick.QID {
			chosen = candidate
			break
		}
	}

	proposal := models.SubjectEntity{
		SubjectID:   subject.ID,
		QID:         chosen.QID,
		Label:       chosen.Label,
		Description: chosen.Description,
		MatchType:   chosen.MatchType,
		Confidence:  pick.Confidence,
		Reason:      pick.Reason,
		MessageID:   message.ID,
	}
	if err := a.store.ProposeSubjectEntity(ctx, proposal); err != nil {
		log.Error().Err(err).Str("label", subject.Label).
			Msg("subjects: cannot record the entity proposal")
		return
	}

	log.Info().Str("subject", subject.Label).Str("qid", chosen.QID).
		Str("entity", chosen.Label).Int("confidence", pick.Confidence).
		Str("matched_on", chosen.MatchType).
		Msg("subjects: an entity is proposed for a curator to confirm")
}

// resolveByEmbedding is the last layer, and the only one that costs a call.
//
// All the unknown labels from one message are embedded together, so a doléance
// costs one request rather than one per subject. It is also where every
// question is finally shaped, because this is the only point at which both
// signals are in hand at once.
func (a *API) resolveByEmbedding(ctx context.Context, client *llm.Client, message models.Message,
	candidates []candidate, language string, settings models.LLMSettings) []string {

	labels := make([]string, len(candidates))
	for i, c := range candidates {
		labels[i] = c.label
	}

	model := settings.EmbeddingModel
	vectors, err := client.Embed(ctx, model, labels)
	if err != nil {
		// A failed embedding means new subjects rather than no subjects. A
		// duplicate is a tidiness problem a curator fixes in seconds; blocking
		// classification over an embeddings outage stops the register
		// filtering at all, which is what the subjects are for.
		//
		// A Wikidata match still gets asked about, with no second opinion
		// attached — half the evidence is better than losing the question.
		log.Warn().Err(err).Msg("subjects: embedding failed; creating the labels as new")
		return a.createAndAsk(ctx, client, settings, message, candidates, language, nil, model, nil)
	}

	existing, err := a.store.ListSubjectVectors(ctx, model)
	if err != nil {
		log.Error().Err(err).Msg("subjects: cannot read the vocabulary's vectors")
		existing = nil
	}

	var resolved []string
	for i, c := range candidates {
		vector := vectors[i]
		best, score := nearest(existing, vector)
		crossLanguage := best.Subject.Language != language

		// Which threshold applies depends on whether the two labels are in the
		// same language, because the two comparisons are different questions
		// with different distributions. See models.LLMSettings for the
		// measurements.
		suggestAt := settings.SubjectSuggestThreshold
		if crossLanguage {
			suggestAt = settings.SubjectSuggestCrossLanguage
		}

		switch {
		case crossLanguage && score >= suggestAt:
			// Never merged automatically, however high the score. "Wohnen" is a
			// verb where "logement" is a noun, and a vector cannot see that; a
			// curator who reads one of the two languages can.
			resolved = append(resolved, a.createAndAsk(ctx, client, settings, message,
				[]candidate{c}, language, [][]float32{vector}, model, &question{
					into:          best.Subject.ID,
					source:        models.MergeSourceEmbedding,
					similarity:    score,
					crossLanguage: true,
				})...)

		case score >= settings.SubjectMergeThreshold:
			// Same language and all but identical — two spellings the match key
			// happened not to collapse. This is the one similarity confident
			// enough to act on.
			log.Debug().Str("label", c.label).Str("matched", best.Subject.Label).
				Float64("cosine", score).Str("layer", "embedding").
				Msg("subjects: resolved")
			resolved = append(resolved, best.Subject.ID)

		case score >= suggestAt:
			// Close, not close enough. An embedding that is *fairly* sure
			// "logement" and "habitat" are the same is not sure enough to
			// quietly collapse two filters.
			resolved = append(resolved, a.createAndAsk(ctx, client, settings, message,
				[]candidate{c}, language, [][]float32{vector}, model, &question{
					into:       best.Subject.ID,
					source:     models.MergeSourceEmbedding,
					similarity: score,
				})...)

		default:
			resolved = append(resolved, a.createAndAsk(ctx, client, settings, message,
				[]candidate{c}, language, [][]float32{vector}, model, nil)...)
		}
	}
	return resolved
}

// question is what a curator will be asked about a newly created subject.
type question struct {
	into          string
	source        string
	evidence      string
	similarity    float64
	crossLanguage bool
}

// nearest finds the closest subject to a vector.
func nearest(candidates []store.SubjectVector, vector []float32) (store.SubjectVector, float64) {
	var (
		best  store.SubjectVector
		score float64
	)
	for _, candidate := range candidates {
		if similarity := subjects.Cosine(candidate.Vector, vector); similarity > score {
			best, score = candidate, similarity
		}
	}
	return best, score
}

// createAndAsk adds labels to the vocabulary and, where one was raised, records
// the question that goes with the first of them.
//
// The subject is created before the question is asked, and that order is the
// point: the doléance being classified keeps its subject whatever a curator
// later decides, and a merge moves the message rather than stranding it.
func (a *API) createAndAsk(ctx context.Context, client *llm.Client, settings models.LLMSettings,
	message models.Message, candidates []candidate, language string, vectors [][]float32,
	model string, ask *question) []string {

	messageID := message.ID

	ids := make([]string, 0, len(candidates))
	for i, c := range candidates {
		var vector []float32
		if i < len(vectors) {
			vector = vectors[i]
		}

		// No QID: an identity is attributed only once a person has confirmed
		// one, and this subject has never been in front of anybody.
		subject, err := a.store.CreateSubject(ctx, c.label, language, "", vector, model)
		if err != nil {
			log.Error().Err(err).Str("label", c.label).Msg("subjects: cannot create")
			continue
		}
		log.Info().Str("subject", subject.Label).Str("slug", subject.Slug).
			Msg("subjects: new subject added to the vocabulary")
		ids = append(ids, subject.ID)

		if c.proposeEntity {
			a.proposeEntity(ctx, client, settings, subject, message)
		}

		// A question belongs to the candidate it was raised for, which is the
		// only one a caller ever passes alongside one.
		if ask == nil || i > 0 {
			continue
		}
		a.askCurator(ctx, store.MergeProposal{
			SubjectID:     subject.ID,
			IntoID:        ask.into,
			MessageID:     messageID,
			Source:        ask.source,
			Evidence:      ask.evidence,
			Similarity:    ask.similarity,
			CrossLanguage: ask.crossLanguage,
		})
	}

	// A Wikidata match whose subject could not be created leaves nothing to
	// ask about, which is why this is not reported as a failure.
	return ids
}

// askCurator records that two subjects may be the same thing.
func (a *API) askCurator(ctx context.Context, proposal store.MergeProposal) {
	if proposal.SubjectID == proposal.IntoID || proposal.IntoID == "" {
		return
	}

	log.Info().Str("subject", proposal.SubjectID).Str("into", proposal.IntoID).
		Str("source", proposal.Source).Str("qid", proposal.Evidence).
		Float64("cosine", proposal.Similarity).
		Bool("cross_language", proposal.CrossLanguage).
		Msg("subjects: asking a curator whether these are one subject")

	if err := a.store.SuggestSubjectMerge(ctx, proposal); err != nil {
		log.Error().Err(err).Msg("subjects: cannot record a merge suggestion")
	}
}

// learnTranslations writes down what this subject is called in every language
// the register speaks.
//
// Called only after a curator has confirmed the entity. Running it on an
// unconfirmed one is what put "Raumfahrt" on the transport filter and
// "solitary confinement" into the matching path as the English name of rural
// isolation.
//
// A QID is not only an identity, it is a dictionary entry: Wikidata already
// knows that Q12147 is "santé", "Gesundheit" and "health", so there is no
// reason to rediscover that one contributor at a time — at the price of a
// Wikidata call, an embedding call and a curator's attention per language.
// Writing the names down once means every later spelling resolves at layer one
// for free.
//
// This is what makes the curation questions thin out rather than accumulate.
// Each entity ever resolved permanently teaches the vocabulary a set of words,
// so the questions are concentrated in the register's first weeks and become
// rare exactly as the user of a busy console would want.
//
// Failure is silence. An alias is an optimisation: without it the register
// still works, it merely asks a person a question it could have answered
// itself.
func (a *API) learnTranslations(ctx context.Context, subject models.Subject) {
	if a.wikidata == nil || subject.QID == "" {
		return
	}
	if len(a.languages) == 0 {
		// Never a legitimate state: the configuration falls back to a default
		// list, so an empty one means the field was not wired. Said out loud
		// because the silent version of this cost a whole run — aliases are an
		// optimisation, so their absence breaks nothing and shows up only as
		// curator questions that should not have been asked.
		log.Warn().Msg("subjects: no register languages configured; translations will not be learned")
		return
	}

	labels, err := a.wikidata.Labels(ctx, subject.QID, a.languages)
	if err != nil {
		log.Debug().Err(err).Str("qid", subject.QID).
			Msg("subjects: no translations this time")
		return
	}
	if len(labels) == 0 {
		return
	}

	// Wikidata's own label in the subject's language is usually the word we
	// already have, and the collision is skipped rather than reported: it
	// means the two agree.
	added, err := a.store.AddSubjectAliases(ctx, subject.ID, models.AliasSourceWikidata, labels)
	if err != nil {
		log.Error().Err(err).Str("subject", subject.Label).
			Msg("subjects: cannot store the translations")
		return
	}
	if added > 0 {
		log.Info().Str("subject", subject.Label).Str("qid", subject.QID).
			Int("spellings", added).Strs("languages", a.languages).
			Msg("subjects: the vocabulary learned this subject's other names")
	}
}
