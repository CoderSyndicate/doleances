package backend

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CoderSyndicate/doleances/internal/cache"
	"github.com/CoderSyndicate/doleances/internal/models"
	"github.com/CoderSyndicate/doleances/internal/store"
)

func newAPI(t *testing.T) *API {
	t.Helper()

	db, err := store.Open(store.Options{
		Driver: store.DriverSQLite,
		DSN:    filepath.Join(t.TempDir(), "test.db"),
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close(context.Background()) }) //nolint:errcheck

	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	// With a cache, so every test in this package exercises the cached read
	// paths and the drops the sweeps perform rather than a configuration
	// nothing ships with.
	return &API{
		store:     db,
		languages: []string{"fr", "de"},
		cache:     cache.New(cache.DefaultTTL, cache.DefaultLimit),
	}
}

// TestNoQIDIsAttributedWithoutAPerson is the rule the whole cascade turns on.
//
// An identity decides what a subject is called in fifty languages and which
// other subjects are merged into it. An unconfirmed pipeline got that wrong on
// a third of real subjects — spaceflight for "transports", solitary
// confinement for "isolement", a Belgian magazine for "laïcité" — and wrote
// each of them into the matching path, where a German doléance about prison
// isolation would have resolved to rural loneliness.
//
// So a subject is created with no identity at all, and the proposal is a
// separate, answerable thing.
func TestNoQIDIsAttributedWithoutAPerson(t *testing.T) {
	a := newAPI(t)
	ctx := context.Background()

	message := models.Message{Text: "personne ne passe plus me voir", TokenHash: "h"}
	if err := a.store.CreateMessage(ctx, &message); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	ids := a.createAndAsk(ctx, nil, models.LLMSettings{}, message,
		[]candidate{{label: "isolement"}}, "fr", nil, "", nil)
	if len(ids) != 1 {
		t.Fatalf("created %d subjects, want 1", len(ids))
	}

	var subject models.Subject
	if err := a.store.DB().First(&subject, "id = ?", ids[0]).Error; err != nil {
		t.Fatalf("read subject: %v", err)
	}
	if subject.QID != "" {
		t.Errorf("a QID was attributed with nobody involved: %q", subject.QID)
	}
	if subject.QIDConfirmedAt != nil {
		t.Error("an identity was marked confirmed with nobody involved")
	}
}

// TestConfirmingAnEntityIsTheOnlyPathToAQID, and it is what unlocks the
// translations: an unconfirmed entity must teach the vocabulary nothing.
func TestConfirmingAnEntityIsTheOnlyPathToAQID(t *testing.T) {
	a := newAPI(t)
	ctx := context.Background()

	subject, err := a.store.CreateSubject(ctx, "isolement", "fr", "", nil, "")
	if err != nil {
		t.Fatalf("CreateSubject: %v", err)
	}

	err = a.store.ProposeSubjectEntity(ctx, models.SubjectEntity{
		SubjectID:   subject.ID,
		QID:         "Q6010868",
		Label:       "isolement",
		Description: "état d'isolement d'une personne",
		Confidence:  90,
	})
	if err != nil {
		t.Fatalf("ProposeSubjectEntity: %v", err)
	}

	// Proposed is not attributed.
	var proposed models.Subject
	if err := a.store.DB().First(&proposed, "id = ?", subject.ID).Error; err != nil {
		t.Fatalf("read subject: %v", err)
	}
	if proposed.QID != "" {
		t.Errorf("a proposal attributed %q on its own", proposed.QID)
	}

	pending, err := a.store.ListSubjectEntityProposals(ctx)
	if err != nil {
		t.Fatalf("ListSubjectEntityProposals: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("got %d proposals, want 1", len(pending))
	}

	confirmed, err := a.store.ConfirmSubjectEntity(ctx, pending[0].ID)
	if err != nil {
		t.Fatalf("ConfirmSubjectEntity: %v", err)
	}
	if confirmed.QID != "Q6010868" || confirmed.QIDConfirmedAt == nil {
		t.Errorf("confirming did not attribute the entity: %+v", confirmed)
	}

	// And the question is not asked again.
	pending, err = a.store.ListSubjectEntityProposals(ctx)
	if err != nil {
		t.Fatalf("ListSubjectEntityProposals: %v", err)
	}
	if len(pending) != 0 {
		t.Error("an answered proposal came back")
	}
}

// TestARejectedEntityIsNotProposedAgain: a curator who said "that is not this
// subject" must not be asked on the next doléance carrying the word.
func TestARejectedEntityIsNotProposedAgain(t *testing.T) {
	a := newAPI(t)
	ctx := context.Background()

	subject, _ := a.store.CreateSubject(ctx, "transports", "fr", "", nil, "")
	proposal := models.SubjectEntity{
		SubjectID: subject.ID, QID: "Q5916", Label: "vol spatial", Confidence: 20,
	}
	if err := a.store.ProposeSubjectEntity(ctx, proposal); err != nil {
		t.Fatalf("ProposeSubjectEntity: %v", err)
	}

	pending, _ := a.store.ListSubjectEntityProposals(ctx)
	if len(pending) != 1 {
		t.Fatalf("got %d proposals, want 1", len(pending))
	}
	if err := a.store.RejectSubjectEntity(ctx, pending[0].ID); err != nil {
		t.Fatalf("RejectSubjectEntity: %v", err)
	}

	if err := a.store.ProposeSubjectEntity(ctx, proposal); err != nil {
		t.Fatalf("ProposeSubjectEntity: %v", err)
	}
	pending, _ = a.store.ListSubjectEntityProposals(ctx)
	if len(pending) != 0 {
		t.Error("a rejected entity was proposed again")
	}
}

// TestAgreementNeedsTwoOpinions: "alone" is the truthful answer when only one
// system spoke, and presenting it as agreement would invent a corroboration
// that never happened — on the one signal known to land on a guest house.
func TestAgreementNeedsTwoOpinions(t *testing.T) {
	settings := models.LLMSettings{}.WithThresholdDefaults()

	tests := []struct {
		name       string
		suggestion store.SubjectMergeSuggestion
		want       string
	}{
		{
			name: "no vector to compare",
			suggestion: store.SubjectMergeSuggestion{
				Source: models.MergeSourceWikidata, Evidence: "Q12147",
			},
			want: agreementAlone,
		},
		{
			name: "the vectors back the entity",
			suggestion: store.SubjectMergeSuggestion{
				Source: models.MergeSourceWikidata, Evidence: "Q12147", Similarity: 0.91,
			},
			want: agreementAgree,
		},
		{
			name: "the vectors see nothing in common",
			suggestion: store.SubjectMergeSuggestion{
				Source: models.MergeSourceWikidata, Evidence: "Q946865", Similarity: 0.24,
			},
			want: agreementDisagree,
		},
		{
			name: "cross-language is judged by its own threshold",
			suggestion: store.SubjectMergeSuggestion{
				Source: models.MergeSourceWikidata, Evidence: "Q12147",
				Similarity: 0.66, CrossLanguage: true,
			},
			want: agreementAgree,
		},
		{
			name: "an embedding question has nothing to corroborate it",
			suggestion: store.SubjectMergeSuggestion{
				Source: models.MergeSourceEmbedding, Similarity: 0.88,
			},
			want: agreementAlone,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := agreement(test.suggestion, settings); got != test.want {
				t.Errorf("agreement = %q, want %q", got, test.want)
			}
		})
	}
}

// TestSameLanguageThresholdIsNotUsedAcrossLanguages guards the number that was
// silently zero once before: 0.66 is a convincing cross-language score and an
// unconvincing same-language one, and judging by the wrong one either floods a
// curator or hides a real duplicate.
func TestSameLanguageThresholdIsNotUsedAcrossLanguages(t *testing.T) {
	settings := models.LLMSettings{}.WithThresholdDefaults()

	sameLanguage := store.SubjectMergeSuggestion{
		Source: models.MergeSourceWikidata, Evidence: "Q12147", Similarity: 0.66,
	}
	if agreement(sameLanguage, settings) != agreementDisagree {
		t.Error("0.66 between two labels in one language should not read as agreement")
	}

	crossLanguage := sameLanguage
	crossLanguage.CrossLanguage = true
	if agreement(crossLanguage, settings) != agreementAgree {
		t.Error("0.66 across two languages should read as agreement")
	}
}

// TestEachSideCarriesItsOwnEntity: the card used to show an entity only when
// Wikidata had raised the question, so a pair like "accès aux soins"
// (Q2822913) and "healthcare access" (Q67075251) — raised by the embedding
// layer — appeared with no identity at all, hiding the strongest evidence
// available for the decision.
func TestEachSideCarriesItsOwnEntity(t *testing.T) {
	tests := []struct {
		name  string
		left  string
		right string
		want  string
	}{
		{"one entity, two subjects", "Q12147", "Q12147", entitiesSame},
		{"two entities", "Q2822913", "Q67075251", entitiesDifferent},
		// An absence is not an argument: one side having an identity says
		// nothing at all about the other.
		{"only one side resolved", "Q12147", "", entitiesUnknown},
		{"neither resolved", "", "", entitiesUnknown},
	}
	for _, test := range tests {
		if got := comparedEntities(test.left, test.right); got != test.want {
			t.Errorf("%s: entities = %q, want %q", test.name, got, test.want)
		}
	}

	// And each side links to its own entity and its own article, not the
	// pair's — an embedding-raised question has no shared entity to link to.
	side := subjectSide(models.Subject{
		Model: models.Model{ID: "s1"}, Label: "accès aux soins",
		Language: "fr", QID: "Q2822913",
	})
	if side.QID != "Q2822913" {
		t.Errorf("qid = %q", side.QID)
	}
	if !strings.Contains(side.Entity, "Q2822913") {
		t.Errorf("entity link = %q, want the side's own entity", side.Entity)
	}
	if !strings.Contains(side.Article, "frwiki/Q2822913") {
		t.Errorf("article link = %q, want the French article for this entity", side.Article)
	}

	// A subject with no confirmed entity offers no links rather than broken
	// ones.
	bare := subjectSide(models.Subject{
		Model: models.Model{ID: "s2"}, Label: "pouvoir d'achat", Language: "fr",
	})
	if bare.QID != "" || bare.Entity != "" || bare.Article != "" {
		t.Errorf("an unidentified subject produced links: %+v", bare)
	}
}

// TestArrangeAsTree covers the ordering the filter indents by.
func TestArrangeAsTree(t *testing.T) {
	sub := func(id, label string) models.Subject {
		return models.Subject{Model: models.Model{ID: id}, Label: label}
	}
	// As the store returns them: sorted by the label a reader sees.
	subjects := []models.Subject{
		sub("bus", "bus"), sub("h", "health"),
		sub("pt", "public transport"), sub("t", "transport"),
	}

	got := arrangeAsTree(subjects, []models.SubjectRelation{
		{ChildID: "pt", ParentID: "t"},
		{ChildID: "bus", ParentID: "pt"},
	})

	want := []struct {
		label string
		depth int
	}{
		{"health", 0}, // a root with nothing under it
		{"transport", 0},
		{"public transport", 1},
		{"bus", 2},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d", len(got), len(want))
	}
	for i, expected := range want {
		if got[i].Label != expected.label || got[i].Depth != expected.depth {
			t.Errorf("row %d = %q at depth %d, want %q at %d",
				i, got[i].Label, got[i].Depth, expected.label, expected.depth)
		}
	}
}

// TestArrangeAsTreeShowsEachSubjectOnce. The hierarchy is a graph: "public
// transport" sits under both "transport service" and "public service", and
// both are true. A filter cannot draw that honestly — two checkboxes for one
// subject are two ways to select the same thing — so it appears once.
func TestArrangeAsTreeShowsEachSubjectOnce(t *testing.T) {
	sub := func(id, label string) models.Subject {
		return models.Subject{Model: models.Model{ID: id}, Label: label}
	}
	subjects := []models.Subject{
		sub("pt", "public transport"), sub("ps", "public service"), sub("ts", "transport service"),
	}

	got := arrangeAsTree(subjects, []models.SubjectRelation{
		{ChildID: "pt", ParentID: "ts"},
		{ChildID: "pt", ParentID: "ps"},
	})

	seen := map[string]int{}
	for _, entry := range got {
		seen[entry.Label]++
	}
	if seen["public transport"] != 1 {
		t.Errorf("public transport appears %d times, want once", seen["public transport"])
	}
	if len(got) != 3 {
		t.Errorf("got %d rows for 3 subjects", len(got))
	}
}

// TestArrangeAsTreeSurvivesACycle. The store refuses cycles; this runs on
// every page that offers a filter, and trusting that would be trusting the
// wrong thing. A loop must produce a flat tail, never a hang.
func TestArrangeAsTreeSurvivesACycle(t *testing.T) {
	sub := func(id, label string) models.Subject {
		return models.Subject{Model: models.Model{ID: id}, Label: label}
	}
	subjects := []models.Subject{sub("a", "a"), sub("b", "b"), sub("c", "c")}

	done := make(chan []SubjectFilterEntry, 1)
	go func() {
		done <- arrangeAsTree(subjects, []models.SubjectRelation{
			{ChildID: "b", ParentID: "a"},
			{ChildID: "c", ParentID: "b"},
			{ChildID: "a", ParentID: "c"},
		})
	}()

	select {
	case got := <-done:
		if len(got) != 3 {
			t.Errorf("got %d rows, want every subject listed exactly once", len(got))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("arrangeAsTree did not terminate on a cycle")
	}
}
