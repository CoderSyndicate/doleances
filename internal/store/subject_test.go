package store

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/CoderSyndicate/doleances/internal/models"
	"github.com/CoderSyndicate/doleances/internal/subjects"
)

// TestSubjectKeysAreDerived: a subject written by any path — a seed, a
// snapshot restore, a test — has to be findable by the deduplication layers.
// A row whose keys do not match its own label is invisible to layer one, and
// the next spelling of it becomes a duplicate nobody can explain.
func TestSubjectKeysAreDerived(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	subject := models.Subject{Label: "Accès aux soins"}
	if err := s.DB().WithContext(ctx).Create(&subject).Error; err != nil {
		t.Fatalf("create: %v", err)
	}

	if subject.MatchKey != subjects.MatchKey("Accès aux soins") {
		t.Errorf("match key = %q, not derived from the label", subject.MatchKey)
	}
	if subject.FoldKey == "" {
		t.Error("no fold key was derived")
	}
	if subject.Slug != "acces-aux-soins" {
		t.Errorf("slug = %q, want acces-aux-soins", subject.Slug)
	}

	// And it is findable by the key that was derived.
	if _, err := s.FindSubjectByMatchKey(ctx, subjects.MatchKey("ACCÈS AUX SOINS")); err != nil {
		t.Errorf("a differently spelled label did not find the subject: %v", err)
	}
}

// TestLayerOneMergesSpellings covers the cheap path: the same subject typed
// differently must not become two rows.
func TestLayerOneMergesSpellings(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	first, err := s.CreateSubject(ctx, "Santé", "fr", "", nil, "")
	if err != nil {
		t.Fatalf("CreateSubject: %v", err)
	}

	for _, spelling := range []string{"santé", "SANTE", " Santé ", "sante"} {
		found, err := s.FindSubjectByMatchKey(ctx, subjects.MatchKey(spelling))
		if err != nil {
			t.Errorf("%q did not resolve: %v", spelling, err)
			continue
		}
		if found.ID != first.ID {
			t.Errorf("%q resolved to a different subject", spelling)
		}
	}
}

func TestLayerTwoMergesPlurals(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	created, err := s.CreateSubject(ctx, "transport", "fr", "", nil, "")
	if err != nil {
		t.Fatalf("CreateSubject: %v", err)
	}

	// "transports" is not an exact key match, so layer one misses it.
	if _, err := s.FindSubjectByMatchKey(ctx, subjects.MatchKey("transports")); !errors.Is(err, ErrSubjectNotFound) {
		t.Error("layer one matched a plural, which is layer two's job")
	}

	found, err := s.FindSubjectByFoldKey(ctx, subjects.FoldPlural(subjects.MatchKey("transports")))
	if err != nil {
		t.Fatalf("layer two did not match the plural: %v", err)
	}
	if found.ID != created.ID {
		t.Error("the plural resolved to a different subject")
	}
}

// TestVectorsRoundTrip: the similarity layer is worthless if a stored vector
// comes back as something else.
func TestVectorsRoundTrip(t *testing.T) {
	original := []float32{0.1, -0.5, 0.9, 0, 1e-7}

	decoded := DecodeVector(EncodeVector(original))
	if len(decoded) != len(original) {
		t.Fatalf("decoded %d values, want %d", len(decoded), len(original))
	}
	for i := range original {
		if math.Abs(float64(decoded[i]-original[i])) > 1e-9 {
			t.Errorf("value %d: got %v, want %v", i, decoded[i], original[i])
		}
	}

	// Corrupt input degrades to nothing rather than to wrong numbers: a
	// subject with no usable vector simply takes no part in matching.
	if DecodeVector([]byte{1, 2, 3}) != nil {
		t.Error("a truncated blob should decode to nothing")
	}
}

// TestVectorsAreScopedToTheirModel: comparing across two embedding spaces
// produces confident nonsense, which here means merging unrelated subjects.
func TestVectorsAreScopedToTheirModel(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	if _, err := s.CreateSubject(ctx, "santé", "fr", "", []float32{1, 0, 0}, "bge-m3"); err != nil {
		t.Fatalf("CreateSubject: %v", err)
	}
	if _, err := s.CreateSubject(ctx, "logement", "fr", "", []float32{0, 1, 0}, "some-other-model"); err != nil {
		t.Fatalf("CreateSubject: %v", err)
	}

	vectors, err := s.ListSubjectVectors(ctx, "bge-m3")
	if err != nil {
		t.Fatalf("ListSubjectVectors: %v", err)
	}
	if len(vectors) != 1 {
		t.Fatalf("got %d vectors, want only the one from bge-m3", len(vectors))
	}
	if vectors[0].Subject.Label != "santé" {
		t.Errorf("got %q, want the subject embedded by the requested model", vectors[0].Subject.Label)
	}
}

// TestMergeKeepsEveryClassification is the property a curator is trusting:
// folding two subjects together must not cost any doléance its subject.
func TestMergeKeepsEveryClassification(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	logement, _ := s.CreateSubject(ctx, "logement", "fr", "", nil, "")
	habitat, _ := s.CreateSubject(ctx, "habitat", "fr", "", nil, "")

	// One message on each, and one on both — the case a naive merge breaks on
	// a duplicate key.
	var ids []string
	for _, text := range []string{"sur le logement", "sur l'habitat", "sur les deux"} {
		message := models.Message{Text: text, Status: models.StatusAccepted, TokenHash: "h"}
		if err := s.CreateMessage(ctx, &message); err != nil {
			t.Fatalf("CreateMessage: %v", err)
		}
		ids = append(ids, message.ID)
	}
	mustAttach(t, s, ids[0], logement.ID)
	mustAttach(t, s, ids[1], habitat.ID)
	mustAttach(t, s, ids[2], logement.ID, habitat.ID)

	if err := s.MergeSubjects(ctx, habitat.ID, logement.ID); err != nil {
		t.Fatalf("MergeSubjects: %v", err)
	}

	// Every message now carries the survivor, and none carries the subject
	// that is gone.
	for _, id := range ids {
		message, err := s.GetMessage(ctx, id)
		if err != nil {
			t.Fatalf("GetMessage: %v", err)
		}
		if len(message.Subjects) != 1 {
			t.Errorf("%q has %d subjects, want 1", message.Text, len(message.Subjects))
			continue
		}
		if message.Subjects[0].ID != logement.ID {
			t.Errorf("%q kept the merged-away subject", message.Text)
		}
	}

	// And the merged subject is gone from the vocabulary.
	all, err := s.ListSubjects(ctx)
	if err != nil {
		t.Fatalf("ListSubjects: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("the vocabulary holds %d subjects, want 1", len(all))
	}
}

// TestDismissedSuggestionIsNotAskedAgain: a curator who said "these are not
// the same" must not be asked on the next submission, for ever.
func TestDismissedSuggestionIsNotAskedAgain(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	a, _ := s.CreateSubject(ctx, "paix", "fr", "", nil, "")
	b, _ := s.CreateSubject(ctx, "pays", "fr", "", nil, "")

	if err := s.SuggestSubjectMerge(ctx, MergeProposal{
		SubjectID: a.ID, IntoID: b.ID, Similarity: 0.82,
	}); err != nil {
		t.Fatalf("SuggestSubjectMerge: %v", err)
	}

	pending, err := s.ListSubjectMergeSuggestions(ctx)
	if err != nil {
		t.Fatalf("ListSubjectMergeSuggestions: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("got %d suggestions, want 1", len(pending))
	}

	if err := s.DismissSubjectMerge(ctx, pending[0].ID); err != nil {
		t.Fatalf("DismissSubjectMerge: %v", err)
	}

	// Proposed again — in either order, since the pair is one question.
	if err := s.SuggestSubjectMerge(ctx, MergeProposal{
		SubjectID: b.ID, IntoID: a.ID, Similarity: 0.83,
	}); err != nil {
		t.Fatalf("SuggestSubjectMerge: %v", err)
	}
	pending, err = s.ListSubjectMergeSuggestions(ctx)
	if err != nil {
		t.Fatalf("ListSubjectMergeSuggestions: %v", err)
	}
	if len(pending) != 0 {
		t.Error("a dismissed pair was asked about again")
	}
}

func mustAttach(t *testing.T, s *Store, messageID string, subjectIDs ...string) {
	t.Helper()
	if err := s.AttachSubjects(context.Background(), messageID, subjectIDs); err != nil {
		t.Fatalf("AttachSubjects: %v", err)
	}
}

// TestSuggestionCarriesBothSignals: the question a curator answers is only
// worth asking if it shows what each system said. A row that kept the QID and
// dropped the cosine would present a Wikidata guess as if nothing disagreed
// with it.
func TestSuggestionCarriesBothSignals(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	a, _ := s.CreateSubject(ctx, "retraite", "fr", "Q946865", nil, "")
	b, _ := s.CreateSubject(ctx, "pension", "en", "Q946865", nil, "")

	message := models.Message{Text: "ma retraite ne suffit plus", TokenHash: "h"}
	if err := s.CreateMessage(ctx, &message); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	err := s.SuggestSubjectMerge(ctx, MergeProposal{
		SubjectID:     b.ID,
		IntoID:        a.ID,
		MessageID:     message.ID,
		Source:        models.MergeSourceWikidata,
		Evidence:      "Q946865",
		Similarity:    0.31, // the vectors disagree, which is the useful part
		CrossLanguage: true,
	})
	if err != nil {
		t.Fatalf("SuggestSubjectMerge: %v", err)
	}

	pending, err := s.ListSubjectMergeSuggestions(ctx)
	if err != nil {
		t.Fatalf("ListSubjectMergeSuggestions: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("got %d suggestions, want 1", len(pending))
	}

	got := pending[0]
	if got.Evidence != "Q946865" {
		t.Errorf("evidence = %q, want the shared QID", got.Evidence)
	}
	if got.Similarity != 0.31 {
		t.Errorf("similarity = %v, want the second opinion to survive", got.Similarity)
	}
	if !got.CrossLanguage {
		t.Error("the pair is French and English and was not marked cross-language")
	}
	// Without the doléance the question is a linguistics exam.
	if got.Message == nil {
		t.Fatal("the suggestion lost the message that raised it")
	}
	if got.Message.ID != message.ID {
		t.Errorf("message = %q, want the one being classified", got.Message.Text)
	}
}

// TestWikidataQuestionsComeFirst: a shared entity is better evidence than a
// close vector, so it is what a curator with five minutes should see.
func TestWikidataQuestionsComeFirst(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	create := func(label string) models.Subject {
		subject, err := s.CreateSubject(ctx, label, "fr", "", nil, "")
		if err != nil {
			t.Fatalf("CreateSubject(%q): %v", label, err)
		}
		return subject
	}

	a, b := create("logement"), create("habitat")
	c, d := create("santé"), create("soins")

	// The embedding question scores higher, and still comes second.
	if err := s.SuggestSubjectMerge(ctx, MergeProposal{
		SubjectID: a.ID, IntoID: b.ID,
		Source: models.MergeSourceEmbedding, Similarity: 0.88,
	}); err != nil {
		t.Fatalf("SuggestSubjectMerge: %v", err)
	}
	if err := s.SuggestSubjectMerge(ctx, MergeProposal{
		SubjectID: c.ID, IntoID: d.ID,
		Source: models.MergeSourceWikidata, Evidence: "Q12147", Similarity: 0.61,
	}); err != nil {
		t.Fatalf("SuggestSubjectMerge: %v", err)
	}

	pending, err := s.ListSubjectMergeSuggestions(ctx)
	if err != nil {
		t.Fatalf("ListSubjectMergeSuggestions: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("got %d suggestions, want 2", len(pending))
	}
	if pending[0].Source != models.MergeSourceWikidata {
		t.Errorf("first question came from %q, want the Wikidata one", pending[0].Source)
	}
}

// TestAliasesResolveLikeSubjects is the whole point of pre-filling from
// Wikidata: a spelling the register has never been given still resolves, for
// one index read, with no model call and no curator involved.
func TestAliasesResolveLikeSubjects(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	health, err := s.CreateSubject(ctx, "santé", "fr", "Q12147", nil, "")
	if err != nil {
		t.Fatalf("CreateSubject: %v", err)
	}

	added, err := s.AddSubjectAliases(ctx, health.ID, models.AliasSourceWikidata,
		map[string]string{"de": "Gesundheit", "en": "health", "fr": "santé"})
	if err != nil {
		t.Fatalf("AddSubjectAliases: %v", err)
	}
	// Three names, but the French one is the subject's own label: an alias
	// that shadowed a real subject would make one word resolve two ways.
	if added != 2 {
		t.Errorf("stored %d spellings, want 2 — the label's own must be skipped", added)
	}

	for spelling, language := range map[string]string{"Gesundheit": "de", "health": "en"} {
		found, err := s.FindSubjectByAlias(ctx, subjects.MatchKey(spelling), language)
		if err != nil {
			t.Fatalf("FindSubjectByAlias(%q): %v", spelling, err)
		}
		if found.ID != health.ID {
			t.Errorf("%q resolved to %q, want santé", spelling, found.Label)
		}
	}

	// And only in its own language. A German alias answering an English
	// doléance is the homograph failure this scoping exists to stop.
	if _, err := s.FindSubjectByAlias(ctx, subjects.MatchKey("Gesundheit"), "en"); !errors.Is(err, ErrSubjectNotFound) {
		t.Errorf("err = %v, want a German alias to be invisible to an English doléance", err)
	}
	// An unnamed language matches nothing rather than everything.
	if _, err := s.FindSubjectByAlias(ctx, subjects.MatchKey("Gesundheit"), ""); !errors.Is(err, ErrSubjectNotFound) {
		t.Errorf("err = %v, want no match when the language is unknown", err)
	}
}

// TestOneWordCanBeTwoSubjectsInTwoLanguages is why the alias key is unique per
// language rather than globally.
//
// Pre-filling fifty languages means the same string is routinely two different
// words — "pain" is bread in French and suffering in English, and Catalan
// "salut" is a real label of Q12147, health, where French "salut" is a
// greeting. A global key would give the word to whichever entity was imported
// first and silently drop the other.
func TestOneWordCanBeTwoSubjectsInTwoLanguages(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	bread, _ := s.CreateSubject(ctx, "boulangerie", "fr", "Q131734", nil, "")
	suffering, _ := s.CreateSubject(ctx, "douleur", "fr", "Q81938", nil, "")

	added, err := s.AddSubjectAliases(ctx, bread.ID, models.AliasSourceWikidata,
		map[string]string{"fr": "pain"})
	if err != nil || added != 1 {
		t.Fatalf("AddSubjectAliases(bread) = %d, %v", added, err)
	}

	added, err = s.AddSubjectAliases(ctx, suffering.ID, models.AliasSourceWikidata,
		map[string]string{"en": "pain"})
	if err != nil {
		t.Fatalf("AddSubjectAliases(suffering): %v", err)
	}
	if added != 1 {
		t.Fatalf("stored %d spellings, want the English \"pain\" to survive the French one", added)
	}

	french, err := s.FindSubjectByAlias(ctx, subjects.MatchKey("pain"), "fr")
	if err != nil {
		t.Fatalf("FindSubjectByAlias(fr): %v", err)
	}
	if french.ID != bread.ID {
		t.Errorf("French \"pain\" resolved to %q, want the bread one", french.Label)
	}

	english, err := s.FindSubjectByAlias(ctx, subjects.MatchKey("pain"), "en")
	if err != nil {
		t.Fatalf("FindSubjectByAlias(en): %v", err)
	}
	if english.ID != suffering.ID {
		t.Errorf("English \"pain\" resolved to %q, want the suffering one", english.Label)
	}
}

// TestMergeKeepsTheLabelAsAnAlias: a curator's decision has to be permanent.
// Without this the next doléance using the merged spelling recreates the row
// they removed, and they are asked the same question for ever.
func TestMergeKeepsTheLabelAsAnAlias(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	housing, _ := s.CreateSubject(ctx, "logement", "fr", "", nil, "")
	habitat, _ := s.CreateSubject(ctx, "habitat", "fr", "", nil, "")

	// The doomed subject had learned a spelling of its own, which must not be
	// lost with it.
	if _, err := s.AddSubjectAliases(ctx, habitat.ID, models.AliasSourceWikidata,
		map[string]string{"de": "Wohnraum"}); err != nil {
		t.Fatalf("AddSubjectAliases: %v", err)
	}

	if err := s.MergeSubjects(ctx, habitat.ID, housing.ID); err != nil {
		t.Fatalf("MergeSubjects: %v", err)
	}

	for spelling, language := range map[string]string{"habitat": "fr", "Wohnraum": "de"} {
		found, err := s.FindSubjectByAlias(ctx, subjects.MatchKey(spelling), language)
		if err != nil {
			t.Fatalf("FindSubjectByAlias(%q): %v", spelling, err)
		}
		if found.ID != housing.ID {
			t.Errorf("%q resolved to %q, want logement", spelling, found.Label)
		}
	}
}

// TestDanglingAliasIsNotAWormhole: an alias whose subject is gone must not
// keep answering for it, or one word silently resolves to nothing for ever.
func TestDanglingAliasIsNotAWormhole(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	subject, _ := s.CreateSubject(ctx, "retraite", "fr", "Q946865", nil, "")
	if _, err := s.AddSubjectAliases(ctx, subject.ID, models.AliasSourceWikidata,
		map[string]string{"en": "pension"}); err != nil {
		t.Fatalf("AddSubjectAliases: %v", err)
	}

	if err := s.DB().Delete(&models.Subject{}, "id = ?", subject.ID).Error; err != nil {
		t.Fatalf("delete subject: %v", err)
	}

	_, err := s.FindSubjectByAlias(ctx, subjects.MatchKey("pension"), "en")
	if !errors.Is(err, ErrSubjectNotFound) {
		t.Errorf("err = %v, want ErrSubjectNotFound", err)
	}

	// And the dead row is gone, so the word is free again.
	aliases, err := s.ListSubjectAliases(ctx, subject.ID)
	if err != nil {
		t.Fatalf("ListSubjectAliases: %v", err)
	}
	if len(aliases) != 0 {
		t.Errorf("%d dangling aliases survived", len(aliases))
	}
}

// TestAMergeDecisionOutlivesAnUnknownLanguage: classification reports a
// language it could not name often enough that a curator's ruling must not
// depend on having one. Without the fallback, the pair is re-proposed on the
// next doléance and the same person answers the same question again.
func TestAMergeDecisionOutlivesAnUnknownLanguage(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	housing, _ := s.CreateSubject(ctx, "logement", "fr", "", nil, "")
	habitat, _ := s.CreateSubject(ctx, "habitat", "fr", "", nil, "")

	if err := s.MergeSubjects(ctx, habitat.ID, housing.ID); err != nil {
		t.Fatalf("MergeSubjects: %v", err)
	}

	// The classifier could not name the language this time.
	found, err := s.FindSubjectByAlias(ctx, subjects.MatchKey("habitat"), "")
	if err != nil {
		t.Fatalf("FindSubjectByAlias: %v", err)
	}
	if found.ID != housing.ID {
		t.Errorf("resolved to %q, want the curator's decision to still hold", found.Label)
	}

	// A Wikidata alias is not granted the same latitude: it was generated by a
	// machine for a language nobody may ever write in.
	if _, err := s.AddSubjectAliases(ctx, housing.ID, models.AliasSourceWikidata,
		map[string]string{"ca": "salut"}); err != nil {
		t.Fatalf("AddSubjectAliases: %v", err)
	}
	if _, err := s.FindSubjectByAlias(ctx, subjects.MatchKey("salut"), ""); !errors.Is(err, ErrSubjectNotFound) {
		t.Errorf("err = %v, want a pre-filled label to stay out of an unnamed language", err)
	}
	// And a French doléance saying "salut" is not about housing.
	if _, err := s.FindSubjectByAlias(ctx, subjects.MatchKey("salut"), "fr"); !errors.Is(err, ErrSubjectNotFound) {
		t.Errorf("err = %v, want the Catalan label to be invisible to French", err)
	}
}

// TestFindSubjectByQIDActuallyFinds is a regression test for a whole layer
// that was silently dead.
//
// The lookup was written as `WHERE qid = ?` while GORM had named the column
// `q_id` — its list of known initialisms contains "ID" but not "QID", so the
// field name splits into q + id. Every call failed with "no such column",
// which the caller treated the same way it treats "Wikidata does not know this
// word": carry on to the next layer. So the layer never matched anything, and
// nothing anywhere said so. A live run of the corpus produced twenty-one of
// these errors behind logs that otherwise looked healthy.
//
// Nothing in the previous tests called this function. They created subjects
// carrying QIDs and asserted on the rows, which exercises GORM's naming in
// both directions and never touches the hand-written column name.
func TestFindSubjectByQIDActuallyFinds(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	health, err := s.CreateSubject(ctx, "santé", "fr", "Q12147", nil, "")
	if err != nil {
		t.Fatalf("CreateSubject: %v", err)
	}

	found, err := s.FindSubjectByQID(ctx, "Q12147")
	if err != nil {
		t.Fatalf("FindSubjectByQID: %v — the Wikidata layer is not reaching the column", err)
	}
	if found.ID != health.ID {
		t.Errorf("found %q, want santé", found.Label)
	}

	// A QID nothing carries is an ordinary miss, and must be reported as one:
	// the caller tells "not found" from "broken" only by the error type, which
	// is exactly how the column-name bug stayed invisible.
	if _, err := s.FindSubjectByQID(ctx, "Q999999999"); !errors.Is(err, ErrSubjectNotFound) {
		t.Errorf("err = %v, want ErrSubjectNotFound", err)
	}
}

// TestSubjectLabelsInAReadersLanguage is what the QIDs were ultimately for.
//
// The vocabulary is built from whichever spelling arrived first, so a register
// that has taken one German doléance would otherwise show "Gesundheit" to its
// French readers for ever after.
func TestSubjectLabelsInAReadersLanguage(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	health, _ := s.CreateSubject(ctx, "Gesundheit", "de", "Q12147", nil, "")
	housing, _ := s.CreateSubject(ctx, "logement", "fr", "", nil, "")

	if _, err := s.AddSubjectAliases(ctx, health.ID, models.AliasSourceWikidata,
		map[string]string{"fr": "santé", "en": "health"}); err != nil {
		t.Fatalf("AddSubjectAliases: %v", err)
	}

	french, err := s.SubjectLabelsIn(ctx, "fr")
	if err != nil {
		t.Fatalf("SubjectLabelsIn: %v", err)
	}
	if french[health.ID] != "santé" {
		t.Errorf("French readers see %q, want santé", french[health.ID])
	}

	// A subject with no name in that language is simply absent, and the caller
	// falls back to its own label. Being readable matters more than being
	// uniform, so a French reader sees "logement" rather than nothing.
	if _, present := french[housing.ID]; present {
		t.Error("a subject with no French alias should be absent, not blank")
	}

	// And the identity never moves: a filter that renamed itself per reader
	// would break every link between them.
	stored, err := s.FindSubjectByMatchKey(ctx, subjects.MatchKey("Gesundheit"))
	if err != nil {
		t.Fatalf("FindSubjectByMatchKey: %v", err)
	}
	if stored.Slug != health.Slug {
		t.Errorf("slug changed to %q", stored.Slug)
	}

	// An unnamed language asks for nothing rather than everything.
	none, err := s.SubjectLabelsIn(ctx, "")
	if err != nil || len(none) != 0 {
		t.Errorf("SubjectLabelsIn(\"\") = %v, %v", none, err)
	}
}

// TestHandWrittenColumnNamesExist guards a mistake this package has now made
// twice.
//
// GORM derives column names from field names, and its list of known
// initialisms contains "ID" but not "QID" — so `QID` becomes `q_id` and
// `QIDConfirmedAt` becomes `q_id_confirmed_at`. Written as `qid` in a Where
// clause, a query fails with "no such column", and the callers here treat a
// failed lookup as an ordinary miss. That is how the entire Wikidata layer ran
// dead and silent through a full corpus run while the logs looked healthy.
//
// The compiler cannot see a column name in a string, and neither can a test
// that only checks behaviour through GORM's own field mapping. So this asserts
// the strings themselves against the migrated schema.
func TestHandWrittenColumnNamesExist(t *testing.T) {
	s := newStore(t)

	// Every column this package names in raw SQL rather than through a struct.
	columns := map[string][]string{
		"subjects": {
			"q_id", "q_id_confirmed_at", "match_key", "fold_key",
			"embedding", "embedding_model", "label", "slug", "language",
		},
		"subject_entities": {"subject_id", "q_id", "resolved_at", "accepted", "confidence"},
		"subject_aliases":  {"subject_id", "match_key", "language", "source"},
		"subject_merges":   {"subject_id", "into_id", "resolved_at", "merged"},
		"messages":         {"content_hash", "duplicate_of", "drop_reason", "language"},
	}

	for table, names := range columns {
		var actual []string
		if err := s.DB().Raw("SELECT name FROM pragma_table_info(?)", table).Scan(&actual).Error; err != nil {
			t.Fatalf("read %s: %v", table, err)
		}
		present := make(map[string]bool, len(actual))
		for _, name := range actual {
			present[name] = true
		}
		for _, name := range names {
			if !present[name] {
				t.Errorf("%s has no column %q — a query naming it fails as a silent miss, not an error.\n  columns: %v",
					table, name, actual)
			}
		}
	}
}

// TestTheHierarchyCannotLoop. Every reader of the hierarchy walks it, so a
// cycle turns a filter into a hang — and a curator asserting "A is broader
// than B" has no way of seeing that B is already three links above A.
func TestTheHierarchyCannotLoop(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	transport, _ := s.CreateSubject(ctx, "transport", "fr", "", nil, "")
	public, _ := s.CreateSubject(ctx, "transport en commun", "fr", "", nil, "")
	bus, _ := s.CreateSubject(ctx, "bus", "fr", "", nil, "")

	for _, link := range [][2]string{{public.ID, transport.ID}, {bus.ID, public.ID}} {
		if err := s.LinkSubjects(ctx, link[0], link[1], models.RelationSourceCurator); err != nil {
			t.Fatalf("LinkSubjects: %v", err)
		}
	}

	// Closing the loop at any distance is refused, not just the direct one.
	for _, loop := range [][2]string{
		{transport.ID, bus.ID},    // three links away
		{transport.ID, public.ID}, // one link away
		{bus.ID, bus.ID},          // itself
	} {
		if err := s.LinkSubjects(ctx, loop[0], loop[1], models.RelationSourceCurator); !errors.Is(err, ErrSubjectCycle) {
			t.Errorf("err = %v for %v, want ErrSubjectCycle", err, loop)
		}
	}

	// And the real hierarchy still stands.
	kin, err := s.SubjectRelations(ctx, public.ID)
	if err != nil {
		t.Fatalf("SubjectRelations: %v", err)
	}
	if len(kin.Parents) != 1 || kin.Parents[0].ID != transport.ID {
		t.Errorf("parents = %+v, want transport", kin.Parents)
	}
	if len(kin.Children) != 1 || kin.Children[0].ID != bus.ID {
		t.Errorf("children = %+v, want bus", kin.Children)
	}
}

// TestDetachedIsTheWorklist: a subject with no parent and no child is
// unreachable from any broader filter, so only somebody who already knows its
// exact name will find it. That is the list the console is built around.
func TestDetachedIsTheWorklist(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	transport, _ := s.CreateSubject(ctx, "transport", "fr", "", nil, "")
	bus, _ := s.CreateSubject(ctx, "bus", "fr", "", nil, "")
	alone, _ := s.CreateSubject(ctx, "pouvoir d'achat", "fr", "", nil, "")

	if err := s.LinkSubjects(ctx, bus.ID, transport.ID, models.RelationSourceCurator); err != nil {
		t.Fatalf("LinkSubjects: %v", err)
	}

	detached, err := s.ListDetachedSubjects(ctx)
	if err != nil {
		t.Fatalf("ListDetachedSubjects: %v", err)
	}
	if len(detached) != 1 || detached[0].ID != alone.ID {
		labels := make([]string, len(detached))
		for i, d := range detached {
			labels[i] = d.Label
		}
		t.Errorf("detached = %v, want only pouvoir d'achat", labels)
	}
}

// TestReplacingAnEntityRemovesTheNamesItGave is the correction that would
// otherwise bite silently.
//
// A confirmed identity writes up to fifty aliases, and those are live in the
// matching path. A curator fixing a wrong entity while its names stayed behind
// would leave the register recognising incoming doléances by the wrong
// entity's words — the exact failure that made confirmation necessary.
func TestReplacingAnEntityRemovesTheNamesItGave(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	subject, _ := s.CreateSubject(ctx, "isolement", "fr", "", nil, "")
	if _, err := s.AttachSubjectEntity(ctx, subject.ID, "Q132627"); err != nil {
		t.Fatalf("AttachSubjectEntity: %v", err)
	}

	// The wrong entity's names, as learnTranslations would have written them.
	if _, err := s.AddSubjectAliases(ctx, subject.ID, models.AliasSourceWikidata,
		map[string]string{"de": "Isolationshaft", "en": "solitary confinement"}); err != nil {
		t.Fatalf("AddSubjectAliases: %v", err)
	}
	// And one a curator settled, which is a decision rather than a consequence.
	if _, err := s.AddSubjectAliases(ctx, subject.ID, models.AliasSourceMerge,
		map[string]string{"fr": "solitude"}); err != nil {
		t.Fatalf("AddSubjectAliases: %v", err)
	}

	if _, err := s.AttachSubjectEntity(ctx, subject.ID, "Q1778765"); err != nil {
		t.Fatalf("AttachSubjectEntity: %v", err)
	}

	if _, err := s.FindSubjectByAlias(ctx, subjects.MatchKey("Isolationshaft"), "de"); !errors.Is(err, ErrSubjectNotFound) {
		t.Error("the wrong entity's German name still matches incoming doléances")
	}
	// The curator's own alias survives: it was never a consequence of the QID.
	if _, err := s.FindSubjectByAlias(ctx, subjects.MatchKey("solitude"), "fr"); err != nil {
		t.Errorf("a curator's merge alias was deleted with the entity: %v", err)
	}
}

// TestMergingCarriesTheHierarchyOver: a merge is a curator saying two words
// are one subject. It must not also discard the hierarchy work somebody did on
// the word that disappears — which it did, silently, until relations were
// moved here.
func TestMergingCarriesTheHierarchyOver(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	transport, _ := s.CreateSubject(ctx, "transport", "fr", "", nil, "")
	bus, _ := s.CreateSubject(ctx, "bus", "fr", "", nil, "")
	autobus, _ := s.CreateSubject(ctx, "autobus", "fr", "", nil, "")

	// "autobus" is a child of transport and a parent of nothing.
	if err := s.LinkSubjects(ctx, autobus.ID, transport.ID, models.RelationSourceCurator); err != nil {
		t.Fatalf("LinkSubjects: %v", err)
	}

	// A curator decides "autobus" and "bus" are one subject.
	if err := s.MergeSubjects(ctx, autobus.ID, bus.ID); err != nil {
		t.Fatalf("MergeSubjects: %v", err)
	}

	kin, err := s.SubjectRelations(ctx, bus.ID)
	if err != nil {
		t.Fatalf("SubjectRelations: %v", err)
	}
	if len(kin.Parents) != 1 || kin.Parents[0].ID != transport.ID {
		t.Errorf("the survivor's parents = %+v, want transport inherited", kin.Parents)
	}

	// And nothing dangles against the row that is gone.
	var dangling int64
	err = s.DB().Model(&models.SubjectRelation{}).
		Where("child_id = ? OR parent_id = ?", autobus.ID, autobus.ID).
		Count(&dangling).Error
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if dangling != 0 {
		t.Errorf("%d relations still point at the merged-away subject", dangling)
	}
}

// TestMergingTwoRelatedSubjectsDoesNotLoop. If a curator merges a child into
// its own parent, the link has nowhere to go — forcing it would make the
// survivor its own ancestor, and every reader of the hierarchy walks it.
func TestMergingTwoRelatedSubjectsDoesNotLoop(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	transport, _ := s.CreateSubject(ctx, "transport", "fr", "", nil, "")
	bus, _ := s.CreateSubject(ctx, "bus", "fr", "", nil, "")
	if err := s.LinkSubjects(ctx, bus.ID, transport.ID, models.RelationSourceCurator); err != nil {
		t.Fatalf("LinkSubjects: %v", err)
	}

	if err := s.MergeSubjects(ctx, bus.ID, transport.ID); err != nil {
		t.Fatalf("MergeSubjects: %v", err)
	}

	kin, err := s.SubjectRelations(ctx, transport.ID)
	if err != nil {
		t.Fatalf("SubjectRelations: %v", err)
	}
	if len(kin.Parents) != 0 || len(kin.Children) != 0 {
		t.Errorf("the survivor became its own relative: %+v", kin)
	}

	detached, err := s.ListDetachedSubjects(ctx)
	if err != nil {
		t.Fatalf("ListDetachedSubjects: %v", err)
	}
	if len(detached) != 1 || detached[0].ID != transport.ID {
		t.Errorf("detached = %+v, want the survivor alone", detached)
	}
}
