package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/CoderSyndicate/doleances/internal/geo"
	"github.com/CoderSyndicate/doleances/internal/models"
)

func TestCreateMessageStartsPending(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	message := models.Message{Text: "La maternité a fermé.", TokenHash: "hash"}
	if err := s.CreateMessage(ctx, &message); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	if message.ID == "" {
		t.Error("no identifier was assigned")
	}
	// A doléance is accepted the moment it is written down; what decides
	// whether it is published happens afterwards.
	if message.Status != models.StatusPending {
		t.Errorf("status = %q, want %q", message.Status, models.StatusPending)
	}

	stored, err := s.GetMessage(ctx, message.ID)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if stored.Text != message.Text {
		t.Errorf("text = %q, want %q", stored.Text, message.Text)
	}
}

func TestGetMessageReportsMissing(t *testing.T) {
	s := newStore(t)

	_, err := s.GetMessage(context.Background(), "no-such-id")
	if !errors.Is(err, ErrMessageNotFound) {
		t.Errorf("err = %v, want ErrMessageNotFound", err)
	}
}

// TestPublishedRegisterExcludesUnpublished pins what the public listing is:
// the register, not the queue.
func TestPublishedRegisterExcludesUnpublished(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	for _, status := range []models.ReviewStatus{
		models.StatusPending, models.StatusCurating, models.StatusAccepted,
		models.StatusRejected, models.StatusDropped,
	} {
		message := models.Message{Text: string(status), Status: status, TokenHash: "h"}
		if err := s.CreateMessage(ctx, &message); err != nil {
			t.Fatalf("CreateMessage(%s): %v", status, err)
		}
	}

	published, err := s.ListPublishedMessages(ctx, 50)
	if err != nil {
		t.Fatalf("ListPublishedMessages: %v", err)
	}
	if len(published) != 1 {
		t.Fatalf("published = %d messages, want 1", len(published))
	}
	if published[0].Status != models.StatusAccepted {
		t.Errorf("published status = %q", published[0].Status)
	}
}

func TestDeleteMessageIsReal(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	message := models.Message{Text: "à supprimer", TokenHash: "h"}
	if err := s.CreateMessage(ctx, &message); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	if err := s.DeleteMessage(ctx, message.ID); err != nil {
		t.Fatalf("DeleteMessage: %v", err)
	}
	if _, err := s.GetMessage(ctx, message.ID); !errors.Is(err, ErrMessageNotFound) {
		t.Error("the message survived deletion")
	}
	// Deleting something already gone is an error, not a silent success: the
	// caller asked about a specific message.
	if err := s.DeleteMessage(ctx, message.ID); !errors.Is(err, ErrMessageNotFound) {
		t.Errorf("second delete err = %v, want ErrMessageNotFound", err)
	}
}

// TestPurgeExpiredMessages covers the retention promise the form makes: an
// expiry date means deletion at that date, without a reminder.
func TestPurgeExpiredMessages(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(24 * time.Hour)

	expired := models.Message{Text: "expired", TokenHash: "h", ExpiresAt: &past}
	living := models.Message{Text: "living", TokenHash: "h", ExpiresAt: &future}
	permanent := models.Message{Text: "permanent", TokenHash: "h"}
	for _, m := range []*models.Message{&expired, &living, &permanent} {
		if err := s.CreateMessage(ctx, m); err != nil {
			t.Fatalf("CreateMessage: %v", err)
		}
	}

	removed, err := s.PurgeExpiredMessages(ctx, time.Now())
	if err != nil {
		t.Fatalf("PurgeExpiredMessages: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}

	if _, err := s.GetMessage(ctx, expired.ID); !errors.Is(err, ErrMessageNotFound) {
		t.Error("the expired message survived")
	}
	for _, id := range []string{living.ID, permanent.ID} {
		if _, err := s.GetMessage(ctx, id); err != nil {
			t.Errorf("a message that should have been kept is gone: %v", err)
		}
	}
}

// TestNearbyMessagesFiltersByRealDistance checks both halves of the proximity
// query: the geohash prefixes narrow the table, and the distance check turns
// that square-ish region into an actual circle.
func TestNearbyMessagesFiltersByRealDistance(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	// Saint-Jean-de-Luz, and points at increasing distance from it.
	const lat, lng = 43.3883, -1.6626
	places := []struct {
		name     string
		lat, lng float64
	}{
		{"same street", 43.3885, -1.6628}, // ~30 m
		{"across town", 43.4063, -1.6626}, // ~2 km
		{"Bayonne", 43.4929, -1.4748},     // ~25 km
		{"Houdan", 48.7889, 1.6019},       // ~640 km
	}
	for _, place := range places {
		message := models.Message{
			Text:      place.name,
			Status:    models.StatusAccepted,
			TokenHash: "h",
			Location:  &models.Location{Latitude: place.lat, Longitude: place.lng, Label: place.name},
		}
		if err := s.CreateMessage(ctx, &message); err != nil {
			t.Fatalf("CreateMessage(%s): %v", place.name, err)
		}
		// The hook derives the geohash; nothing sets it by hand.
		if message.Location.Geohash == "" {
			t.Fatalf("%s was stored without a geohash", place.name)
		}
	}

	// A message with no location must never appear in a proximity search.
	unplaced := models.Message{Text: "nowhere", Status: models.StatusAccepted, TokenHash: "h"}
	if err := s.CreateMessage(ctx, &unplaced); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	tests := []struct {
		radius float64
		want   []string
	}{
		{1_000, []string{"same street"}},
		{5_000, []string{"same street", "across town"}},
		{50_000, []string{"same street", "across town", "Bayonne"}},
	}
	for _, tt := range tests {
		found, err := s.NearbyMessages(ctx, lat, lng, tt.radius, 50)
		if err != nil {
			t.Fatalf("NearbyMessages(%.0f): %v", tt.radius, err)
		}
		if len(found) != len(tt.want) {
			t.Errorf("within %.0fm: got %d messages, want %d", tt.radius, len(found), len(tt.want))
			for _, m := range found {
				t.Logf("  got %q", m.Text)
			}
			continue
		}
		// Nearest first, so the order is part of the contract.
		for i, want := range tt.want {
			if found[i].Text != want {
				t.Errorf("within %.0fm: position %d is %q, want %q", tt.radius, i, found[i].Text, want)
			}
		}
	}
}

// TestGeohashFollowsTheCoordinates guards the derived-data invariant: moving a
// pin must move the geohash, or the row becomes invisible to a search of its
// own neighbourhood.
func TestGeohashFollowsTheCoordinates(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	message := models.Message{
		Text:      "moved",
		Status:    models.StatusAccepted,
		TokenHash: "h",
		Location:  &models.Location{Latitude: 43.3883, Longitude: -1.6626},
	}
	if err := s.CreateMessage(ctx, &message); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	first := message.Location.Geohash

	// Move the pin to Houdan and save again, without touching the geohash.
	message.Location.Latitude, message.Location.Longitude = 48.7889, 1.6019
	if err := s.DB().WithContext(ctx).Save(&message).Error; err != nil {
		t.Fatalf("Save: %v", err)
	}

	stored, err := s.GetMessage(ctx, message.ID)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if stored.Location.Geohash == first {
		t.Error("the geohash did not follow the coordinates")
	}
	if stored.Location.Geohash != geo.Encode(48.7889, 1.6019) {
		t.Errorf("geohash = %q, want the one for the new point", stored.Location.Geohash)
	}

	// And it is findable where it now is, not where it was.
	found, err := s.NearbyMessages(ctx, 48.7889, 1.6019, 1_000, 10)
	if err != nil {
		t.Fatalf("NearbyMessages: %v", err)
	}
	if len(found) != 1 {
		t.Errorf("found %d messages at the new location, want 1", len(found))
	}
}

// TestFindMessagesByViewport covers the register page's behaviour: the list
// describes the area on screen, and nothing outside it leaks in through the
// overhang of the geohash cells.
func TestFindMessagesByViewport(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	places := map[string][2]float64{
		"Saint-Jean-de-Luz": {43.3883, -1.6626},
		"Bayonne":           {43.4929, -1.4748},
		"Bordeaux":          {44.8378, -0.5792},
		"Houdan":            {48.7889, 1.6019},
	}
	for name, at := range places {
		message := models.Message{
			Text:      name,
			Status:    models.StatusAccepted,
			TokenHash: "h",
			Location:  &models.Location{Latitude: at[0], Longitude: at[1], Label: name},
		}
		if err := s.CreateMessage(ctx, &message); err != nil {
			t.Fatalf("CreateMessage(%s): %v", name, err)
		}
	}

	// A doléance that named only its country has no coordinates and can fall
	// in no viewport. It must be counted, not silently dropped.
	unplaced := models.Message{
		Text: "France", Status: models.StatusAccepted, TokenHash: "h",
		Location: &models.Location{Label: "France", CountryCode: "FR"},
	}
	if err := s.CreateMessage(ctx, &unplaced); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	tests := []struct {
		name string
		box  geo.Box
		want []string
	}{
		{
			"the Basque coast",
			geo.Box{North: 43.55, South: 43.30, East: -1.40, West: -1.80},
			[]string{"Bayonne", "Saint-Jean-de-Luz"},
		},
		{
			"one town",
			geo.Box{North: 43.42, South: 43.36, East: -1.62, West: -1.70},
			[]string{"Saint-Jean-de-Luz"},
		},
		{
			"the south-west",
			geo.Box{North: 45.2, South: 43.0, East: 0.2, West: -2.0},
			[]string{"Bordeaux", "Bayonne", "Saint-Jean-de-Luz"},
		},
		{
			"open sea",
			geo.Box{North: 45.0, South: 44.0, East: -5.0, West: -6.0},
			nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			box := tt.box
			found, unplacedCount, err := s.FindMessages(ctx, MessageQuery{Bounds: &box, Limit: 50})
			if err != nil {
				t.Fatalf("FindMessages: %v", err)
			}

			got := map[string]bool{}
			for _, message := range found {
				got[message.Text] = true
			}
			if len(got) != len(tt.want) {
				t.Errorf("found %d messages, want %d", len(got), len(tt.want))
				for _, m := range found {
					t.Logf("  got %q", m.Text)
				}
			}
			for _, want := range tt.want {
				if !got[want] {
					t.Errorf("%q is in the viewport but was not returned", want)
				}
			}
			// The one that named only a country is never in a viewport, but is
			// always counted so the page can say it exists.
			if got["France"] {
				t.Error("a doléance with no coordinates answered a viewport query")
			}
			if unplacedCount != 1 {
				t.Errorf("unplaced = %d, want 1", unplacedCount)
			}
		})
	}
}

// TestFindMessagesWithoutBoundsReturnsEverything: the register with no map
// filter is the whole published register, unplaced doléances included.
func TestFindMessagesWithoutBoundsReturnsEverything(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	placed := models.Message{
		Text: "placed", Status: models.StatusAccepted, TokenHash: "h",
		Location: &models.Location{Latitude: 43.3883, Longitude: -1.6626},
	}
	unplaced := models.Message{Text: "unplaced", Status: models.StatusAccepted, TokenHash: "h"}
	for _, m := range []*models.Message{&placed, &unplaced} {
		if err := s.CreateMessage(ctx, m); err != nil {
			t.Fatalf("CreateMessage: %v", err)
		}
	}

	found, _, err := s.FindMessages(ctx, MessageQuery{Limit: 50})
	if err != nil {
		t.Fatalf("FindMessages: %v", err)
	}
	if len(found) != 2 {
		t.Errorf("found %d messages, want both", len(found))
	}
}

// TestWorldViewportShowsEverything: when the map shows the whole planet, the
// viewport and "everywhere" are the same thing, so a reader looking at all of
// it must see all of it.
func TestWorldViewportShowsEverything(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	dusseldorf := models.Message{
		Text: "Düsseldorf", Status: models.StatusAccepted, TokenHash: "h",
		Location: &models.Location{Latitude: 51.2277, Longitude: 6.7735},
	}
	unplaced := models.Message{
		Text: "somewhere in Germany", Status: models.StatusAccepted, TokenHash: "h",
		Location: &models.Location{Label: "Deutschland", CountryCode: "DE"},
	}
	for _, m := range []*models.Message{&dusseldorf, &unplaced} {
		if err := s.CreateMessage(ctx, m); err != nil {
			t.Fatalf("CreateMessage: %v", err)
		}
	}

	world := geo.Box{North: 85.05, South: -85.05, East: 180, West: -180}
	found, _, err := s.FindMessages(ctx, MessageQuery{Bounds: &world, Limit: 50})
	if err != nil {
		t.Fatalf("FindMessages: %v", err)
	}

	names := map[string]bool{}
	for _, m := range found {
		names[m.Text] = true
	}
	if !names["Düsseldorf"] {
		t.Error("Düsseldorf is not shown with the whole world in view")
	}
	if !names["somewhere in Germany"] {
		t.Error("a doléance with no pin is not shown with the whole world in view")
	}
}

// TestLanguageIsWrittenByTheClassifierNotTheForm pins where the language comes
// from.
//
// A submission carries none: the page somebody typed on says nothing about
// what they typed, and a guess would travel into the subject vocabulary and
// into the similarity thresholds that depend on knowing the language.
func TestLanguageIsWrittenByTheClassifierNotTheForm(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	message := models.Message{Text: "Die Straßenbahn fährt nicht mehr.", TokenHash: "h"}
	if err := s.CreateMessage(ctx, &message); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	stored, err := s.GetMessage(ctx, message.ID)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if stored.Language != "" {
		t.Errorf("a fresh submission carries language %q, want none until it is read",
			stored.Language)
	}

	if err := s.SetMessageLanguage(ctx, message.ID, "de"); err != nil {
		t.Fatalf("SetMessageLanguage: %v", err)
	}
	stored, _ = s.GetMessage(ctx, message.ID)
	if stored.Language != "de" {
		t.Errorf("language = %q, want de", stored.Language)
	}

	// An unknown language never overwrites a known one: the classifier failing
	// to say must not erase what a previous run established.
	if err := s.SetMessageLanguage(ctx, message.ID, ""); err != nil {
		t.Fatalf("SetMessageLanguage: %v", err)
	}
	stored, _ = s.GetMessage(ctx, message.ID)
	if stored.Language != "de" {
		t.Errorf("an empty language erased a known one: %q", stored.Language)
	}
}

// TestABroaderSubjectReachesTheNarrowerOnes is the point of keeping both.
//
// The classifier is right to write "transport en commun" when somebody
// complains about a bus, and a reader asking the broad question should still
// find them. Collapsing the two would have lost the writer's word; this keeps
// it and answers the broad question anyway.
func TestABroaderSubjectReachesTheNarrowerOnes(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	transport, _ := s.CreateSubject(ctx, "transport", "fr", "", nil, "")
	public, _ := s.CreateSubject(ctx, "transport en commun", "fr", "", nil, "")
	bus, _ := s.CreateSubject(ctx, "bus", "fr", "", nil, "")
	health, _ := s.CreateSubject(ctx, "santé", "fr", "", nil, "")

	for _, link := range [][2]string{{public.ID, transport.ID}, {bus.ID, public.ID}} {
		if err := s.LinkSubjects(ctx, link[0], link[1], models.RelationSourceCurator); err != nil {
			t.Fatalf("LinkSubjects: %v", err)
		}
	}

	published := func(text string, subjectIDs ...string) models.Message {
		m := models.Message{Text: text, Status: models.StatusAccepted, TokenHash: "h"}
		if err := s.CreateMessage(ctx, &m); err != nil {
			t.Fatalf("CreateMessage: %v", err)
		}
		if err := s.AttachSubjects(ctx, m.ID, subjectIDs); err != nil {
			t.Fatalf("AttachSubjects: %v", err)
		}
		return m
	}

	onABus := published("le bus ne passe plus", bus.ID)
	published("la maternité a fermé", health.ID)
	both := published("pas de bus pour aller à l'hôpital", bus.ID, health.ID)

	find := func(slugs ...string) map[string]bool {
		found, _, err := s.FindMessages(ctx, MessageQuery{Subjects: slugs, Limit: 50})
		if err != nil {
			t.Fatalf("FindMessages(%v): %v", slugs, err)
		}
		ids := map[string]bool{}
		for _, m := range found {
			ids[m.ID] = true
		}
		return ids
	}

	// Two levels up still reaches it.
	if got := find(transport.Slug); !got[onABus.ID] {
		t.Error("filtering by transport did not reach a doléance tagged bus")
	}
	// The precise question still gets the precise answer.
	if got := find(bus.Slug); !got[onABus.ID] {
		t.Error("filtering by bus did not find the bus doléance")
	}
	// And a broad selection does not leak sideways.
	if got := find(transport.Slug); got[both.ID] != true {
		t.Error("expected the doléance carrying bus and santé to match transport")
	}
	if got := find(health.Slug); got[onABus.ID] {
		t.Error("filtering by santé returned a doléance that is only about buses")
	}

	// Selections are ORed: ticking two boxes asks for either, which is what
	// anybody doing it means. The AND this used to do answered with doléances
	// about buses *and* health — a rare intersection, usually empty, so the
	// second click emptied the page and taught people to stop clicking.
	got := find(transport.Slug, health.Slug)
	if !got[both.ID] {
		t.Error("the doléance carrying bus and santé should match transport or santé")
	}
	if !got[onABus.ID] {
		t.Error("a doléance about buses should match when transport is one of the selections")
	}

	// And it is still a filter: something carrying neither stays out.
	unrelated := published("le loyer a doublé")
	if find(transport.Slug, health.Slug)[unrelated.ID] {
		t.Error("the OR widened to doléances carrying neither subject")
	}
}

// TestAnUnknownSubjectNarrowsRatherThanWidens: a slug nobody has must return
// nothing. Widening to everything would answer a question that was not asked.
func TestAnUnknownSubjectNarrowsRatherThanWidens(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	subject, _ := s.CreateSubject(ctx, "santé", "fr", "", nil, "")
	m := models.Message{Text: "la maternité a fermé", Status: models.StatusAccepted, TokenHash: "h"}
	if err := s.CreateMessage(ctx, &m); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	if err := s.AttachSubjects(ctx, m.ID, []string{subject.ID}); err != nil {
		t.Fatalf("AttachSubjects: %v", err)
	}

	found, _, err := s.FindMessages(ctx, MessageQuery{Subjects: []string{"pas-un-sujet"}, Limit: 50})
	if err != nil {
		t.Fatalf("FindMessages: %v", err)
	}
	if len(found) != 0 {
		t.Errorf("an unknown subject returned %d doléances, want none", len(found))
	}
}

// TestLikesAddUpRatherThanOverwrite. Two readers pressing at the same moment
// must both be counted; a read-modify-write would lose one.
func TestLikesAddUpRatherThanOverwrite(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	message := models.Message{Text: "la maternité a fermé", Status: models.StatusAccepted, TokenHash: "h"}
	if err := s.CreateMessage(ctx, &message); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.LikeMessage(ctx, message.ID); err != nil {
				t.Errorf("LikeMessage: %v", err)
			}
		}()
	}
	wg.Wait()

	stored, err := s.GetMessage(ctx, message.ID)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if stored.Likes != 8 {
		t.Errorf("likes = %d, want 8 — presses were lost", stored.Likes)
	}
}

// TestOnlyAPublishedDoleanceCanBeLiked. A count on text nobody can see would
// be a signal about a submission the public has no business knowing exists.
func TestOnlyAPublishedDoleanceCanBeLiked(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	for _, status := range []models.ReviewStatus{
		models.StatusPending, models.StatusCurating, models.StatusDropped,
	} {
		message := models.Message{Text: "pas encore publiée", Status: status, TokenHash: "h"}
		if err := s.CreateMessage(ctx, &message); err != nil {
			t.Fatalf("CreateMessage: %v", err)
		}
		if _, err := s.LikeMessage(ctx, message.ID); !errors.Is(err, ErrMessageNotFound) {
			t.Errorf("status %q: err = %v, want ErrMessageNotFound", status, err)
		}
	}
}
