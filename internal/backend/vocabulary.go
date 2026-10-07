package backend

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/rs/zerolog/log"

	"github.com/CoderSyndicate/doleances/internal/cache"
	"github.com/CoderSyndicate/doleances/internal/models"
	"github.com/CoderSyndicate/doleances/internal/store"
	"github.com/CoderSyndicate/doleances/internal/wikidata"
)

func (a *API) registerVocabularyRoutes(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "search-subjects",
		Method:      http.MethodGet,
		Path:        "/v1/curation/vocabulary/search",
		Summary:     "Find subjects by any of their names",
		Description: "Searches labels and aliases, so a curator looking for a German subject by " +
			"its French name finds it — which is what the aliases are for.",
		Tags: []string{"Curation"},
	}, a.searchSubjects)

	huma.Register(api, huma.Operation{
		OperationID: "list-detached-subjects",
		Method:      http.MethodGet,
		Path:        "/v1/curation/vocabulary/detached",
		Summary:     "Subjects outside the hierarchy",
		Description: "No parent and no child. Such a subject classifies doléances perfectly well " +
			"and is unreachable from any broader filter, so only somebody who already " +
			"knows its exact name will ever find it.",
		Tags: []string{"Curation"},
	}, a.listDetachedSubjects)

	huma.Register(api, huma.Operation{
		OperationID: "get-subject",
		Method:      http.MethodGet,
		Path:        "/v1/curation/vocabulary/{id}",
		Summary:     "One subject, with its identity, names and relations",
		Tags:        []string{"Curation"},
	}, a.getSubject)

	huma.Register(api, huma.Operation{
		OperationID: "search-wikidata",
		Method:      http.MethodGet,
		Path:        "/v1/curation/wikidata",
		Summary:     "Search Wikidata for an entity",
		Description: "Proxied through the backend, never called from the console's browser: a page " +
			"that queried Wikidata directly would hand it the address of everyone " +
			"curating. Same rule as the reverse geocoder.",
		Tags: []string{"Curation"},
	}, a.searchWikidata)

	huma.Register(api, invalidates(cache.Subjects)(huma.Operation{
		OperationID: "attach-subject-entity",
		Method:      http.MethodPost,
		Path:        "/v1/curation/vocabulary/{id}/entity",
		Summary:     "Give a subject an identity, or relate it to one",
		Description: "`self` attributes the entity to this subject. `parent` and `child` create " +
			"the entity as its own subject — or reuse the existing one — and link it. " +
			"Creating ancestors is what makes a hierarchy possible at all: measured on a " +
			"real vocabulary, relations between subjects that already existed connected " +
			"5 of 41.",
		Tags: []string{"Curation"},
	}), a.attachSubjectEntity)

	huma.Register(api, invalidates(cache.Subjects)(huma.Operation{
		OperationID: "detach-subject-entity",
		Method:      http.MethodDelete,
		Path:        "/v1/curation/vocabulary/{id}/entity",
		Summary:     "Remove a subject's identity",
		Description: "Deletes the names derived from it too. Those are live in the matching path, " +
			"so leaving them would keep recognising doléances by the wrong entity's names.",
		Tags: []string{"Curation"},
	}), a.detachSubjectEntity)

	huma.Register(api, huma.Operation{
		OperationID: "unlink-subjects",
		Method:      http.MethodDelete,
		Path:        "/v1/curation/vocabulary/{id}/relations/{other}",
		Summary:     "Remove a relation between two subjects",
		Tags:        []string{"Curation"},
	}, a.unlinkSubjects)
}

// SubjectSearchInput is a query over the vocabulary.
type SubjectSearchInput struct {
	Query string `query:"q"`
	Limit int    `query:"limit" default:"30"`
}

// SubjectListOutput is a plain list of subjects.
type SubjectListOutput struct {
	Body struct {
		Subjects []VocabularyEntry `json:"subjects"`
	}
}

func (a *API) searchSubjects(ctx context.Context, in *SubjectSearchInput) (*SubjectListOutput, error) {
	found, err := a.store.SearchSubjects(ctx, in.Query, in.Limit)
	if err != nil {
		log.Error().Err(err).Msg("cannot search the vocabulary")
		return nil, huma.Error500InternalServerError("cannot search the vocabulary")
	}
	return a.vocabularyList(ctx, found), nil
}

func (a *API) listDetachedSubjects(ctx context.Context, _ *struct{}) (*SubjectListOutput, error) {
	found, err := a.store.ListDetachedSubjects(ctx)
	if err != nil {
		log.Error().Err(err).Msg("cannot list the detached subjects")
		return nil, huma.Error500InternalServerError("cannot list the detached subjects")
	}
	return a.vocabularyList(ctx, found), nil
}

func (a *API) vocabularyList(ctx context.Context, subjects []models.Subject) *SubjectListOutput {
	out := &SubjectListOutput{}
	out.Body.Subjects = make([]VocabularyEntry, 0, len(subjects))
	for _, subject := range subjects {
		out.Body.Subjects = append(out.Body.Subjects, a.vocabularyEntry(ctx, subject))
	}
	return out
}

// SubjectDetail is one subject as the editing page needs it.
type SubjectDetail struct {
	VocabularyEntry

	// Parents and Children are the direct neighbours only. A curator is
	// editing one subject, and showing the whole graph would be showing them
	// a diagram instead of a decision.
	Parents  []VocabularyEntry `json:"parents,omitempty"`
	Children []VocabularyEntry `json:"children,omitempty"`
}

// SubjectDetailOutput is one subject.
type SubjectDetailOutput struct {
	Body SubjectDetail
}

func (a *API) getSubject(ctx context.Context, in *MessageIDInput) (*SubjectDetailOutput, error) {
	var subject models.Subject
	if err := a.store.DB().WithContext(ctx).First(&subject, "id = ?", in.ID).Error; err != nil {
		return nil, huma.Error404NotFound("no such subject")
	}

	kin, err := a.store.SubjectRelations(ctx, in.ID)
	if err != nil {
		log.Error().Err(err).Str("subject", in.ID).Msg("cannot read a subject's relations")
	}

	out := &SubjectDetailOutput{}
	out.Body.VocabularyEntry = a.vocabularyEntry(ctx, subject)
	for _, parent := range kin.Parents {
		out.Body.Parents = append(out.Body.Parents, a.vocabularyEntry(ctx, parent))
	}
	for _, child := range kin.Children {
		out.Body.Children = append(out.Body.Children, a.vocabularyEntry(ctx, child))
	}
	return out, nil
}

// WikidataSearchInput is a lookup for the console's entity picker.
type WikidataSearchInput struct {
	Query    string `query:"q"`
	Language string `query:"language"`
}

// WikidataCandidate is one entity offered to a curator.
type WikidataCandidate struct {
	QID         string `json:"qid"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Entity      string `json:"entity"`
	Article     string `json:"article,omitempty"`

	// MatchType is how the search found it. An alias match is the shape of the
	// worst errors — "transports spatiaux" is why spaceflight was once offered
	// for "transports" — so the curator sees it before choosing.
	MatchType string `json:"match_type,omitempty"`

	// Held names the subject already carrying this entity, if there is one.
	// Choosing it as a parent or child then links that subject rather than
	// inventing a second row for one concept.
	Held     string `json:"held,omitempty"`
	HeldID   string `json:"held_id,omitempty"`
	HeldSelf bool   `json:"held_self,omitempty"`
}

// WikidataSearchOutput is the candidate list.
type WikidataSearchOutput struct {
	Body struct {
		Candidates []WikidataCandidate `json:"candidates"`
	}
}

func (a *API) searchWikidata(ctx context.Context, in *WikidataSearchInput) (*WikidataSearchOutput, error) {
	out := &WikidataSearchOutput{}
	out.Body.Candidates = []WikidataCandidate{}

	query := strings.TrimSpace(in.Query)
	if query == "" || a.wikidata == nil {
		return out, nil
	}

	language := in.Language
	if len(language) != 2 {
		language = "en"
	}

	results, err := a.wikidata.Search(ctx, query, language)
	if err != nil {
		// Reported as an answer rather than a server fault: the curator asked
		// a third party a question and it did not reply, which is information
		// they can act on.
		log.Info().Err(err).Str("query", query).Msg("wikidata search failed for a curator")
		return nil, huma.Error502BadGateway("Wikidata did not answer: " + err.Error())
	}

	for _, result := range results {
		candidate := WikidataCandidate{
			QID:         result.QID,
			Label:       result.Label,
			Description: result.Description,
			Entity:      wikidata.EntityURL(result.QID),
			Article:     wikidata.ArticleURL(result.QID, language),
			MatchType:   result.MatchType,
		}
		if held, err := a.store.SubjectForEntity(ctx, result.QID); err == nil {
			candidate.Held = held.Label
			candidate.HeldID = held.ID
		}
		out.Body.Candidates = append(out.Body.Candidates, candidate)
	}
	return out, nil
}

// AttachEntityInput is a curator's choice from the entity search.
type AttachEntityInput struct {
	ID   string `path:"id"`
	Body struct {
		QID   string `json:"qid"`
		Label string `json:"label"`

		// Relation is "self", "parent" or "child".
		Relation string `json:"relation"`

		Actor  string `json:"actor,omitempty"`
		Reason string `json:"reason,omitempty"`
	}
}

func (a *API) attachSubjectEntity(ctx context.Context, in *AttachEntityInput) (*SubjectDecisionOutput, error) {
	var subject models.Subject
	if err := a.store.DB().WithContext(ctx).First(&subject, "id = ?", in.ID).Error; err != nil {
		return nil, huma.Error404NotFound("no such subject")
	}

	qid := strings.TrimSpace(in.Body.QID)
	if qid == "" {
		return nil, huma.Error422UnprocessableEntity("an entity is needed")
	}

	out := &SubjectDecisionOutput{}
	out.Body.ID = in.ID

	switch in.Body.Relation {
	case "self":
		updated, err := a.store.AttachSubjectEntity(ctx, in.ID, qid)
		if err != nil {
			log.Error().Err(err).Str("subject", in.ID).Msg("cannot attach an entity")
			return nil, huma.Error500InternalServerError("cannot attach the entity")
		}
		a.recordDecision(ctx, in.Body.Actor, models.AuditConfirmEntity, "subject",
			subject.ID+" = "+qid, in.Body.Reason)
		log.Info().Str("subject", subject.Label).Str("qid", qid).
			Str("actor", in.Body.Actor).Msg("entity attached by a curator")

		// The identity is vouched for, so its names in every language the
		// register speaks are worth having — and only now.
		a.learnTranslations(ctx, updated)
		out.Body.Decided = "attached"

	case "parent", "child":
		other, err := a.relatedSubject(ctx, qid, strings.TrimSpace(in.Body.Label), subject.Language)
		if err != nil {
			log.Error().Err(err).Str("qid", qid).Msg("cannot resolve the related subject")
			return nil, huma.Error500InternalServerError("cannot create the related subject")
		}

		child, parent := subject.ID, other.ID
		if in.Body.Relation == "child" {
			child, parent = other.ID, subject.ID
		}
		if err := a.store.LinkSubjects(ctx, child, parent, models.RelationSourceCurator); err != nil {
			if errors.Is(err, store.ErrSubjectCycle) {
				return nil, huma.Error422UnprocessableEntity(
					"that would make the subject its own ancestor")
			}
			log.Error().Err(err).Msg("cannot link two subjects")
			return nil, huma.Error500InternalServerError("cannot link the subjects")
		}

		a.recordDecision(ctx, in.Body.Actor, models.AuditLinkSubjects, "subject",
			child+" under "+parent, in.Body.Reason)
		log.Info().Str("child", child).Str("parent", parent).
			Str("actor", in.Body.Actor).Msg("subjects linked by a curator")
		out.Body.Decided = in.Body.Relation

	default:
		return nil, huma.Error422UnprocessableEntity(
			`relation must be "self", "parent" or "child"`)
	}
	return out, nil
}

// relatedSubject finds or creates the subject for an entity.
//
// Finding first matters: the concept may already be in the vocabulary under a
// name the curator did not think to search for, and a second row for one
// concept is the duplicate this whole layer exists to prevent.
func (a *API) relatedSubject(ctx context.Context, qid, label, language string) (models.Subject, error) {
	if existing, err := a.store.SubjectForEntity(ctx, qid); err == nil {
		return existing, nil
	}

	created, err := a.store.CreateSubjectFromEntity(ctx, label, language, qid)
	if err != nil {
		return created, err
	}
	log.Info().Str("subject", created.Label).Str("qid", qid).
		Msg("subjects: a subject was created from an entity a curator chose")

	a.learnTranslations(ctx, created)
	return created, nil
}

func (a *API) detachSubjectEntity(ctx context.Context, in *SubjectDecisionInput) (*SubjectDecisionOutput, error) {
	if err := a.store.DetachSubjectEntity(ctx, in.ID); err != nil {
		log.Error().Err(err).Str("subject", in.ID).Msg("cannot detach an entity")
		return nil, huma.Error500InternalServerError("cannot detach the entity")
	}

	a.recordDecision(ctx, in.Body.Actor, models.AuditRejectEntity, "subject", in.ID, in.Body.Reason)
	log.Info().Str("subject", in.ID).Str("actor", in.Body.Actor).
		Msg("entity detached by a curator, with the names derived from it")

	out := &SubjectDecisionOutput{}
	out.Body.ID = in.ID
	out.Body.Decided = "detached"
	return out, nil
}

// UnlinkInput removes one relation.
type UnlinkInput struct {
	ID    string `path:"id"`
	Other string `path:"other"`
	Body  struct {
		Actor string `json:"actor,omitempty"`
	}
}

func (a *API) unlinkSubjects(ctx context.Context, in *UnlinkInput) (*SubjectDecisionOutput, error) {
	// The relation is stored in one direction and the console may ask from
	// either end, so both are removed; only one of them exists.
	for _, pair := range [][2]string{{in.ID, in.Other}, {in.Other, in.ID}} {
		if err := a.store.UnlinkSubjects(ctx, pair[0], pair[1]); err != nil {
			log.Error().Err(err).Msg("cannot unlink two subjects")
			return nil, huma.Error500InternalServerError("cannot remove the relation")
		}
	}

	a.recordDecision(ctx, in.Body.Actor, models.AuditUnlinkSubjects, "subject",
		in.ID+" / "+in.Other, "")

	out := &SubjectDecisionOutput{}
	out.Body.ID = in.ID
	out.Body.Decided = "unlinked"
	return out, nil
}
