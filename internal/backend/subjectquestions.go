package backend

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/rs/zerolog/log"

	"github.com/CoderSyndicate/doleances/internal/cache"
	"github.com/CoderSyndicate/doleances/internal/models"
	"github.com/CoderSyndicate/doleances/internal/store"
	"github.com/CoderSyndicate/doleances/internal/wikidata"
)

func (a *API) registerSubjectQuestionRoutes(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "list-subject-questions",
		Method:      http.MethodGet,
		Path:        "/v1/curation/subjects",
		Summary:     "List subject questions awaiting a human decision",
		Description: "Two subjects that may be one. Nothing here was merged automatically: " +
			"a wrong merge silently changes what the register appears to say, and the " +
			"evidence that it was wrong is the row that was deleted. Each question " +
			"carries what each system concluded and the doléance that raised it.",
		Tags: []string{"Curation"},
	}, a.listSubjectQuestions)

	huma.Register(api, invalidates(cache.Subjects, cache.Messages)(huma.Operation{
		OperationID: "merge-subjects",
		Method:      http.MethodPost,
		Path:        "/v1/curation/subjects/{id}/merge",
		Summary:     "Confirm that two subjects are one",
		Description: "Every doléance carrying the merged subject moves to the survivor, so no " +
			"message loses its classification.",
		Tags: []string{"Curation"},
	}), a.mergeSubjects)

	huma.Register(api, huma.Operation{
		OperationID: "list-entity-proposals",
		Method:      http.MethodGet,
		Path:        "/v1/curation/entities",
		Summary:     "Wikidata identities awaiting confirmation",
		Description: "No QID is ever attributed without a person. An identity decides what a " +
			"subject is called in fifty languages and which subjects are merged into it, " +
			"and an unconfirmed pipeline attributed spaceflight to \"transports\". Least " +
			"confident first: that is where a curator's attention is worth most.",
		Tags: []string{"Curation"},
	}, a.listEntityProposals)

	huma.Register(api, invalidates(cache.Subjects)(huma.Operation{
		OperationID: "confirm-subject-entity",
		Method:      http.MethodPost,
		Path:        "/v1/curation/entities/{id}/confirm",
		Summary:     "Attribute the entity to the subject",
		Description: "The only path that writes a QID. Confirming also fetches the entity's name " +
			"in every language the register speaks, so the subject is recognised in all of " +
			"them from then on.",
		Tags: []string{"Curation"},
	}), a.confirmEntity)

	huma.Register(api, huma.Operation{
		OperationID: "reject-subject-entity",
		Method:      http.MethodPost,
		Path:        "/v1/curation/entities/{id}/reject",
		Summary:     "Record that the entity is not this subject",
		Description: "The pair is never proposed again, so a curator answers each question once.",
		Tags:        []string{"Curation"},
	}, a.rejectEntity)

	huma.Register(api, huma.Operation{
		OperationID: "list-subject-vocabulary",
		Method:      http.MethodGet,
		Path:        "/v1/curation/vocabulary",
		Summary:     "The subject vocabulary as a curator needs to see it",
		Description: "Every subject with what it is called elsewhere and how many doléances carry " +
			"it. Distinct from GET /v1/subjects, which is the public filter list: a curator " +
			"deciding whether to rename or merge needs the usage count and the other " +
			"spellings, and a reader browsing the register needs neither.",
		Tags: []string{"Curation"},
	}, a.listVocabulary)

	huma.Register(api, invalidates(cache.Subjects)(huma.Operation{
		OperationID: "rename-subject",
		Method:      http.MethodPost,
		Path:        "/v1/curation/subjects/{id}/rename",
		Summary:     "Change the label a reader sees",
		Description: "The slug and the match key are left alone: the slug is the stable identity " +
			"and appears in query strings, and keeping the match key means renaming " +
			"\"santé\" does not make the next \"santé\" a second row. A curator who wants " +
			"that merges instead.",
		Tags: []string{"Curation"},
	}), a.renameSubject)

	huma.Register(api, huma.Operation{
		OperationID: "dismiss-subject-question",
		Method:      http.MethodPost,
		Path:        "/v1/curation/subjects/{id}/dismiss",
		Summary:     "Record that two subjects are different",
		Description: "The pair is never proposed again, so a curator answers each question once.",
		Tags:        []string{"Curation"},
	}, a.dismissSubjectQuestion)
}

// SubjectSide is one of the two subjects in a question.
type SubjectSide struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Slug     string `json:"slug"`
	Language string `json:"language,omitempty"`

	// QID is this side's own confirmed entity, and Entity is where to read it.
	//
	// Each side's own, not the shared one. A question raised by the embedding
	// layer has no shared entity, and its two subjects may still both carry an
	// identity — "accès aux soins" is Q2822913 and "healthcare access" is
	// Q67075251 — which is precisely the evidence a curator needs and which
	// showing only the shared entity hides.
	QID    string `json:"qid,omitempty"`
	Entity string `json:"entity,omitempty"`

	// Article is the Wikipedia page for *this side's* entity in *this side's*
	// language, which is the fastest way to answer the question: if the German
	// and the French article describe the same thing, the two labels are the
	// same subject. Empty when that side has no confirmed entity.
	Article string `json:"article,omitempty"`
}

// subjectSide describes one of the two subjects, with its own identity rather
// than the pair's.
func subjectSide(subject models.Subject) SubjectSide {
	return SubjectSide{
		ID:       subject.ID,
		Label:    subject.Label,
		Slug:     subject.Slug,
		Language: subject.Language,
		QID:      subject.QID,
		Entity:   wikidata.EntityURL(subject.QID),
		Article:  wikidata.ArticleURL(subject.QID, subject.Language),
	}
}

// Values for SubjectQuestion.Entities.
const (
	entitiesSame      = "same"
	entitiesDifferent = "different"
	entitiesUnknown   = "unknown"
)

// comparedEntities says what the two sides' own identities imply.
//
// Only when both have one. A single confirmed entity on one side says nothing
// about the other, and reporting that as "different" would turn an absence
// into an argument.
func comparedEntities(left, right string) string {
	if left == "" || right == "" {
		return entitiesUnknown
	}
	if left == right {
		return entitiesSame
	}
	return entitiesDifferent
}

// SubjectQuestion is one pending decision, with everything needed to take it.
type SubjectQuestion struct {
	ID   string      `json:"id"`
	New  SubjectSide `json:"new"`
	Into SubjectSide `json:"into"`

	// Source is which signal raised the question: "wikidata" or "embedding".
	Source string `json:"source"`

	// QID and Entity are the shared Wikidata identity and its page. Present
	// only when that is what raised the question.
	QID    string `json:"qid,omitempty"`
	Entity string `json:"entity,omitempty"`

	// Similarity is what the embeddings made of the same pair, from 0 to 1,
	// and zero when no vector was available. It is a second opinion rather
	// than a verdict, and it is at its most useful when it contradicts the QID.
	Similarity float64 `json:"similarity"`

	// CrossLanguage marks a pair written in two languages. These are never
	// merged automatically whatever they score.
	CrossLanguage bool `json:"cross_language"`

	// Entities compares the two sides' own identities: "same", "different", or
	// "unknown" when at least one of them has none.
	//
	// It is the strongest evidence on the card when it is available, and it
	// points both ways. Two subjects resolved to one entity are almost
	// certainly one subject; two resolved to *different* entities are a
	// reason not to merge, and that reason was invisible before — the card
	// only ever showed an entity when Wikidata had raised the question.
	//
	// "Different" is evidence, not a verdict: Wikidata carries near-duplicate
	// entries for close concepts, so a curator still decides.
	Entities string `json:"entities"`

	// Agreement compares what the two systems concluded about this pair:
	// "agree", "disagree", or "alone" when only one of them had an opinion.
	//
	// Computed here rather than in the browser because it is a judgement
	// against the operator's own thresholds, and the console does not hold
	// those. A disagreement is the most informative thing on the page — it is
	// usually a Wikidata search that landed on a guest house — so it must not
	// depend on a number somebody guessed in a script.
	Agreement string `json:"agreement"`

	// Message is the doléance being classified when the question arose — the
	// context that makes it answerable. Absent if that message has since been
	// deleted by its author.
	Message *QuestionContext `json:"message,omitempty"`
}

// QuestionContext is the doléance a curator reads to decide.
type QuestionContext struct {
	ID        string    `json:"id"`
	Text      string    `json:"text"`
	Language  string    `json:"language,omitempty"`
	Place     string    `json:"place,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// SubjectQuestionsOutput is the pending list.
type SubjectQuestionsOutput struct {
	Body struct {
		Items []SubjectQuestion `json:"items"`
	}
}

func (a *API) listSubjectQuestions(ctx context.Context, _ *struct{}) (*SubjectQuestionsOutput, error) {
	suggestions, err := a.store.ListSubjectMergeSuggestions(ctx)
	if err != nil {
		log.Error().Err(err).Msg("cannot read the subject questions")
		return nil, huma.Error500InternalServerError("cannot read the subject questions")
	}

	// The thresholds are what "close" means here, and they are the operator's
	// to set. A failure to read them costs the agreement column and nothing
	// else: the question is still answerable from the two raw numbers.
	settings, err := a.store.LLMSettings(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("cannot read the thresholds; questions will not say whether the systems agree")
	}
	settings = settings.WithThresholdDefaults()

	out := &SubjectQuestionsOutput{}
	out.Body.Items = make([]SubjectQuestion, 0, len(suggestions))
	for _, suggestion := range suggestions {
		question := SubjectQuestion{
			ID:            suggestion.ID,
			Source:        suggestion.Source,
			QID:           suggestion.Evidence,
			Entity:        wikidata.EntityURL(suggestion.Evidence),
			Similarity:    suggestion.Similarity,
			CrossLanguage: suggestion.CrossLanguage,
			Agreement:     agreement(suggestion, settings),
			Entities:      comparedEntities(suggestion.Subject.QID, suggestion.Into.QID),
			New:           subjectSide(suggestion.Subject),
			Into:          subjectSide(suggestion.Into),
		}

		if message := suggestion.Message; message != nil {
			question.Message = &QuestionContext{
				ID:        message.ID,
				Text:      message.Text,
				Language:  message.Language,
				CreatedAt: message.CreatedAt,
			}
			if message.Location != nil {
				question.Message.Place = message.Location.Label
			}
		}

		out.Body.Items = append(out.Body.Items, question)
	}
	return out, nil
}

// VocabularyEntry is one subject with what a curator needs to judge it.
type VocabularyEntry struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Slug     string `json:"slug"`
	Language string `json:"language,omitempty"`
	QID      string `json:"qid,omitempty"`
	Entity   string `json:"entity,omitempty"`

	// Messages is how many doléances carry this subject. It is the number that
	// makes a rename or a merge a real decision rather than a tidy-up: a label
	// on three hundred doléances is a filter people are using.
	Messages int64 `json:"messages"`

	// Aliases are the other spellings that resolve to this subject, newest
	// last. A curator renaming a subject should see what it is already called
	// elsewhere — and that a word they are about to remove from view is still
	// how the register recognises incoming doléances.
	Aliases []VocabularyAlias `json:"aliases,omitempty"`
}

// VocabularyAlias is one other spelling.
type VocabularyAlias struct {
	Label    string `json:"label"`
	Language string `json:"language,omitempty"`
	Source   string `json:"source,omitempty"`
}

// VocabularyOutput is the whole vocabulary.
type VocabularyOutput struct {
	Body struct {
		Subjects []VocabularyEntry `json:"subjects"`
	}
}

func (a *API) listVocabulary(ctx context.Context, _ *struct{}) (*VocabularyOutput, error) {
	subjects, err := a.store.ListSubjects(ctx)
	if err != nil {
		log.Error().Err(err).Msg("cannot read the subject vocabulary")
		return nil, huma.Error500InternalServerError("cannot read the vocabulary")
	}

	out := &VocabularyOutput{}
	out.Body.Subjects = make([]VocabularyEntry, 0, len(subjects))
	for _, subject := range subjects {
		out.Body.Subjects = append(out.Body.Subjects, a.vocabularyEntry(ctx, subject))
	}
	return out, nil
}

// vocabularyEntry describes one subject as a curator needs to see it: what it
// is called elsewhere, what identity it carries, and how much of the register
// depends on it.
func (a *API) vocabularyEntry(ctx context.Context, subject models.Subject) VocabularyEntry {
	entry := VocabularyEntry{
		ID:       subject.ID,
		Label:    subject.Label,
		Slug:     subject.Slug,
		Language: subject.Language,
		QID:      subject.QID,
		Entity:   wikidata.EntityURL(subject.QID),
	}

	count, err := a.store.CountMessagesForSubject(ctx, subject.ID)
	if err != nil {
		log.Error().Err(err).Str("subject", subject.ID).
			Msg("cannot count the doléances carrying a subject")
	}
	entry.Messages = count

	aliases, err := a.store.ListSubjectAliases(ctx, subject.ID)
	if err != nil {
		log.Error().Err(err).Str("subject", subject.ID).Msg("cannot read a subject's aliases")
	}
	for _, alias := range aliases {
		entry.Aliases = append(entry.Aliases, VocabularyAlias{
			Label: alias.Label, Language: alias.Language, Source: alias.Source,
		})
	}
	return entry
}

// RenameSubjectInput is a new label for a subject.
type RenameSubjectInput struct {
	ID   string `path:"id"`
	Body struct {
		Label  string `json:"label"`
		Actor  string `json:"actor,omitempty"`
		Reason string `json:"reason,omitempty"`
	}
}

func (a *API) renameSubject(ctx context.Context, in *RenameSubjectInput) (*SubjectDecisionOutput, error) {
	label := strings.TrimSpace(in.Body.Label)

	var before models.Subject
	if err := a.store.DB().WithContext(ctx).First(&before, "id = ?", in.ID).Error; err != nil {
		return nil, huma.Error404NotFound("no such subject")
	}

	if err := a.store.RenameSubject(ctx, in.ID, label); err != nil {
		// The store validates the label — three words, sixty-four runes — and
		// a refusal is an answer to the curator rather than a server fault.
		log.Info().Err(err).Str("subject", in.ID).Msg("subject rename refused")
		return nil, huma.Error422UnprocessableEntity(err.Error())
	}

	// The old label is in the audit entry, because the row no longer holds it
	// and a rename is otherwise untraceable: the filter people were using
	// silently becomes a different word.
	a.recordDecision(ctx, in.Body.Actor, models.AuditRenameSubject, "subject",
		in.ID, strings.TrimSpace(before.Label+" → "+label+". "+in.Body.Reason))
	log.Info().Str("subject", in.ID).Str("from", before.Label).Str("to", label).
		Str("actor", in.Body.Actor).Msg("subject renamed by a curator")

	out := &SubjectDecisionOutput{}
	out.Body.ID = in.ID
	out.Body.Decided = "renamed"
	return out, nil
}

// SubjectDecisionInput is a curator's answer to one question.
type SubjectDecisionInput struct {
	ID   string `path:"id"`
	Body struct {
		Actor  string `json:"actor,omitempty"`
		Reason string `json:"reason,omitempty"`
	}
}

// SubjectDecisionOutput reports what happened.
type SubjectDecisionOutput struct {
	Body struct {
		ID      string `json:"id"`
		Decided string `json:"decided"`
	}
}

func (a *API) mergeSubjects(ctx context.Context, in *SubjectDecisionInput) (*SubjectDecisionOutput, error) {
	suggestion, err := a.findSubjectQuestion(ctx, in.ID)
	if err != nil {
		return nil, err
	}

	// The newcomer folds into the subject that was there first, so the
	// vocabulary keeps the label the register has been showing and the slug
	// already in people's query strings.
	if err := a.store.MergeSubjects(ctx, suggestion.Subject.ID, suggestion.Into.ID); err != nil {
		log.Error().Err(err).Str("question", in.ID).Msg("cannot merge two subjects")
		return nil, huma.Error500InternalServerError("cannot merge the subjects")
	}

	a.recordDecision(ctx, in.Body.Actor, models.AuditMergeSubjects, "subject",
		suggestion.Subject.ID+" into "+suggestion.Into.ID, in.Body.Reason)
	log.Info().Str("subject", suggestion.Subject.Label).
		Str("into", suggestion.Into.Label).Str("actor", in.Body.Actor).
		Msg("subjects merged by a curator")

	out := &SubjectDecisionOutput{}
	out.Body.ID = in.ID
	out.Body.Decided = "merged"
	return out, nil
}

func (a *API) dismissSubjectQuestion(ctx context.Context, in *SubjectDecisionInput) (*SubjectDecisionOutput, error) {
	suggestion, err := a.findSubjectQuestion(ctx, in.ID)
	if err != nil {
		return nil, err
	}

	if err := a.store.DismissSubjectMerge(ctx, in.ID); err != nil {
		log.Error().Err(err).Str("question", in.ID).Msg("cannot dismiss a subject question")
		return nil, huma.Error500InternalServerError("cannot record the decision")
	}

	a.recordDecision(ctx, in.Body.Actor, models.AuditKeepSubjects, "subject",
		suggestion.Subject.ID+" and "+suggestion.Into.ID, in.Body.Reason)
	log.Info().Str("subject", suggestion.Subject.Label).
		Str("and", suggestion.Into.Label).Str("actor", in.Body.Actor).
		Msg("a curator kept two subjects apart")

	out := &SubjectDecisionOutput{}
	out.Body.ID = in.ID
	out.Body.Decided = "kept apart"
	return out, nil
}

// findSubjectQuestion resolves a pending question, so a decision names the
// subjects it acted on rather than an opaque identifier.
//
// It reads the pending list rather than the row, which also means an already
// answered question cannot be answered a second time — two curators with the
// page open is the ordinary case, not an edge one.
func (a *API) findSubjectQuestion(ctx context.Context, id string) (*store.SubjectMergeSuggestion, error) {
	suggestions, err := a.store.ListSubjectMergeSuggestions(ctx)
	if err != nil {
		log.Error().Err(err).Msg("cannot read the subject questions")
		return nil, huma.Error500InternalServerError("cannot read the subject questions")
	}
	for _, suggestion := range suggestions {
		if suggestion.ID == id {
			return &suggestion, nil
		}
	}
	return nil, huma.Error404NotFound("no such question, or it has already been answered")
}

// Agreement values.
const (
	agreementAgree    = "agree"
	agreementDisagree = "disagree"
	agreementAlone    = "alone"
)

// agreement says whether the second system backs the first.
//
// "Alone" is not a hedge, it is the truthful answer when only one system had
// anything to say — a label with no Wikidata entity, or a pair with no vector
// because the embeddings were down when it arrived. Presenting that as
// agreement would invent a corroboration that never happened.
func agreement(suggestion store.SubjectMergeSuggestion, settings models.LLMSettings) string {
	if suggestion.Source != models.MergeSourceWikidata {
		// An embedding question has a QID only when both labels resolved to
		// one, and that is the Wikidata case handled below. So there is
		// nothing here for a second system to confirm.
		return agreementAlone
	}
	if suggestion.Similarity <= 0 {
		return agreementAlone
	}

	// Against the threshold the operator set for this kind of comparison,
	// because "close" means something different across two languages than
	// within one.
	closeEnough := settings.SubjectSuggestThreshold
	if suggestion.CrossLanguage {
		closeEnough = settings.SubjectSuggestCrossLanguage
	}
	if suggestion.Similarity >= closeEnough {
		return agreementAgree
	}
	return agreementDisagree
}

// EntityProposalItem is one identity awaiting confirmation.
type EntityProposalItem struct {
	ID string `json:"id"`

	// Subject is the word from the register; Label and Description are what
	// Wikidata calls the entity. The curator is comparing those two.
	Subject     string `json:"subject"`
	SubjectID   string `json:"subject_id"`
	Language    string `json:"language,omitempty"`
	QID         string `json:"qid"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`

	// Entity and Article are where to read it — the statements, and the
	// encyclopedia page in the subject's own language.
	Entity  string `json:"entity,omitempty"`
	Article string `json:"article,omitempty"`

	// MatchType is how the search found it. An alias match is the shape of the
	// worst errors — "transports spatiaux" is why spaceflight was ever offered
	// for "transports" — so a curator sees it.
	MatchType string `json:"match_type,omitempty"`

	// Confidence and Reason are the model's. It picked from the same list and
	// with the same descriptions the curator is now reading.
	Confidence int    `json:"confidence"`
	Reason     string `json:"reason,omitempty"`

	// Message is the doléance the subject came from, which is the context that
	// decides the answer: "isolement" in a text about a village is a different
	// entity from "isolement" in one about a prison.
	Message *QuestionContext `json:"message,omitempty"`
}

// EntityProposalsOutput is the pending list.
type EntityProposalsOutput struct {
	Body struct {
		Items []EntityProposalItem `json:"items"`
	}
}

func (a *API) listEntityProposals(ctx context.Context, _ *struct{}) (*EntityProposalsOutput, error) {
	proposals, err := a.store.ListSubjectEntityProposals(ctx)
	if err != nil {
		log.Error().Err(err).Msg("cannot read the entity proposals")
		return nil, huma.Error500InternalServerError("cannot read the entity proposals")
	}

	out := &EntityProposalsOutput{}
	out.Body.Items = make([]EntityProposalItem, 0, len(proposals))
	for _, proposal := range proposals {
		item := EntityProposalItem{
			ID:          proposal.ID,
			Subject:     proposal.Subject.Label,
			SubjectID:   proposal.Subject.ID,
			Language:    proposal.Subject.Language,
			QID:         proposal.QID,
			Label:       proposal.Label,
			Description: proposal.Description,
			Entity:      wikidata.EntityURL(proposal.QID),
			Article:     wikidata.ArticleURL(proposal.QID, proposal.Subject.Language),
			MatchType:   proposal.MatchType,
			Confidence:  proposal.Confidence,
			Reason:      proposal.Reason,
		}
		if message := proposal.Message; message != nil {
			item.Message = &QuestionContext{
				ID:        message.ID,
				Text:      message.Text,
				Language:  message.Language,
				CreatedAt: message.CreatedAt,
			}
			if message.Location != nil {
				item.Message.Place = message.Location.Label
			}
		}
		out.Body.Items = append(out.Body.Items, item)
	}
	return out, nil
}

func (a *API) confirmEntity(ctx context.Context, in *SubjectDecisionInput) (*SubjectDecisionOutput, error) {
	subject, err := a.store.ConfirmSubjectEntity(ctx, in.ID)
	if errors.Is(err, store.ErrSubjectNotFound) {
		return nil, huma.Error404NotFound("no such proposal, or it has already been answered")
	}
	if err != nil {
		log.Error().Err(err).Str("proposal", in.ID).Msg("cannot confirm an entity")
		return nil, huma.Error500InternalServerError("cannot confirm the entity")
	}

	a.recordDecision(ctx, in.Body.Actor, models.AuditConfirmEntity, "subject",
		subject.ID+" = "+subject.QID, in.Body.Reason)
	log.Info().Str("subject", subject.Label).Str("qid", subject.QID).
		Str("actor", in.Body.Actor).Msg("entity confirmed by a curator")

	// Now that a person has vouched for the identity, the entity's names in
	// every language the register speaks are worth having — and only now.
	a.learnTranslations(ctx, subject)

	out := &SubjectDecisionOutput{}
	out.Body.ID = in.ID
	out.Body.Decided = "confirmed"
	return out, nil
}

func (a *API) rejectEntity(ctx context.Context, in *SubjectDecisionInput) (*SubjectDecisionOutput, error) {
	if err := a.store.RejectSubjectEntity(ctx, in.ID); err != nil {
		if errors.Is(err, store.ErrSubjectNotFound) {
			return nil, huma.Error404NotFound("no such proposal, or it has already been answered")
		}
		log.Error().Err(err).Str("proposal", in.ID).Msg("cannot reject an entity")
		return nil, huma.Error500InternalServerError("cannot record the decision")
	}

	a.recordDecision(ctx, in.Body.Actor, models.AuditRejectEntity, "subject",
		in.ID, in.Body.Reason)
	log.Info().Str("proposal", in.ID).Str("actor", in.Body.Actor).
		Msg("a curator refused an entity")

	out := &SubjectDecisionOutput{}
	out.Body.ID = in.ID
	out.Body.Decided = "rejected"
	return out, nil
}
