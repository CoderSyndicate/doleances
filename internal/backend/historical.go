package backend

import (
	"context"
	"errors"
	"net/http"

	"github.com/CoderSyndicate/doleances/internal/cache"
	"github.com/CoderSyndicate/doleances/internal/corpus"
	"github.com/CoderSyndicate/doleances/internal/models"
	"github.com/CoderSyndicate/doleances/internal/store"
	"github.com/danielgtaylor/huma/v2"
	"github.com/rs/zerolog/log"
)

func (a *API) registerHistoricalRoutes(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "list-historical",
		Method:      http.MethodGet,
		Path:        "/v1/historical",
		Summary:     "List the historical doléances",
		Description: "Passages from earlier registers, shown alongside present-day ones. " +
			"Each carries its source: a quotation from someone who cannot correct the record needs its provenance.",
		Tags: []string{"Historical"},
	}, a.listHistorical)

	huma.Register(api, huma.Operation{
		OperationID: "get-historical",
		Method:      http.MethodGet,
		Path:        "/v1/historical/{id}",
		Summary:     "Read one passage",
		Description: "The passage entire, with its translations — what a card links to.",
		Tags:        []string{"Historical"},
	}, a.getHistorical)

	huma.Register(api, huma.Operation{
		OperationID: "like-historical",
		Method:      http.MethodPost,
		Path:        "/v1/historical/{id}/like",
		Summary:     "Say this happened to you too",
		Description: "Anonymous by construction, like a doléance's. It means something " +
			"slightly different here, and the difference is the point of the corpus: " +
			"somebody recognising themselves in 1789 is saying the two registers are one " +
			"act performed twice.",
		Tags: []string{"Historical"},
	}, a.likeHistorical)
}

// HistoricalItem is one passage as the site shows it.
type HistoricalItem struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Text     string `json:"text"`
	Language string `json:"language"`
	Period   string `json:"period,omitempty"`
	Region   string `json:"region,omitempty"`

	// DocumentTitle names the document the passage comes from.
	DocumentTitle string `json:"document_title,omitempty"`
	Source        string `json:"source,omitempty"`

	// Latitude and Longitude are the commune the document was written for, so
	// a passage can sit on the register's map beside the doléances written
	// where it was written. Absent for a passage the corpus did not place.
	Latitude  float64 `json:"latitude,omitempty"`
	Longitude float64 `json:"longitude,omitempty"`

	// Placeholder marks seeded example content, so it can never be mistaken
	// for real archival material.
	Placeholder bool `json:"placeholder"`

	// Likes is how many readers recognised themselves in it — presses rather
	// than people, and nothing is ranked by it.
	Likes int `json:"likes"`

	// Translations are renderings into other languages, keyed by language.
	Translations []HistoricalTranslationItem `json:"translations,omitempty"`

	// Kept says whether the signed-in reader has this in their own list.
	// Always sent, never omitted: a boolean that disappears when false makes
	// "no" and "not asked" the same answer. See backend.MessageItem.
	Kept bool `json:"kept"`
}

// HistoricalTranslationItem is one passage in another language.
type HistoricalTranslationItem struct {
	Language      string `json:"language"`
	Title         string `json:"title"`
	Text          string `json:"text"`
	DocumentTitle string `json:"document_title,omitempty"`
	Region        string `json:"region,omitempty"`
	Source        string `json:"source,omitempty"`
}

// HistoricalOutput is the corpus listing.
type HistoricalOutput struct {
	Body struct {
		Texts []HistoricalItem `json:"texts"`
	}
}

func (a *API) listHistorical(ctx context.Context, _ *struct{}) (*HistoricalOutput, error) {
	// The corpus is read on the landing page, beside the submission form and
	// on Voices of the past, and it changes when a curator corrects a
	// transcription — which is to say almost never. It is the cheapest entry
	// in the whole cache.
	texts, err := cache.Fetch(a.cache, cache.Keyed("historical.list"),
		cache.Historical, func() ([]models.HistoricalText, error) {
			return a.store.ListHistoricalTexts(ctx)
		})
	if err != nil {
		log.Error().Err(err).Msg("cannot list historical texts")
		return nil, huma.Error500InternalServerError("cannot read the historical texts")
	}

	ids := make([]string, 0, len(texts))
	for _, text := range texts {
		ids = append(ids, text.ID)
	}
	kept := a.markKept(ctx, models.BookmarkHistorical, ids)

	out := &HistoricalOutput{}
	for _, text := range texts {
		translations := make([]HistoricalTranslationItem, 0, len(text.Translations))
		for _, t := range text.Translations {
			translations = append(translations, HistoricalTranslationItem{
				Language:      t.Language,
				Title:         t.Title,
				Text:          t.Text,
				DocumentTitle: t.DocumentTitle,
				Region:        t.Region,
				Source:        t.Source,
			})
		}

		item := toHistoricalItem(text)
		item.Translations = translations
		item.Kept = kept[text.ID]
		out.Body.Texts = append(out.Body.Texts, item)
	}
	return out, nil
}

// toHistoricalItem is one passage on the wire. Its translations are filled in
// by the caller, which knows whether the reader is getting all of them or the
// one they can read.
func toHistoricalItem(text models.HistoricalText) HistoricalItem {
	item := HistoricalItem{
		ID:            text.ID,
		Title:         text.Title,
		Text:          text.Text,
		Language:      text.Language,
		Period:        text.Period,
		Region:        text.Region,
		DocumentTitle: text.DocumentTitle,
		Source:        text.Source,
		Placeholder:   text.Placeholder,
		Likes:         text.Likes,
	}
	if text.Location != nil {
		item.Latitude, item.Longitude = text.Location.Latitude, text.Location.Longitude
	}
	for _, t := range text.Translations {
		item.Translations = append(item.Translations, HistoricalTranslationItem{
			Language:      t.Language,
			Title:         t.Title,
			Text:          t.Text,
			DocumentTitle: t.DocumentTitle,
			Region:        t.Region,
			Source:        t.Source,
		})
	}
	return item
}

// seedCorpus puts the bundled historical passages into the database on first
// start, the same way bundled themes are seeded. Translations are seeded
// alongside their passage, so a reader meets the text in their own language
// where one exists.
func (a *API) seedCorpus(ctx context.Context) error {
	entries, err := corpus.Load()
	if err != nil {
		return err
	}

	seeded, translated := 0, 0
	for _, entry := range entries {
		text := models.HistoricalText{
			Model:         models.Model{ID: entry.ID},
			Title:         entry.Title,
			Text:          entry.Text,
			Language:      entry.Source.Language,
			Period:        entry.Source.Period,
			Region:        entry.Source.Region,
			DocumentTitle: entry.Source.Title,
			Source:        entry.Source.Reference,
			// These are transcriptions of real documents, not examples.
			Placeholder: false,
		}
		// A passage the corpus placed gets a point; one it did not keeps its
		// region in words and sits on no point of the map, which is the truth
		// rather than a gap. Null Island is not a place anybody wrote from.
		if entry.Source.Latitude != 0 || entry.Source.Longitude != 0 {
			text.Location = &models.Location{
				Latitude:  entry.Source.Latitude,
				Longitude: entry.Source.Longitude,
				Label:     entry.Source.Region,
			}
		}
		for language, translation := range entry.Translations {
			text.Translations = append(text.Translations, models.HistoricalTranslation{
				Model:            models.Model{ID: entry.ID + ":" + language},
				HistoricalTextID: entry.ID,
				Language:         language,
				Title:            translation.Title,
				Text:             translation.Text,
				DocumentTitle:    translation.Source.Title,
				Region:           translation.Source.Region,
				Source:           translation.Source.Reference,
			})
		}

		added, err := a.store.SeedHistoricalText(ctx, text)
		if err != nil {
			return err
		}
		if added {
			seeded++
			translated += len(text.Translations)
		}
	}
	if seeded > 0 {
		log.Info().Int("passages", seeded).Int("translations", translated).
			Msg("historical corpus seeded")
	}
	return nil
}

// HistoricalTextOutput is one passage.
type HistoricalTextOutput struct {
	Body HistoricalItem
}

func (a *API) getHistorical(ctx context.Context, in *MessageIDInput) (*HistoricalTextOutput, error) {
	text, err := a.store.GetHistoricalText(ctx, in.ID)
	if errors.Is(err, store.ErrHistoricalNotFound) {
		return nil, huma.Error404NotFound("no such passage")
	}
	if err != nil {
		log.Error().Err(err).Str("text", in.ID).Msg("cannot read a historical text")
		return nil, huma.Error500InternalServerError("cannot read the passage")
	}
	item := toHistoricalItem(text)
	item.Kept = a.markKept(ctx, models.BookmarkHistorical, []string{text.ID})[text.ID]
	return &HistoricalTextOutput{Body: item}, nil
}

// likeHistorical records one more reader who recognised themselves.
func (a *API) likeHistorical(ctx context.Context, in *MessageIDInput) (*LikeOutput, error) {
	likes, err := a.store.LikeHistoricalText(ctx, in.ID)
	if errors.Is(err, store.ErrHistoricalNotFound) {
		return nil, huma.Error404NotFound("no such passage")
	}
	if err != nil {
		log.Error().Err(err).Str("text", in.ID).Msg("cannot record a like")
		return nil, huma.Error500InternalServerError("cannot record that")
	}

	out := &LikeOutput{}
	out.Body.Likes = likes
	return out, nil
}
