package store

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/rs/zerolog/log"
	"gorm.io/gorm"

	"github.com/CoderSyndicate/doleances/internal/content"
	"github.com/CoderSyndicate/doleances/internal/geo"
	"github.com/CoderSyndicate/doleances/internal/models"
)

// CreateMessage persists a submission.
//
// It is saved as pending: a doléance is accepted the moment it is written down,
// and the assessment that decides whether it is published happens afterwards.
// That order matters — a classifier being slow, or the model being unreachable,
// must never cost somebody the thing they just spent an hour writing.
func (s *Store) CreateMessage(ctx context.Context, message *models.Message) error {
	if message.Status == "" {
		message.Status = models.StatusPending
	}
	return s.db.WithContext(ctx).Create(message).Error
}

// RefreshExcerpts re-derives every stored excerpt that disagrees with its own
// text, and reports how many it moved.
//
// Two things need it, and both are the same thing. A column added to a table
// that already has rows starts empty, so every doléance written before this
// existed has no excerpt; and the length is an operator setting, so changing
// it leaves every row cut to the old one. Deriving on write is what keeps a
// row honest going forward — this is what makes the rows that already exist
// honest too.
//
// It reads in pages and writes only what differs: a register of two hundred
// thousand doléances should cost one pass at startup and no writes at all on
// the starts after that.
func (s *Store) RefreshExcerpts(ctx context.Context, batch int) (int64, error) {
	if batch <= 0 {
		batch = 500
	}

	var moved int64
	var offset int
	for {
		var messages []models.Message
		err := s.db.WithContext(ctx).
			Order("created_at asc").Offset(offset).Limit(batch).
			Find(&messages).Error
		if err != nil {
			return moved, err
		}
		if len(messages) == 0 {
			return moved, nil
		}
		offset += len(messages)

		for _, message := range messages {
			excerpt, truncated := content.Excerpt(message.Text, models.ExcerptRunes)
			if excerpt == message.Excerpt && truncated == message.Truncated {
				continue
			}

			err := s.db.WithContext(ctx).Model(&models.Message{}).
				Where("id = ?", message.ID).
				// UpdateColumns, so re-deriving an excerpt does not touch
				// updated_at: this is housekeeping, not a change to somebody's
				// doléance, and the sweeps that read that column would
				// otherwise see every message as freshly modified.
				UpdateColumns(map[string]any{
					"excerpt": excerpt, "truncated": truncated,
				}).Error
			if err != nil {
				return moved, err
			}
			moved++
		}
	}
}

// LikeMessage records one more "me too" and returns the new count.
//
// An UPDATE that adds one, rather than a read, an increment and a write: two
// readers pressing at the same moment must both be counted, and a
// read-modify-write would lose one of them. The database does the addition.
//
// Only a published doléance can be liked. Anything else is either waiting on a
// decision or was refused, and a count attached to text nobody can see would
// be a signal about a submission the public has no business knowing exists.
func (s *Store) LikeMessage(ctx context.Context, id string) (int, error) {
	result := s.db.WithContext(ctx).Model(&models.Message{}).
		Where("id = ? AND status = ?", id, models.StatusAccepted).
		UpdateColumn("likes", gorm.Expr("likes + 1"))
	if result.Error != nil {
		return 0, result.Error
	}
	if result.RowsAffected == 0 {
		return 0, ErrMessageNotFound
	}

	var message models.Message
	if err := s.db.WithContext(ctx).Select("likes").
		First(&message, "id = ?", id).Error; err != nil {
		return 0, err
	}
	return message.Likes, nil
}

// ReportMessage sends a published doléance back to a human and takes it off
// the register until one has looked.
//
// # What it refuses, and why each refusal is the interesting part
//
// Only an **accepted** doléance can be reported. Anything else is already
// waiting on a decision or was refused, and reporting it would either queue
// the same text twice or tell somebody that a submission they cannot see
// exists.
//
// Only an **unverified** one can be reported. A verified doléance has been
// read by a curator who let it stand, so a second report is asking the same
// person the same question — and the control is replaced by the mark in the
// interface precisely so nobody is invited to. The guard is here as well
// because an interface is not an authorisation.
//
// # It unpublishes, which is why the caller logs loudly
//
// This is the only path in the project by which somebody other than the author
// or a curator takes a published doléance off the register. It is reversible —
// a curator accepting it puts it back, verified — and it is bounded by the
// per-address write limit, and neither of those makes it harmless. See the
// handler for what that costs and what would reduce it.
func (s *Store) ReportMessage(ctx context.Context, id string) error {
	now := time.Now()

	result := s.db.WithContext(ctx).Model(&models.Message{}).
		Where("id = ? AND status = ? AND verified = ?", id, models.StatusAccepted, false).
		Updates(map[string]any{
			"status":     models.StatusCurating,
			"updated_at": now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		// Gone, not published, already reported, or verified. One answer for
		// all four: a reader finding out which would learn the state of a
		// submission that is not on the register.
		return ErrMessageNotFound
	}
	return nil
}

// GetMessage reads one doléance by identifier, whatever its state.
//
// The permalink resolves before a curator has looked at the message, because
// the contributor holding that link needs to see what they submitted. The
// caller decides what a stranger is shown.
func (s *Store) GetMessage(ctx context.Context, id string) (models.Message, error) {
	var message models.Message
	err := s.db.WithContext(ctx).Preload("Subjects").Where("id = ?", id).First(&message).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return message, ErrMessageNotFound
	}
	return message, err
}

// ListPublishedMessages returns the public register, newest first.
func (s *Store) ListPublishedMessages(ctx context.Context, limit int) ([]models.Message, error) {
	if limit <= 0 {
		limit = 50
	}

	var messages []models.Message
	err := s.db.WithContext(ctx).
		Preload("Subjects").
		Where("status = ?", models.StatusAccepted).
		Order("created_at desc").
		Limit(limit).
		Find(&messages).Error
	return messages, err
}

// NearbyMessages returns published doléances within radiusMetres of a point,
// nearest first.
//
// Two steps, and both are needed. The geohash prefixes narrow the table to a
// handful of cells using an index that behaves the same on PostgreSQL and
// SQLite; the distance check in Go then turns that square-ish region into an
// actual radius. Skipping the second step would return a box and call it a
// circle — visibly wrong at the corners, and wrong in a way nobody notices
// until they wonder why a village 40km away is "near" them.
func (s *Store) NearbyMessages(ctx context.Context, lat, lng, radiusMetres float64, limit int) ([]models.Message, error) {
	if limit <= 0 {
		limit = 50
	}

	precision := geo.PrecisionForRadius(radiusMetres)
	cells := geo.Cells(lat, lng, precision)

	query := s.db.WithContext(ctx).
		Preload("Subjects").
		Where("status = ?", models.StatusAccepted)

	// One LIKE per cell, OR'd. A prefix match is a range scan on the index;
	// a LIKE with a leading wildcard would not be, which is why the cells are
	// computed rather than the coordinates compared.
	prefixes := s.db.Session(&gorm.Session{NewDB: true})
	for i, cell := range cells {
		if i == 0 {
			prefixes = prefixes.Where("location_geohash LIKE ?", cell+"%")
			continue
		}
		prefixes = prefixes.Or("location_geohash LIKE ?", cell+"%")
	}

	var candidates []models.Message
	// The cell ring holds more than the radius, so read past the limit before
	// filtering: cutting at the limit first would drop near points in favour
	// of far ones that happened to be inserted earlier.
	err := query.Where(prefixes).
		Order("created_at desc").
		Limit(limit * 4).
		Find(&candidates).Error
	if err != nil {
		return nil, err
	}

	type scored struct {
		message  models.Message
		distance float64
	}
	within := make([]scored, 0, len(candidates))
	for _, message := range candidates {
		if message.Location == nil {
			continue
		}
		distance := geo.Distance(lat, lng, message.Location.Latitude, message.Location.Longitude)
		if distance <= radiusMetres {
			within = append(within, scored{message: message, distance: distance})
		}
	}

	sort.Slice(within, func(i, j int) bool { return within[i].distance < within[j].distance })
	if len(within) > limit {
		within = within[:limit]
	}

	messages := make([]models.Message, 0, len(within))
	for _, item := range within {
		messages = append(messages, item.message)
	}
	return messages, nil
}

// MessageQuery is what the register page asks for: the doléances inside the
// map's current viewport, optionally narrowed by subject.
type MessageQuery struct {
	// Bounds limits results to a map viewport. Nil means the whole register.
	Bounds *geo.Box

	// Unplaced asks for the doléances that sit on no point of the map:
	// somebody who named only a country or a region, which this register
	// deliberately stores as a name and not as a coordinate.
	//
	// # Why they are a query of their own
	//
	// They cannot answer a viewport, and for a long time that meant they were
	// simply missing from the register unless a reader found a button marked
	// "see everything, even without a place" — a control that existed to undo
	// a filter they never chose. Asking for them separately is what lets the
	// page keep a few slots for them whatever the map is showing, so the
	// button has nothing left to do.
	//
	// It is exclusive with Bounds rather than combined with it: the question
	// is "the ones with no place", and a viewport is the opposite question.
	Unplaced bool

	// Subjects are ORed: a doléance carrying any of them matches.
	//
	// It used to be an AND, on the reading that selecting more should narrow.
	// That is not what anybody does with a list of checkboxes: ticking
	// `transport` and `santé` reads as "show me both", and the AND answered
	// with doléances about buses *and* health — which is a rare and strange
	// intersection, usually empty. A filter whose second click almost always
	// empties the page teaches people to stop clicking.
	//
	// Each selection still reaches the subjects below it in the hierarchy, so
	// `transport` finds a doléance tagged `public transport`. That is the
	// point of keeping the precise subject rather than collapsing it into the
	// broad one: the writer's word survives, and the broad question finds it.
	Subjects []string

	// Shuffled asks for an arbitrary selection rather than the newest.
	//
	// # Why the register offers two orders and not one
	//
	// Newest-first with a cap is the only order a register can page through,
	// and it is also the order that buries everything older for ever: past the
	// first page nobody goes, so a doléance written last year is unreachable
	// however many people share its complaint. That is a poor fit for a
	// register whose whole claim is that every one of them counts the same.
	//
	// Shuffling fixes that and cannot be paged: `random()` draws a fresh order
	// per query, so a second page would repeat some rows and skip others. The
	// two properties are exclusive, so they are offered as a choice rather
	// than reconciled — shuffled and unpaged, or ordered and paged.
	//
	// Note what this is **not**: nothing here ranks. The alternative to time
	// is chance, never attention, because ranking by what readers do would say
	// some grievances are worth more than others.
	Shuffled bool

	// Offset pages through an ordered read. Meaningless when Shuffled, and
	// refused there rather than quietly returning overlapping rows.
	Offset int

	Limit int
}

// FindMessages returns published doléances matching a query, newest first,
// along with how many published doléances carry no coordinates at all.
//
// That second number matters. A location coarser than a department is stored
// as a name with no point — somebody who meant "France" did not give one — so
// those doléances can never fall inside a viewport. Filtering by map would
// hide them with nothing to show they exist, which is why the count comes back
// and the page says so.
func (s *Store) FindMessages(ctx context.Context, query MessageQuery) ([]models.Message, int64, error) {
	limit := query.Limit
	if limit <= 0 {
		limit = 50
	}

	db := s.db.WithContext(ctx).Model(&models.Message{}).
		Where("status = ?", models.StatusAccepted)

	// The viewport becomes geohash prefixes on the server, never in the
	// browser: the page sends four numbers and the conversion — which decides
	// what the database is asked — stays where it can be reasoned about and
	// tested. Each prefix is a range scan on location_geohash, the same index
	// every other location query uses.
	// bounds is what actually gets applied. It is cleared when the viewport is
	// too wide to narrow anything, so the refinement pass below agrees with
	// the query above rather than trimming against a filter that was not used.
	bounds := query.Bounds

	var cells []string
	if bounds != nil {
		cells = geo.Cover(*bounds)
		log.Debug().
			Float64("north", bounds.North).Float64("south", bounds.South).
			Float64("east", bounds.East).Float64("west", bounds.West).
			Bool("crosses_date_line", bounds.CrossesDateLine()).
			Int("cells", len(cells)).
			Msg("register: filtering by viewport")

		// An empty cover means the viewport is the whole world. Looking at
		// everywhere and looking at nowhere in particular are the same act, so
		// the register answers with everything it has — including the
		// doléances that named only a country and sit on no point of the map.
		// Excluding those while narrowing nothing reported "0 here" to
		// somebody looking at the entire planet.
		if len(cells) == 0 {
			log.Debug().Msg("register: viewport covers the world; no place filter applied")
			bounds = nil
		}
	}

	if query.Unplaced {
		// The ones that named a country or a region and nothing finer. An
		// empty geohash is how this register stores "no point", and it is
		// stored rather than inferred: a row with zero coordinates and no
		// hash is a doléance from a country, not one from the Gulf of Guinea.
		db = db.Where("location_geohash = ?", "")
		bounds = nil
	}

	if bounds != nil {
		// One LIKE per cell, OR'd. A prefix match is a range scan on the
		// index; a leading wildcard would not be, which is why the cells are
		// computed rather than the coordinates compared.
		prefixes := s.db.Session(&gorm.Session{NewDB: true})
		for i, cell := range cells {
			if i == 0 {
				prefixes = prefixes.Where("location_geohash LIKE ?", cell+"%")
				continue
			}
			prefixes = prefixes.Or("location_geohash LIKE ?", cell+"%")
		}
		db = db.Where(prefixes)
		// A doléance with no pin holds an empty geohash. It must not answer a
		// viewport query narrower than the world: its coordinates are zero,
		// which is a real point in the Gulf of Guinea, and it would surface
		// for anybody looking at that stretch of sea.
		db = db.Where("location_geohash <> ?", "")
	}

	if len(query.Subjects) > 0 {
		// Each selection carries the subjects below it. A failure here narrows
		// to the exact subjects rather than widening: a filter that quietly
		// returned more than was asked for would be worse than one that
		// returned the literal answer.
		subtrees, err := s.ExpandSubtrees(ctx, query.Subjects)
		if err != nil {
			log.Error().Err(err).Msg("cannot expand the subject hierarchy, filtering on the exact subjects")
			subtrees = nil
		}

		// One EXISTS over the union of every selection's subtree is what makes
		// the filter an OR: a doléance carrying any of them matches. A second
		// EXISTS per selection would be the AND this used to do.
		wanted := make([]string, 0, len(query.Subjects))
		seen := map[string]bool{}
		for _, subject := range query.Subjects {
			subtree := subtrees[subject]
			if len(subtree) == 0 {
				subtree = []string{subject}
			}
			for _, slug := range subtree {
				if !seen[slug] {
					seen[slug] = true
					wanted = append(wanted, slug)
				}
			}
		}
		db = db.Where(`EXISTS (
			SELECT 1 FROM message_subjects ms
			JOIN subjects s ON s.id = ms.subject_id
			WHERE ms.message_id = messages.id AND s.slug IN ?)`, wanted)
	}

	var candidates []models.Message
	// Cells always cover at least the viewport, so they overhang its edges.
	// Read past the limit and trim in Go, or the overhang would push genuinely
	// visible doléances off the end of the page.
	//
	// `random()` is the one ordering both engines spell the same way. SQLite
	// returns a large signed integer and PostgreSQL a double in [0,1); neither
	// matters, because the only thing asked of it is an order. Seeding is
	// **not** portable — PostgreSQL has setseed() and SQLite has nothing — so
	// a shuffled read can never be paged, which is why Offset is refused with
	// it rather than silently producing overlapping pages.
	order := "created_at desc"
	if query.Shuffled {
		order = "random()"
	}
	read := db.Preload("Subjects").Order(order).Limit(limit * 2)
	if query.Offset > 0 && !query.Shuffled {
		read = read.Offset(query.Offset)
	}

	var err error
	err = read.Find(&candidates).Error
	if err != nil {
		return nil, 0, err
	}

	messages := make([]models.Message, 0, len(candidates))
	var trimmed int
	for _, message := range candidates {
		// The refinement pass: a cell may reach past the edge of the screen,
		// and a doléance the reader cannot see on the map should not be in the
		// list beside it.
		if bounds != nil && message.Location != nil &&
			!bounds.Contains(message.Location.Latitude, message.Location.Longitude) {
			log.Trace().
				Str("message", message.ID).
				Float64("lat", message.Location.Latitude).
				Float64("lng", message.Location.Longitude).
				Str("geohash", message.Location.Geohash).
				Msg("register: outside the viewport, trimmed")
			trimmed++
			continue
		}
		messages = append(messages, message)
		if len(messages) == limit {
			break
		}
	}

	log.Debug().
		Int("candidates", len(candidates)).
		Int("returned", len(messages)).
		Int("trimmed_by_refinement", trimmed).
		Int("subjects", len(query.Subjects)).
		Msg("register: query finished")

	var unplaced int64
	err = s.db.WithContext(ctx).Model(&models.Message{}).
		Where("status = ? AND (location_geohash IS NULL OR location_geohash = ?)",
			models.StatusAccepted, "").
		Count(&unplaced).Error
	return messages, unplaced, err
}

// SetMessageLanguage records the language a model read the text in.
//
// Never written at submission, because only something that has read the text
// knows it: the page somebody typed on says nothing about what they typed.
//
// Written twice, by both model steps, and the later one wins. Assessment
// reports it first so that a queued or dropped submission has a language at
// all — classification only runs on what was published, and everything else
// would otherwise have none. Classification then overwrites it, because naming
// a text's subjects is a closer reading than scoring it.
func (s *Store) SetMessageLanguage(ctx context.Context, id, language string) error {
	if language == "" {
		// Unknown stays unknown. Writing an empty string over an empty column
		// is harmless, but writing a guess would not be.
		return nil
	}
	return s.db.WithContext(ctx).Model(&models.Message{}).
		Where("id = ?", id).
		Updates(map[string]any{"language": language, "updated_at": time.Now()}).Error
}

// DeleteMessage removes a doléance and everything derived from it.
//
// Deletion is real deletion: the row, its subject links and any pending
// revision. The audit log keeps the fact of a decision, never the text.
func (s *Store) DeleteMessage(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("DELETE FROM message_subjects WHERE message_id = ?", id).Error; err != nil {
			return err
		}
		if err := tx.Where("message_id = ?", id).Delete(&models.MessageRevision{}).Error; err != nil {
			return err
		}
		if err := forgetBookmarksOf(tx, models.BookmarkMessage, id); err != nil {
			return err
		}

		result := tx.Where("id = ?", id).Delete(&models.Message{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrMessageNotFound
		}
		return nil
	})
}

// PurgeExpiredMessages deletes the messages whose contributor set an expiry
// that has now passed. Retention that nothing enforces is not retention.
func (s *Store) PurgeExpiredMessages(ctx context.Context, now time.Time) (int64, error) {
	var expired []models.Message
	err := s.db.WithContext(ctx).
		Select("id").
		Where("expires_at IS NOT NULL AND expires_at <= ?", now).
		Find(&expired).Error
	if err != nil {
		return 0, err
	}

	var removed int64
	for _, message := range expired {
		if err := s.DeleteMessage(ctx, message.ID); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

// FindDuplicate returns the earliest doléance with the same text, or
// ErrMessageNotFound when this text is new here.
//
// The earliest, so that a flood of five hundred copies all point at one
// original rather than at each other in a chain — a curator reading the
// dropped sample then sees one conversation, not five hundred.
//
// Status is deliberately not filtered. A copy of something already dropped is
// still a copy, and a copy of something a curator is still looking at is too;
// the question this answers is "have we been sent this text before", which has
// nothing to do with what was decided about it. Deleted messages are gone from
// the table, so a contributor who removes their doléance frees the text to be
// written again — which is what real deletion has to mean.
func (s *Store) FindDuplicate(ctx context.Context, hash string) (models.Message, error) {
	if hash == "" {
		return models.Message{}, ErrMessageNotFound
	}

	var message models.Message
	err := s.db.WithContext(ctx).
		Where("content_hash = ?", hash).
		Order("created_at asc").
		First(&message).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.Message{}, ErrMessageNotFound
	}
	return message, err
}
