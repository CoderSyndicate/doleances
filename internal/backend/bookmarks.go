package backend

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/rs/zerolog/log"

	"github.com/CoderSyndicate/doleances/internal/models"
)

// registerBookmarkRoutes declares the reader's own list.
//
// Account-scoped throughout, and that is what distinguishes this from the two
// like routes next to it. A like is anonymous because there is nobody to
// attribute it to and the register wants nobody; a bookmark names the account
// that made it, which is allowed for one reason: the reader asked for it, they
// are the only person it answers to, and they can take any of it back. See
// models.Bookmark.
//
// None of these declare invalidates(...). Nothing cached is per-reader — that
// is the property the whole cache rests on — so the "did I keep this" mark is
// computed outside it, from one extra query for a signed-in caller.
func (a *API) registerBookmarkRoutes(api huma.API) {
	huma.Register(api, authenticated(huma.Operation{
		OperationID: "list-bookmarks",
		Method:      http.MethodGet,
		Path:        "/v1/accounts/me/bookmarks",
		Summary:     "The texts this account kept",
		Description: "Most recently kept first, with the doléance or the passage resolved. " +
			"A bookmark whose text has since been deleted is removed rather than " +
			"shown as a gap the reader cannot clear.",
		Tags: []string{"Accounts"},
	}), a.listBookmarks)

	huma.Register(api, authenticated(huma.Operation{
		OperationID: "keep-text",
		Method:      http.MethodPost,
		Path:        "/v1/accounts/me/bookmarks",
		Summary:     "Keep a doléance or a passage",
		Description: "Keeping something twice is not an error: somebody pressed the button " +
			"twice, or their page was stale, and either way it is in their list.",
		Tags: []string{"Accounts"},
	}), a.keepText)

	huma.Register(api, authenticated(huma.Operation{
		OperationID: "release-text",
		Method:      http.MethodDelete,
		Path:        "/v1/accounts/me/bookmarks/{kind}/{target}",
		Summary:     "Take a text back out of the list",
		Description: "Removing one that is not there answers the same way: a reader taking " +
			"something back must never be shown a failure for a row already gone.",
		Tags: []string{"Accounts"},
	}), a.releaseText)
}

// BookmarkInput is what to keep.
type BookmarkInput struct {
	Body struct {
		// Kind is "message" or "historical".
		Kind string `json:"kind" doc:"message or historical"`
		ID   string `json:"id"`
	}
}

// BookmarkTargetInput addresses one entry of the list.
type BookmarkTargetInput struct {
	Kind   string `path:"kind"`
	Target string `path:"target"`
}

// KeptItem is one entry: the bookmark's own moment, and the text.
//
// The two texts are separate fields rather than one shape with a type tag,
// because they are genuinely different objects — a doléance carries a place,
// an activity and a birth year, a passage carries a source and its
// translations — and flattening them into a common shape would mean a listing
// that could show neither properly.
type KeptItem struct {
	Kind   string `json:"kind"`
	KeptAt string `json:"kept_at"`

	Message    *MessageItem    `json:"message,omitempty"`
	Historical *HistoricalItem `json:"historical,omitempty"`
}

// KeptListOutput is the reader's own list.
type KeptListOutput struct {
	Body struct {
		Kept []KeptItem `json:"kept"`
	}
}

func (a *API) listBookmarks(ctx context.Context, _ *struct{}) (*KeptListOutput, error) {
	who, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}

	kept, err := a.store.ListBookmarks(ctx, who.Account.ID)
	if err != nil {
		log.Error().Err(err).Msg("cannot read the kept texts")
		return nil, huma.Error500InternalServerError("cannot read what you kept")
	}

	out := &KeptListOutput{}
	out.Body.Kept = make([]KeptItem, 0, len(kept))
	for _, entry := range kept {
		item := KeptItem{
			Kind:   string(entry.Bookmark.Kind),
			KeptAt: entry.Bookmark.KeptAt.Format("2006-01-02T15:04:05Z07:00"),
		}
		switch {
		case entry.Message != nil:
			message := toMessageItem(*entry.Message)
			// Every entry of this list is kept by definition, which is what
			// lets the page offer the same control it offers everywhere else.
			message.Kept = true
			item.Message = &message
		case entry.Historical != nil:
			passage := toHistoricalItem(*entry.Historical)
			passage.Kept = true
			item.Historical = &passage
		}
		out.Body.Kept = append(out.Body.Kept, item)
	}
	return out, nil
}

func (a *API) keepText(ctx context.Context, in *BookmarkInput) (*DoneOutput, error) {
	who, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}

	kind := models.BookmarkKind(in.Body.Kind)
	if !kind.Valid() {
		return nil, huma.Error422UnprocessableEntity("kind must be message or historical")
	}
	if in.Body.ID == "" {
		return nil, huma.Error422UnprocessableEntity("say which text to keep")
	}

	// The text has to exist, or the list is a place to park arbitrary
	// identifiers and the reader's own page fills with entries that resolve to
	// nothing.
	if err := a.readable(ctx, kind, in.Body.ID); err != nil {
		return nil, err
	}

	if err := a.store.AddBookmark(ctx, who.Account.ID, kind, in.Body.ID); err != nil {
		log.Error().Err(err).Str("kind", in.Body.Kind).Msg("cannot keep a text")
		return nil, huma.Error500InternalServerError("cannot keep that")
	}
	return done(), nil
}

func (a *API) releaseText(ctx context.Context, in *BookmarkTargetInput) (*DoneOutput, error) {
	who, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}

	kind := models.BookmarkKind(in.Kind)
	if !kind.Valid() {
		return nil, huma.Error422UnprocessableEntity("kind must be message or historical")
	}

	if err := a.store.RemoveBookmark(ctx, who.Account.ID, kind, in.Target); err != nil {
		log.Error().Err(err).Str("kind", in.Kind).Msg("cannot release a text")
		return nil, huma.Error500InternalServerError("cannot change your list")
	}
	return done(), nil
}

// readable reports whether a text resolves at all.
//
// Exactly the same question the permalink asks, deliberately — `GetMessage`
// answers for a submission a curator has not looked at yet, because the
// contributor holding that link needs to see what they wrote. So an identifier
// somebody holds is already a capability, and nothing here is being guarded
// that is not guarded there: what this refuses is an identifier naming nothing
// at all.
func (a *API) readable(ctx context.Context, kind models.BookmarkKind, id string) error {
	switch kind {
	case models.BookmarkMessage:
		if _, err := a.store.GetMessage(ctx, id); err != nil {
			return huma.Error404NotFound("no such doléance")
		}
	case models.BookmarkHistorical:
		if _, err := a.store.GetHistoricalText(ctx, id); err != nil {
			return huma.Error404NotFound("no such passage")
		}
	}
	return nil
}

// markKept fills in the "did I keep this" flag for a signed-in caller.
//
// One query for the whole page rather than one per card: the register's
// listings run to fifty, and a mark that cost a round trip each would make
// every page slower for exactly the people who signed in.
//
// It runs **outside** the cache, deliberately. Nothing account-scoped is ever
// stored there — a single entry keyed without the reader in it would serve one
// person's list to another — so the cached rows are the register and this is
// the one reader's annotation on top of them.
func (a *API) markKept(ctx context.Context, kind models.BookmarkKind, ids []string) map[string]bool {
	who := callerOf(ctx)
	if who == nil || len(ids) == 0 {
		return nil
	}

	marks, err := a.store.BookmarkedBy(ctx, who.Account.ID, kind, ids)
	if err != nil {
		// A listing without the marks is still the listing. Losing a button's
		// state is a presentation problem; losing the register would take the
		// page down.
		log.Error().Err(err).Msg("cannot read which texts this reader kept")
		return nil
	}
	return marks
}
