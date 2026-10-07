package apiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// The two registers a reader can keep something from.
const (
	KeptMessage    = "message"
	KeptHistorical = "historical"
)

// KeptText is one entry of a reader's own list.
//
// Exactly one of the two texts is set, matching Kind. They are separate fields
// rather than one shape with a type tag because they are genuinely different
// objects — a doléance carries a place, an activity and a birth year, a passage
// carries a source and its translations — and a common shape would mean a page
// that could show neither properly.
type KeptText struct {
	Kind   string    `json:"kind"`
	KeptAt time.Time `json:"kept_at"`

	Message    *Message        `json:"message,omitempty"`
	Historical *HistoricalText `json:"historical,omitempty"`
}

// When renders the moment it was kept.
func (k KeptText) When() string { return k.KeptAt.Format("2 January 2006") }

// Bookmarks is what an account kept, most recently kept first.
func (c *Client) Bookmarks(ctx context.Context) ([]KeptText, error) {
	var answer struct {
		Kept []KeptText `json:"kept"`
	}
	if err := c.get(ctx, "/v1/accounts/me/bookmarks", &answer); err != nil {
		return nil, err
	}
	return answer.Kept, nil
}

// Keep puts a doléance or a passage in the reader's list.
func (c *Client) Keep(ctx context.Context, kind, id string) error {
	body, err := json.Marshal(map[string]string{"kind": kind, "id": id})
	if err != nil {
		return fmt.Errorf("encode bookmark: %w", err)
	}
	var ignored json.RawMessage
	return c.sendJSON(ctx, http.MethodPost, "/v1/accounts/me/bookmarks", body, &ignored)
}

// Release takes it back out.
func (c *Client) Release(ctx context.Context, kind, id string) error {
	path := "/v1/accounts/me/bookmarks/" + url.PathEscape(kind) + "/" + url.PathEscape(id)
	var ignored json.RawMessage
	return c.sendJSON(ctx, http.MethodDelete, path, []byte("{}"), &ignored)
}
