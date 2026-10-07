package store

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"gorm.io/gorm"

	"github.com/CoderSyndicate/doleances/internal/models"
	"github.com/CoderSyndicate/doleances/internal/subjects"
)

// ErrSubjectNotFound is returned when no subject matches.
var ErrSubjectNotFound = errors.New("subject not found")

// FindSubjectByMatchKey is deduplication layer one: an indexed lookup on the
// tolerant form of the label.
func (s *Store) FindSubjectByMatchKey(ctx context.Context, key string) (models.Subject, error) {
	var subject models.Subject
	err := s.db.WithContext(ctx).Where("match_key = ?", key).First(&subject).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return subject, ErrSubjectNotFound
	}
	return subject, err
}

// FindSubjectByFoldKey is layer two: the same lookup with a trailing plural
// marker removed.
//
// The fold key is not unique — "transport" and "transports" share one on
// purpose — so this takes the oldest match, which is the row that was there
// first and the one other messages are already attached to.
func (s *Store) FindSubjectByFoldKey(ctx context.Context, key string) (models.Subject, error) {
	var subject models.Subject
	err := s.db.WithContext(ctx).
		Where("fold_key = ?", key).
		Order("created_at asc").
		First(&subject).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return subject, ErrSubjectNotFound
	}
	return subject, err
}

// FindSubjectByQID is deduplication layer three: a language-neutral identity.
//
// Used in one direction only. Two subjects that resolved to the same Wikidata
// entity are the same subject whatever their spelling or language; two that
// did not may still be, which is what the embedding layer is for.
func (s *Store) FindSubjectByQID(ctx context.Context, qid string) (models.Subject, error) {
	if qid == "" {
		return models.Subject{}, ErrSubjectNotFound
	}

	// The column is `q_id`, not `qid`. GORM derives it from the field name and
	// its list of known initialisms has "ID" but not "QID", so `QID` splits
	// into q + id. Written wrong this failed as "no such column" on every
	// lookup, which the layer treated as "no Wikidata identity this time" —
	// so the whole layer was silently dead while the logs looked busy.
	var subject models.Subject
	err := s.db.WithContext(ctx).
		Where("q_id = ?", qid).
		Order("created_at asc"). // the row that was there first
		First(&subject).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return subject, ErrSubjectNotFound
	}
	return subject, err
}

// SubjectVector is a subject and its embedding, for the similarity layer.
type SubjectVector struct {
	Subject models.Subject
	Vector  []float32
}

// ListSubjectVectors returns every subject carrying a vector from the given
// model.
//
// Vectors from another model are left out rather than compared: a different
// model is a different space, and cosine across two of them produces confident
// nonsense — which here means silently merging unrelated subjects.
func (s *Store) ListSubjectVectors(ctx context.Context, model string) ([]SubjectVector, error) {
	var rows []models.Subject
	err := s.db.WithContext(ctx).
		Where("embedding_model = ? AND embedding IS NOT NULL", model).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}

	vectors := make([]SubjectVector, 0, len(rows))
	for _, row := range rows {
		vector := DecodeVector(row.Embedding)
		if len(vector) == 0 {
			continue
		}
		vectors = append(vectors, SubjectVector{Subject: row, Vector: vector})
	}
	return vectors, nil
}

// CreateSubject stores a new subject with everything the three layers need.
//
// A unique match key means two workers racing on the same new label produce
// one row and one error rather than two rows; the caller re-reads on conflict.
func (s *Store) CreateSubject(ctx context.Context, label, language, qid string,
	vector []float32, embeddingModel string) (models.Subject, error) {

	if err := subjects.Validate(label); err != nil {
		return models.Subject{}, err
	}

	key := subjects.MatchKey(label)
	subject := models.Subject{
		Slug:     uniqueSlug(ctx, s, subjects.Slug(label)),
		Label:    label,
		Language: language,
		MatchKey: key,
		FoldKey:  subjects.FoldPlural(key),
		QID:      qid,
	}
	if len(vector) > 0 {
		subject.Embedding = EncodeVector(vector)
		subject.EmbeddingModel = embeddingModel
	}

	if err := s.db.WithContext(ctx).Create(&subject).Error; err != nil {
		// Lost the race: somebody else created it a moment ago, and their row
		// is as good as ours would have been.
		if existing, findErr := s.FindSubjectByMatchKey(ctx, key); findErr == nil {
			return existing, nil
		}
		return models.Subject{}, err
	}
	return subject, nil
}

// uniqueSlug keeps the URL identity unique even when two different labels
// normalise to the same slug — "Santé !" and "santé" have distinct match keys
// only if the punctuation survived, and it does not.
func uniqueSlug(ctx context.Context, s *Store, base string) string {
	if base == "" {
		base = "sujet"
	}

	candidate := base
	for suffix := 2; suffix < 100; suffix++ {
		var count int64
		err := s.db.WithContext(ctx).Model(&models.Subject{}).
			Where("slug = ?", candidate).Count(&count).Error
		if err != nil || count == 0 {
			return candidate
		}
		candidate = fmt.Sprintf("%s-%d", base, suffix)
	}
	return fmt.Sprintf("%s-%d", base, time.Now().UnixNano())
}

// AttachSubjects replaces a message's subjects with the ones given.
func (s *Store) AttachSubjects(ctx context.Context, messageID string, ids []string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("DELETE FROM message_subjects WHERE message_id = ?", messageID).Error; err != nil {
			return err
		}
		for _, id := range ids {
			err := tx.Exec("INSERT INTO message_subjects (message_id, subject_id) VALUES (?, ?)",
				messageID, id).Error
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// ListSubjects returns the vocabulary, alphabetically.
func (s *Store) ListSubjects(ctx context.Context) ([]models.Subject, error) {
	var rows []models.Subject
	err := s.db.WithContext(ctx).Order("label asc").Find(&rows).Error
	return rows, err
}

// ---------------------------------------------------------------------------
// Vectors on disk
// ---------------------------------------------------------------------------

// EncodeVector packs an embedding into bytes.
//
// Raw little-endian float32s rather than JSON: a 1024-dimension vector is 4 KiB
// this way and about three times that as text, and this is a column that grows
// with the vocabulary.
func EncodeVector(vector []float32) []byte {
	out := make([]byte, 4*len(vector))
	for i, value := range vector {
		binary.LittleEndian.PutUint32(out[4*i:], math.Float32bits(value))
	}
	return out
}

// DecodeVector unpacks one. A truncated or corrupt blob yields nothing rather
// than an error: a subject with no usable vector takes no part in similarity
// matching, which is a degradation, not a failure.
func DecodeVector(raw []byte) []float32 {
	if len(raw) == 0 || len(raw)%4 != 0 {
		return nil
	}

	vector := make([]float32, len(raw)/4)
	for i := range vector {
		vector[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[4*i:]))
	}
	return vector
}

// ---------------------------------------------------------------------------
// Merge suggestions
// ---------------------------------------------------------------------------

// MergeProposal is one question about two subjects, with everything each
// signal had to say about them.
//
// A struct rather than six arguments because the fields are not
// interchangeable and three of them are strings: transposing SubjectID and
// IntoID at a call site would be invisible and would reverse which subject
// survives the merge.
type MergeProposal struct {
	// SubjectID is the newcomer, IntoID the subject it may be a spelling of.
	SubjectID string
	IntoID    string

	// MessageID is the doléance being classified when the question arose — the
	// context a curator reads to answer it.
	MessageID string

	// Source is which signal raised it; Evidence and Similarity are what each
	// signal said, either of which may be absent.
	Source     string
	Evidence   string
	Similarity float64

	CrossLanguage bool
}

// SuggestSubjectMerge records that two subjects may be one thing.
//
// The pair is stored in a stable order, so "habitat resembles logement" and
// "logement resembles habitat" are one question rather than two. A pair already
// asked about — answered or not — is left alone: a curator who dismissed a
// suggestion must not be asked again on the next submission.
func (s *Store) SuggestSubjectMerge(ctx context.Context, proposal MergeProposal) error {
	if proposal.SubjectID == proposal.IntoID ||
		proposal.SubjectID == "" || proposal.IntoID == "" {
		return nil
	}

	first, second := proposal.SubjectID, proposal.IntoID
	if first > second {
		first, second = second, first
	}

	var count int64
	err := s.db.WithContext(ctx).Model(&models.SubjectMerge{}).
		Where("subject_id = ? AND into_id = ?", first, second).
		Count(&count).Error
	if err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	return s.db.WithContext(ctx).Create(&models.SubjectMerge{
		SubjectID:     first,
		IntoID:        second,
		MessageID:     proposal.MessageID,
		Source:        proposal.Source,
		Evidence:      proposal.Evidence,
		Similarity:    proposal.Similarity,
		CrossLanguage: proposal.CrossLanguage,
	}).Error
}

// SubjectMergeSuggestion is a pending question with everything a curator needs
// to answer it: both labels, both signals, and the doléance that raised it.
type SubjectMergeSuggestion struct {
	ID            string
	Source        string
	Evidence      string
	Similarity    float64
	CrossLanguage bool
	Subject       models.Subject
	Into          models.Subject

	// Message is the doléance being classified when the question arose. It may
	// be absent — a message deleted by its author since, or a suggestion from
	// before this was recorded — and the question is still answerable without
	// it, only harder.
	Message *models.Message
}

// ListSubjectMergeSuggestions returns the unanswered questions.
//
// Wikidata matches come first whatever their similarity: a shared entity is
// better evidence than a close vector, and those are the ones a curator can
// settle at a glance.
func (s *Store) ListSubjectMergeSuggestions(ctx context.Context) ([]SubjectMergeSuggestion, error) {
	var rows []models.SubjectMerge
	err := s.db.WithContext(ctx).
		Where("resolved_at IS NULL").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}

	// Wikidata first, then the closest vectors.
	sort.SliceStable(rows, func(i, j int) bool {
		if (rows[i].Source == models.MergeSourceWikidata) !=
			(rows[j].Source == models.MergeSourceWikidata) {
			return rows[i].Source == models.MergeSourceWikidata
		}
		return rows[i].Similarity > rows[j].Similarity
	})

	out := make([]SubjectMergeSuggestion, 0, len(rows))
	for _, row := range rows {
		var subject, into models.Subject
		if err := s.db.WithContext(ctx).First(&subject, "id = ?", row.SubjectID).Error; err != nil {
			continue // the subject was merged away by an earlier decision
		}
		if err := s.db.WithContext(ctx).First(&into, "id = ?", row.IntoID).Error; err != nil {
			continue
		}

		suggestion := SubjectMergeSuggestion{
			ID:            row.ID,
			Source:        row.Source,
			Evidence:      row.Evidence,
			Similarity:    row.Similarity,
			CrossLanguage: row.CrossLanguage,
			Subject:       subject,
			Into:          into,
		}
		if row.MessageID != "" {
			var message models.Message
			if err := s.db.WithContext(ctx).First(&message, "id = ?", row.MessageID).Error; err == nil {
				suggestion.Message = &message
			}
		}
		out = append(out, suggestion)
	}
	return out, nil
}

// MergeSubjects folds one subject into another and deletes it.
//
// Every message carrying the merged subject is moved to the survivor, so the
// filter keeps working and no doléance loses its classification. The insert
// tolerates a message that already carried both — merging two subjects one
// message shared must not fail on a duplicate key.
//
// The disappearing label survives as an alias of the survivor, and so do its
// own aliases. That is what makes a curator's decision permanent rather than
// momentary: without it the next doléance using that spelling would create the
// row they just removed, and they would be asked the same question again.
func (s *Store) MergeSubjects(ctx context.Context, fromID, intoID string) error {
	if fromID == intoID {
		return errors.New("a subject cannot be merged into itself")
	}

	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var merged models.Subject
		if err := tx.First(&merged, "id = ?", fromID).Error; err != nil {
			return err
		}

		var messageIDs []string
		err := tx.Raw(`SELECT message_id FROM message_subjects WHERE subject_id = ?`,
			fromID).Scan(&messageIDs).Error
		if err != nil {
			return err
		}

		for _, messageID := range messageIDs {
			var existing int64
			err := tx.Raw(`SELECT count(*) FROM message_subjects
				WHERE message_id = ? AND subject_id = ?`, messageID, intoID).
				Scan(&existing).Error
			if err != nil {
				return err
			}
			if existing > 0 {
				continue
			}
			err = tx.Exec(`INSERT INTO message_subjects (message_id, subject_id) VALUES (?, ?)`,
				messageID, intoID).Error
			if err != nil {
				return err
			}
		}

		if err := tx.Exec(`DELETE FROM message_subjects WHERE subject_id = ?`, fromID).Error; err != nil {
			return err
		}

		// The relations follow too, or a merge silently discards the
		// hierarchy work somebody did on the subject that disappears.
		//
		// Re-pointed one at a time rather than with an UPDATE, because three
		// things can go wrong and each is silent: the survivor would become
		// its own parent if the two were related to each other, the same link
		// may already exist on the survivor, and a link that was fine on the
		// merged subject can close a cycle once it moves.
		if err := s.moveRelations(tx, fromID, intoID); err != nil {
			return err
		}

		// The aliases follow the messages: every spelling that used to reach
		// the merged subject now reaches the survivor.
		err = tx.Model(&models.SubjectAlias{}).Where("subject_id = ?", fromID).
			Update("subject_id", intoID).Error
		if err != nil {
			return err
		}

		// And the label itself becomes one, which is the curator's answer
		// written down in the only form the resolver reads.
		alias := models.SubjectAlias{
			SubjectID: intoID,
			Label:     merged.Label,
			Language:  merged.Language,
			MatchKey:  merged.MatchKey,
			FoldKey:   merged.FoldKey,
			Source:    models.AliasSourceMerge,
		}
		if err := tx.Create(&alias).Error; err != nil {
			// The word is already a spelling of something. Better than
			// failing the merge over it: the messages have moved, which is
			// what the curator asked for.
			log.Warn().Err(err).Str("label", merged.Label).
				Msg("subjects: the merged label could not be kept as an alias")
		}

		if err := tx.Delete(&models.Subject{}, "id = ?", fromID).Error; err != nil {
			return err
		}

		// Any question involving the subject that is gone is answered by its
		// disappearance.
		now := time.Now()
		return tx.Model(&models.SubjectMerge{}).
			Where("subject_id = ? OR into_id = ?", fromID, fromID).
			Updates(map[string]any{"resolved_at": now, "merged": true}).Error
	})
}

// moveRelations re-points a disappearing subject's links onto the survivor.
//
// Anything that cannot move is dropped rather than forced. A merge is a
// curator saying two words are one subject; it is not a mandate to invent
// hierarchy that nobody asserted, and a relation that would make the survivor
// its own ancestor is exactly such an invention.
func (s *Store) moveRelations(tx *gorm.DB, fromID, intoID string) error {
	var rows []models.SubjectRelation
	err := tx.Where("child_id = ? OR parent_id = ?", fromID, fromID).Find(&rows).Error
	if err != nil {
		return err
	}

	for _, row := range rows {
		child, parent := row.ChildID, row.ParentID
		if child == fromID {
			child = intoID
		}
		if parent == fromID {
			parent = intoID
		}

		// The two were related to each other, so the link has nowhere to go.
		if child == parent {
			continue
		}

		var clash int64
		err := tx.Model(&models.SubjectRelation{}).
			Where("child_id = ? AND parent_id = ?", child, parent).
			Count(&clash).Error
		if err != nil {
			return err
		}
		if clash > 0 {
			continue
		}

		// Would it close a loop now that one end has moved? The survivor may
		// already sit on the other side of this link.
		var reverse int64
		err = tx.Model(&models.SubjectRelation{}).
			Where("child_id = ? AND parent_id = ?", parent, child).
			Count(&reverse).Error
		if err != nil {
			return err
		}
		if reverse > 0 {
			continue
		}

		err = tx.Model(&models.SubjectRelation{}).Where("id = ?", row.ID).
			Updates(map[string]any{"child_id": child, "parent_id": parent}).Error
		if err != nil {
			return err
		}
	}

	// Whatever could not move goes, rather than dangling against a row that
	// no longer exists.
	return tx.Where("child_id = ? OR parent_id = ?", fromID, fromID).
		Delete(&models.SubjectRelation{}).Error
}

// DismissSubjectMerge records that two subjects are not the same thing.
//
// The row stays, answered: deleting it would mean the same pair is proposed
// again on the next submission and a curator answers the same question for
// ever.
func (s *Store) DismissSubjectMerge(ctx context.Context, id string) error {
	now := time.Now()
	return s.db.WithContext(ctx).Model(&models.SubjectMerge{}).
		Where("id = ?", id).
		Updates(map[string]any{"resolved_at": now, "merged": false}).Error
}

// RenameSubject changes the label a reader sees.
//
// The slug is left alone — it is the stable identity and appears in query
// strings — and so is the match key, so renaming "santé" to "Santé publique"
// does not make the next "santé" a second row. A curator who wants that merges
// instead.
func (s *Store) RenameSubject(ctx context.Context, id, label string) error {
	if err := subjects.Validate(label); err != nil {
		return err
	}
	return s.db.WithContext(ctx).Model(&models.Subject{}).
		Where("id = ?", id).
		Updates(map[string]any{"label": label, "updated_at": time.Now()}).Error
}

// ---------------------------------------------------------------------------
// Aliases — the other spellings of a subject
// ---------------------------------------------------------------------------

// FindSubjectByAlias is the alias layer: the same indexed lookup as layer one,
// against the spellings a subject is also known by.
//
// A hit here is as good as a hit on the subject's own key. That is the point of
// pre-filling aliases from Wikidata: the first German doléance about health
// costs one index read rather than a Wikidata call, an embedding call and a
// curator's attention.
//
// # Two kinds of alias, consulted differently
//
// A Wikidata alias answers only for a doléance written in its own language.
// With fifty languages of pre-filled labels the same string is routinely two
// different words — Catalan "salut" is a real label of Q12147 (health) where
// French "salut" is a greeting, "pain" is bread in French and suffering in
// English — so a language-blind lookup would let a machine-generated name in a
// language nobody here writes in capture a word somebody did write. An unknown
// language therefore matches nothing rather than matching anything.
//
// A merge alias answers whatever the language, because it is not that kind of
// row. It exists because a curator looked at one word, in one doléance, and
// ruled on it; that is the same provenance as a subject, and layer one is
// language-blind for the same reason. Scoping it would quietly undo the
// decision for every doléance whose language the classifier could not name,
// and the curator would be asked again.
//
// So the exact language wins where there is one, and a curator's ruling is the
// fallback rather than the other way round.
func (s *Store) FindSubjectByAlias(ctx context.Context, key, language string) (models.Subject, error) {
	if key == "" {
		return models.Subject{}, ErrSubjectNotFound
	}

	var alias models.SubjectAlias
	err := gorm.ErrRecordNotFound

	if language != "" {
		err = s.db.WithContext(ctx).
			Where("match_key = ? AND language = ?", key, language).
			First(&alias).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return models.Subject{}, err
		}
	}

	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = s.db.WithContext(ctx).
			Where("match_key = ? AND source = ?", key, models.AliasSourceMerge).
			Order("created_at asc"). // the earliest ruling, if a word was ruled on twice
			First(&alias).Error
	}

	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.Subject{}, ErrSubjectNotFound
	}
	if err != nil {
		return models.Subject{}, err
	}

	var subject models.Subject
	err = s.db.WithContext(ctx).First(&subject, "id = ?", alias.SubjectID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// The subject was merged or deleted and the alias outlived it. Cleaning
		// it up here rather than reporting a failure: a dangling alias would
		// otherwise shadow the same word for ever.
		s.db.WithContext(ctx).Delete(&models.SubjectAlias{}, "id = ?", alias.ID) //nolint:errcheck
		return models.Subject{}, ErrSubjectNotFound
	}
	return subject, err
}

// AddSubjectAliases records other spellings of a subject, and reports how many
// were new.
//
// Collisions are skipped rather than failing the batch, and that is the normal
// case rather than an error: Wikidata's label for an entity is very often the
// word the register already has as a subject, or as an alias of one. Nothing is
// overwritten — an existing row was either a curator's decision or an earlier
// entity's claim on that word, and neither should lose to a later import.
//
// Two collisions are checked, and they are checked differently:
//
//   - Against subjects, globally. Layer one runs before this one and is
//     language-blind, so a key that is already a subject can never be reached
//     here whatever language it is filed under — the row would be dead weight.
//   - Against aliases, per language. "pain" may be an English alias of one
//     subject and a French alias of another; only the same word in the same
//     language is a conflict.
func (s *Store) AddSubjectAliases(ctx context.Context, subjectID, source string,
	labels map[string]string) (added int, err error) {

	if subjectID == "" || len(labels) == 0 {
		return 0, nil
	}

	for language, label := range labels {
		key := subjects.MatchKey(label)
		if key == "" {
			continue
		}

		// Never shadow a real subject. One word resolving to two different
		// things depending on which table was consulted first is the failure
		// this whole layer exists to prevent.
		if _, err := s.FindSubjectByMatchKey(ctx, key); err == nil {
			continue
		} else if !errors.Is(err, ErrSubjectNotFound) {
			return added, err
		}

		// Checked against this language only. The merge fallback in
		// FindSubjectByAlias is a read-time rule, not a claim on the word:
		// letting it block the import would stop a subject learning its German
		// name because a curator once ruled on the same string in Polish.
		var clash int64
		err := s.db.WithContext(ctx).Model(&models.SubjectAlias{}).
			Where("match_key = ? AND language = ?", key, language).
			Count(&clash).Error
		if err != nil {
			return added, err
		}
		if clash > 0 {
			continue
		}

		alias := models.SubjectAlias{
			SubjectID: subjectID,
			Label:     label,
			Language:  language,
			MatchKey:  key,
			FoldKey:   subjects.FoldPlural(key),
			Source:    source,
		}
		if err := s.db.WithContext(ctx).Create(&alias).Error; err != nil {
			// Lost a race against another worker importing the same entity.
			// Their row says the same thing ours would have.
			continue
		}
		added++
	}
	return added, nil
}

// ListSubjectAliases returns the spellings a subject is known by.
func (s *Store) ListSubjectAliases(ctx context.Context, subjectID string) ([]models.SubjectAlias, error) {
	var aliases []models.SubjectAlias
	err := s.db.WithContext(ctx).
		Where("subject_id = ?", subjectID).
		Order("language asc").
		Find(&aliases).Error
	return aliases, err
}

// CountMessagesForSubject is how many doléances carry a subject.
//
// It is what turns a rename or a merge from a tidy-up into a decision: a label
// on three hundred doléances is a filter people are using, and a label on one
// is a mistake nobody has noticed yet.
func (s *Store) CountMessagesForSubject(ctx context.Context, subjectID string) (int64, error) {
	var count int64
	err := s.db.WithContext(ctx).
		Raw(`SELECT count(*) FROM message_subjects WHERE subject_id = ?`, subjectID).
		Scan(&count).Error
	return count, err
}

// SubjectLabelsIn returns each subject's name in one language, keyed by
// subject id, for the subjects that have one.
//
// This is what the QIDs were for. The vocabulary is built out of whichever
// spelling happened to arrive first, so a register that has taken one German
// doléance shows "Gesundheit" to its French readers for ever after. The
// aliases already hold Wikidata's name for the entity in every language the
// register speaks; this is the lookup that puts them on the page.
//
// One query rather than one per subject: the filter list is rendered on every
// page that offers it.
//
// A subject with no name in that language is absent from the map, and the
// caller falls back to its own label. Showing a word from the wrong language
// is much better than showing nothing — the point is to be readable, not to be
// uniform.
func (s *Store) SubjectLabelsIn(ctx context.Context, language string) (map[string]string, error) {
	if language == "" {
		return nil, nil
	}

	var aliases []models.SubjectAlias
	err := s.db.WithContext(ctx).
		Where("language = ?", language).
		Find(&aliases).Error
	if err != nil {
		return nil, err
	}

	labels := make(map[string]string, len(aliases))
	for _, alias := range aliases {
		// A subject may carry several names in one language — a curator's
		// merge leaves one, and Wikidata supplies another. First wins, which
		// is the earliest written and so the one readers have already seen.
		if _, taken := labels[alias.SubjectID]; !taken {
			labels[alias.SubjectID] = alias.Label
		}
	}
	return labels, nil
}

// ---------------------------------------------------------------------------
// Entity proposals — no QID without a person
// ---------------------------------------------------------------------------

// ProposeSubjectEntity records a Wikidata entity for a curator to confirm.
//
// Nothing is attributed here. The subject's QID stays empty, and every layer
// that reads it carries on as if no entity existed — which is the point: an
// unconfirmed identity decides what a subject is called in fifty languages and
// which subjects are merged into it, and it has been wrong often enough that
// the pipeline must not act on it alone.
//
// A subject already carrying an open proposal, a confirmed entity, or a
// rejected proposal for this same entity is left alone. Asking twice about one
// word is how a queue stops being read.
func (s *Store) ProposeSubjectEntity(ctx context.Context, proposal models.SubjectEntity) error {
	if proposal.SubjectID == "" || proposal.QID == "" {
		return nil
	}

	var subject models.Subject
	if err := s.db.WithContext(ctx).First(&subject, "id = ?", proposal.SubjectID).Error; err != nil {
		return err
	}
	if subject.QIDConfirmedAt != nil {
		return nil // already settled by a person
	}

	var existing int64
	err := s.db.WithContext(ctx).Model(&models.SubjectEntity{}).
		Where("subject_id = ? AND (resolved_at IS NULL OR q_id = ?)",
			proposal.SubjectID, proposal.QID).
		Count(&existing).Error
	if err != nil {
		return err
	}
	if existing > 0 {
		return nil
	}

	return s.db.WithContext(ctx).Create(&proposal).Error
}

// SubjectEntityProposal is a pending proposal with its subject resolved,
// because a curator needs the word rather than an identifier.
type SubjectEntityProposal struct {
	models.SubjectEntity

	Subject models.Subject

	// Message is the doléance whose classification raised it. It may be
	// absent — deleted by its author since — and the question is still
	// answerable without it, only harder.
	Message *models.Message
}

// ListSubjectEntityProposals returns the unanswered ones, least confident
// first.
//
// Least confident first because those are where a curator's attention is worth
// most: a high-confidence pick is usually a glance, and a low-confidence one is
// the question that actually needed asking.
func (s *Store) ListSubjectEntityProposals(ctx context.Context) ([]SubjectEntityProposal, error) {
	var rows []models.SubjectEntity
	err := s.db.WithContext(ctx).
		Where("resolved_at IS NULL").
		Order("confidence asc").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}

	out := make([]SubjectEntityProposal, 0, len(rows))
	for _, row := range rows {
		proposal := SubjectEntityProposal{SubjectEntity: row}
		if err := s.db.WithContext(ctx).First(&proposal.Subject, "id = ?", row.SubjectID).Error; err != nil {
			continue // the subject was merged away by an earlier decision
		}
		if row.MessageID != "" {
			var message models.Message
			if err := s.db.WithContext(ctx).First(&message, "id = ?", row.MessageID).Error; err == nil {
				proposal.Message = &message
			}
		}
		out = append(out, proposal)
	}
	return out, nil
}

// ConfirmSubjectEntity attributes the entity, which is the only way a QID is
// ever written.
func (s *Store) ConfirmSubjectEntity(ctx context.Context, id string) (models.Subject, error) {
	var subject models.Subject

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var proposal models.SubjectEntity
		if err := tx.First(&proposal, "id = ? AND resolved_at IS NULL", id).Error; err != nil {
			return err
		}
		if err := tx.First(&subject, "id = ?", proposal.SubjectID).Error; err != nil {
			return err
		}

		now := time.Now()
		err := tx.Model(&models.Subject{}).Where("id = ?", proposal.SubjectID).
			Updates(map[string]any{
				"q_id":              proposal.QID,
				"q_id_confirmed_at": now,
				"updated_at":        now,
			}).Error
		if err != nil {
			return err
		}

		subject.QID = proposal.QID
		subject.QIDConfirmedAt = &now

		return tx.Model(&models.SubjectEntity{}).Where("id = ?", id).
			Updates(map[string]any{"resolved_at": now, "accepted": true}).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return subject, ErrSubjectNotFound
	}
	return subject, err
}

// RejectSubjectEntity records that the entity is not this subject.
//
// The row stays, answered: deleting it would mean the same entity is proposed
// again on the next doléance carrying the subject.
func (s *Store) RejectSubjectEntity(ctx context.Context, id string) error {
	now := time.Now()
	result := s.db.WithContext(ctx).Model(&models.SubjectEntity{}).
		Where("id = ? AND resolved_at IS NULL", id).
		Updates(map[string]any{"resolved_at": now, "accepted": false})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrSubjectNotFound
	}
	return nil
}

// PurgeUnconfirmedEntities removes identities and their consequences from any
// database written before a person had to confirm them.
//
// Idempotent, and a no-op on a database that never ran that pipeline. It is
// here rather than in a migration script because the rows it removes are
// actively wrong in the matching path — "solitary confinement" standing as the
// English name of rural isolation — and leaving them until somebody runs a
// tool would mean shipping with them.
func (s *Store) PurgeUnconfirmedEntities(ctx context.Context) (int64, error) {
	var subjects []models.Subject
	err := s.db.WithContext(ctx).
		Where("q_id <> '' AND q_id_confirmed_at IS NULL").
		Find(&subjects).Error
	if err != nil || len(subjects) == 0 {
		return 0, err
	}

	ids := make([]string, 0, len(subjects))
	for _, subject := range subjects {
		ids = append(ids, subject.ID)
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// The aliases first: they are the part that is wrong in the matching
		// path, and a curator's own merge aliases are left alone.
		err := tx.Where("subject_id IN ? AND source = ?", ids, models.AliasSourceWikidata).
			Delete(&models.SubjectAlias{}).Error
		if err != nil {
			return err
		}
		return tx.Model(&models.Subject{}).Where("id IN ?", ids).
			Update("q_id", "").Error
	})
	return int64(len(subjects)), err
}

// ---------------------------------------------------------------------------
// The hierarchy
// ---------------------------------------------------------------------------

// ErrSubjectCycle means the link would make a subject its own ancestor.
var ErrSubjectCycle = errors.New("that would make the subject its own ancestor")

// LinkSubjects records that one subject is broader than another.
//
// Refused if it would close a cycle. A cycle is not merely untidy: every reader
// of the hierarchy walks it, and one that loops turns a filter into a hang.
// Checked against the existing graph rather than assumed, because a curator
// asserting "A is a parent of B" has no way of seeing that B is already an
// ancestor of A three links away.
func (s *Store) LinkSubjects(ctx context.Context, childID, parentID, source string) error {
	if childID == "" || parentID == "" {
		return nil
	}
	if childID == parentID {
		return ErrSubjectCycle
	}

	// Would the parent become its own descendant? It would if the proposed
	// parent already sits somewhere below the child.
	descendants, err := s.descendantsOf(ctx, childID)
	if err != nil {
		return err
	}
	if descendants[parentID] {
		return ErrSubjectCycle
	}

	var existing int64
	err = s.db.WithContext(ctx).Model(&models.SubjectRelation{}).
		Where("child_id = ? AND parent_id = ?", childID, parentID).
		Count(&existing).Error
	if err != nil || existing > 0 {
		return err
	}

	return s.db.WithContext(ctx).Create(&models.SubjectRelation{
		ChildID:  childID,
		ParentID: parentID,
		Source:   source,
	}).Error
}

// UnlinkSubjects removes a relation.
func (s *Store) UnlinkSubjects(ctx context.Context, childID, parentID string) error {
	return s.db.WithContext(ctx).
		Where("child_id = ? AND parent_id = ?", childID, parentID).
		Delete(&models.SubjectRelation{}).Error
}

// descendantsOf walks down the graph, bounded.
//
// The bound is not decoration. The graph is a DAG by intent and a cycle by
// accident is exactly what this is called to prevent, so it must terminate even
// when the data it is reading is already broken.
func (s *Store) descendantsOf(ctx context.Context, subjectID string) (map[string]bool, error) {
	seen := map[string]bool{}
	frontier := []string{subjectID}

	for depth := 0; depth < 32 && len(frontier) > 0; depth++ {
		var rows []models.SubjectRelation
		err := s.db.WithContext(ctx).
			Where("parent_id IN ?", frontier).
			Find(&rows).Error
		if err != nil {
			return nil, err
		}

		frontier = frontier[:0]
		for _, row := range rows {
			if seen[row.ChildID] {
				continue
			}
			seen[row.ChildID] = true
			frontier = append(frontier, row.ChildID)
		}
	}
	return seen, nil
}

// SubjectKin is a subject's immediate neighbours in the hierarchy.
type SubjectKin struct {
	Parents  []models.Subject
	Children []models.Subject
}

// SubjectRelations returns a subject's direct parents and children.
//
// Direct only. The console shows one step in each direction because that is
// what a curator is editing; walking further would show them a graph rather
// than a decision.
func (s *Store) SubjectRelations(ctx context.Context, subjectID string) (SubjectKin, error) {
	var kin SubjectKin

	var rows []models.SubjectRelation
	err := s.db.WithContext(ctx).
		Where("child_id = ? OR parent_id = ?", subjectID, subjectID).
		Find(&rows).Error
	if err != nil {
		return kin, err
	}

	for _, row := range rows {
		other := row.ParentID
		if row.ParentID == subjectID {
			other = row.ChildID
		}

		var subject models.Subject
		if err := s.db.WithContext(ctx).First(&subject, "id = ?", other).Error; err != nil {
			continue // linked to a subject that has since been merged away
		}
		if row.ChildID == subjectID {
			kin.Parents = append(kin.Parents, subject)
		} else {
			kin.Children = append(kin.Children, subject)
		}
	}
	return kin, nil
}

// ListDetachedSubjects returns the subjects with no parent and no child.
//
// This is the worklist the console is built around. A subject outside the
// hierarchy is not broken — it classifies doléances perfectly well — but it is
// unreachable from any broader filter, so it is only ever found by somebody who
// already knows its exact name.
func (s *Store) ListDetachedSubjects(ctx context.Context) ([]models.Subject, error) {
	var subjects []models.Subject
	err := s.db.WithContext(ctx).
		Where(`id NOT IN (SELECT child_id FROM subject_relations)
		   AND id NOT IN (SELECT parent_id FROM subject_relations)`).
		Order("label asc").
		Find(&subjects).Error
	return subjects, err
}

// SearchSubjects finds subjects by any of their names.
//
// Aliases are searched as well as labels, because a curator looking for the
// German subject by its French name should find it — that is what the aliases
// are for, and the console is the one place somebody knows both.
func (s *Store) SearchSubjects(ctx context.Context, query string, limit int) ([]models.Subject, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 30
	}

	pattern := "%" + strings.ToLower(query) + "%"
	var found []models.Subject
	err := s.db.WithContext(ctx).
		Where(`lower(label) LIKE ? OR match_key LIKE ?
		   OR id IN (SELECT subject_id FROM subject_aliases WHERE lower(label) LIKE ?)`,
			pattern, "%"+subjects.MatchKey(query)+"%", pattern).
		Order("label asc").
		Limit(limit).
		Find(&found).Error
	return found, err
}

// AttachSubjectEntity gives a subject an identity, chosen by a person.
//
// This is the second path that writes a QID, and it needs no proposal: the
// curator picked the entity from the search themselves, so sending it to a
// queue would mean confirming their own choice.
//
// Replacing an existing identity deletes the names derived from the old one
// first. Those aliases are **live in the matching path**, so a correction that
// left them behind would keep recognising incoming doléances by the wrong
// entity's names — silently, which is precisely the failure that made the
// confirmation rule necessary. Aliases a curator's merge left behind are kept:
// those are decisions about words, not consequences of an entity.
func (s *Store) AttachSubjectEntity(ctx context.Context, subjectID, qid string) (models.Subject, error) {
	var subject models.Subject

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&subject, "id = ?", subjectID).Error; err != nil {
			return err
		}

		err := tx.Where("subject_id = ? AND source = ?", subjectID, models.AliasSourceWikidata).
			Delete(&models.SubjectAlias{}).Error
		if err != nil {
			return err
		}

		now := time.Now()
		err = tx.Model(&models.Subject{}).Where("id = ?", subjectID).
			Updates(map[string]any{
				"q_id":              qid,
				"q_id_confirmed_at": now,
				"updated_at":        now,
			}).Error
		if err != nil {
			return err
		}

		subject.QID = qid
		subject.QIDConfirmedAt = &now

		// Any proposal for this subject is answered by the curator having
		// chosen for themselves.
		return tx.Model(&models.SubjectEntity{}).
			Where("subject_id = ? AND resolved_at IS NULL", subjectID).
			Updates(map[string]any{"resolved_at": now, "accepted": true}).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return subject, ErrSubjectNotFound
	}
	return subject, err
}

// DetachSubjectEntity removes an identity and everything derived from it.
func (s *Store) DetachSubjectEntity(ctx context.Context, subjectID string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Where("subject_id = ? AND source = ?", subjectID, models.AliasSourceWikidata).
			Delete(&models.SubjectAlias{}).Error
		if err != nil {
			return err
		}
		return tx.Model(&models.Subject{}).Where("id = ?", subjectID).
			Updates(map[string]any{
				"q_id":              "",
				"q_id_confirmed_at": nil,
				"updated_at":        time.Now(),
			}).Error
	})
}

// SubjectForEntity returns the subject already carrying a QID, if any.
//
// Used before creating a parent or a child from the entity search: the concept
// may already be in the vocabulary under a name the curator did not think to
// look for, and inventing a second row for it would be the duplicate this
// whole layer exists to prevent.
func (s *Store) SubjectForEntity(ctx context.Context, qid string) (models.Subject, error) {
	return s.FindSubjectByQID(ctx, qid)
}

// CreateSubjectFromEntity adds a subject for an entity a curator chose.
//
// The identity is confirmed on arrival, because the person choosing it is the
// confirmation. The label is the entity's own name in the language given, so
// the vocabulary reads consistently; every other language follows from the
// entity itself once the caller learns its translations.
func (s *Store) CreateSubjectFromEntity(ctx context.Context, label, language, qid string) (models.Subject, error) {
	subject, err := s.CreateSubject(ctx, label, language, qid, nil, "")
	if err != nil {
		return subject, err
	}

	// CreateSubject returns an existing row when the label is already taken,
	// and that row may have no identity — give it this one rather than
	// leaving two half-answers.
	if subject.QIDConfirmedAt == nil {
		return s.AttachSubjectEntity(ctx, subject.ID, qid)
	}
	return subject, nil
}

// AllSubjectRelations returns every broader/narrower link.
//
// The whole graph in one read, because the callers that need it are building a
// tree and would otherwise ask once per subject.
func (s *Store) AllSubjectRelations(ctx context.Context) ([]models.SubjectRelation, error) {
	var rows []models.SubjectRelation
	err := s.db.WithContext(ctx).Find(&rows).Error
	return rows, err
}

// ExpandSubtrees maps each slug to itself plus every subject below it.
//
// This is what makes a broader subject reach the narrower ones. Selecting
// `transport` finds doléances tagged `public transport` and `bus`, because the
// hierarchy is the whole reason those stayed separate subjects: the classifier
// was right to write the precise word, and a reader asking a broad question
// should still reach it.
//
// A slug with nothing below it maps to itself alone, which is every subject
// until somebody curates the hierarchy — so this changes nothing until it has
// something to work with.
//
// Bounded, and each subject visited once: the writer refuses cycles and this
// runs on the register's main query, which is the wrong place to find out that
// the guard was bypassed.
func (s *Store) ExpandSubtrees(ctx context.Context, slugs []string) (map[string][]string, error) {
	if len(slugs) == 0 {
		return nil, nil
	}

	var subjects []models.Subject
	if err := s.db.WithContext(ctx).Find(&subjects).Error; err != nil {
		return nil, err
	}
	relations, err := s.AllSubjectRelations(ctx)
	if err != nil {
		return nil, err
	}

	slugByID := make(map[string]string, len(subjects))
	idBySlug := make(map[string]string, len(subjects))
	for _, subject := range subjects {
		slugByID[subject.ID] = subject.Slug
		idBySlug[subject.Slug] = subject.ID
	}

	children := map[string][]string{}
	for _, relation := range relations {
		children[relation.ParentID] = append(children[relation.ParentID], relation.ChildID)
	}

	const maxDepth = 8
	expanded := make(map[string][]string, len(slugs))

	for _, slug := range slugs {
		root, known := idBySlug[slug]
		if !known {
			// A slug nobody has. Kept as itself so the query returns nothing
			// rather than silently widening to everything.
			expanded[slug] = []string{slug}
			continue
		}

		seen := map[string]bool{root: true}
		out := []string{slug}
		frontier := []string{root}

		for depth := 0; depth < maxDepth && len(frontier) > 0; depth++ {
			var next []string
			for _, id := range frontier {
				for _, child := range children[id] {
					if seen[child] {
						continue
					}
					seen[child] = true
					if childSlug, ok := slugByID[child]; ok {
						out = append(out, childSlug)
					}
					next = append(next, child)
				}
			}
			frontier = next
		}
		expanded[slug] = out
	}
	return expanded, nil
}
