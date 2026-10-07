package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// MessageSubmission is a doléance on its way to the register.
type MessageSubmission struct {
	Text      string  `json:"text"`
	Nickname  string  `json:"nickname,omitempty"`
	BirthYear int     `json:"birth_year,omitempty"`
	Activity  string  `json:"activity,omitempty"`
	Latitude  float64 `json:"latitude,omitempty"`
	Longitude float64 `json:"longitude,omitempty"`
	Place     string  `json:"place,omitempty"`
	Country   string  `json:"country,omitempty"`

	// Zoom is the map zoom when the pin was placed. It is how precise the
	// contributor meant to be, and the backend never stores a finer location
	// than it allows.
	Zoom      int    `json:"zoom,omitempty"`
	ExpiresOn string `json:"expires_on,omitempty"`
	Agreement bool   `json:"agreement"`
}

// MessageReceipt is what comes back once, and only once.
type MessageReceipt struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Token  string `json:"token"`
}

// Message is a doléance as the register reports it.
type Message struct {
	ID        string     `json:"id"`
	Text      string     `json:"text"`
	Nickname  string     `json:"nickname,omitempty"`
	BirthYear int        `json:"birth_year,omitempty"`
	Activity  string     `json:"activity,omitempty"`
	Language  string     `json:"language,omitempty"`
	Status    string     `json:"status"`
	Subjects  []string   `json:"subjects,omitempty"`
	Place     string     `json:"place,omitempty"`
	Latitude  float64    `json:"latitude,omitempty"`
	Longitude float64    `json:"longitude,omitempty"`
	Geohash   string     `json:"geohash,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`

	// Likes is how many readers said this happened to them too — a count of
	// presses rather than of people, and nothing is ranked by it.
	Likes int `json:"likes"`

	// Excerpt is the card-sized version, derived when the doléance was
	// written. Truncated says whether making it cost anything, so a card can
	// offer the rest rather than leave somebody wondering whether a sentence
	// ended oddly or was cut.
	Excerpt   string `json:"excerpt"`
	Truncated bool   `json:"truncated"`

	// Verified says a human read this doléance and let it stand. Always sent,
	// never omitted: see backend.MessageItem.
	Verified bool `json:"verified"`

	// Kept says whether the signed-in reader has this in their own list.
	// Always sent, never omitted: a boolean that disappears when false makes
	// "no" and "not asked" the same answer. See backend.MessageItem.
	Kept bool `json:"kept"`
}

// SubmitMessage records a doléance and returns its permalink and token.
func (c *Client) SubmitMessage(ctx context.Context, submission MessageSubmission) (MessageReceipt, error) {
	body, err := json.Marshal(submission)
	if err != nil {
		return MessageReceipt{}, fmt.Errorf("encode submission: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return MessageReceipt{}, fmt.Errorf("build submission request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return MessageReceipt{}, fmt.Errorf("call backend: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode >= http.StatusBadRequest {
		return MessageReceipt{}, fmt.Errorf("%s", backendError(resp))
	}

	var receipt MessageReceipt
	if err := json.NewDecoder(resp.Body).Decode(&receipt); err != nil {
		return MessageReceipt{}, fmt.Errorf("decode receipt: %w", err)
	}
	return receipt, nil
}

// GetMessage reads one doléance by identifier.
func (c *Client) GetMessage(ctx context.Context, id string) (Message, error) {
	var message Message
	if err := c.get(ctx, "/v1/messages/"+url.PathEscape(id), &message); err != nil {
		return Message{}, err
	}
	return message, nil
}

// MessageQuery is what the register page asks for: a map viewport, subjects,
// or neither.
type MessageQuery struct {
	Limit    int
	Subjects []string

	// Bounds is the map's current viewport. All four are needed together;
	// Unplaced asks only for the doléances with no point on the map — the
	// ones a viewport can never answer for. Exclusive with the bounds below.
	Unplaced bool

	// HasBounds says whether they were set at all.
	North, South, East, West float64
	HasBounds                bool

	// Shuffled asks for an arbitrary selection instead of the newest, and
	// cannot be combined with Offset: a shuffled read has no pages.
	Shuffled bool

	// Offset pages through a newest-first read.
	Offset int
}

// MessagePage is a slice of the register plus what it could not show.
type MessagePage struct {
	Messages []Message `json:"messages"`

	// Unplaced counts published doléances with no coordinates, which no
	// viewport can contain.
	Unplaced int64 `json:"unplaced"`

	// More says a further page exists. Always false for a shuffled read.
	More bool `json:"more"`
}

// ListMessages returns the published register, newest first.
func (c *Client) ListMessages(ctx context.Context, limit int) ([]Message, error) {
	page, err := c.FindMessages(ctx, MessageQuery{Limit: limit})
	if err != nil {
		return nil, err
	}
	return page.Messages, nil
}

// FindMessages returns the register narrowed by viewport and subjects.
func (c *Client) FindMessages(ctx context.Context, query MessageQuery) (MessagePage, error) {
	values := url.Values{}
	if query.Limit > 0 {
		values.Set("limit", strconv.Itoa(query.Limit))
	}
	for _, subject := range query.Subjects {
		values.Add("subject", subject)
	}
	if query.Unplaced {
		values.Set("unplaced", "true")
	}
	if query.HasBounds && !query.Unplaced {
		edge := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
		values.Set("bounds", edge(query.North)+","+edge(query.South)+
			","+edge(query.East)+","+edge(query.West))
	}
	if query.Shuffled {
		values.Set("order", "random")
	} else if query.Offset > 0 {
		values.Set("offset", strconv.Itoa(query.Offset))
	}

	path := "/v1/messages"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}

	var page MessagePage
	if err := c.get(ctx, path, &page); err != nil {
		return MessagePage{}, err
	}
	return page, nil
}

// NearbyMessages returns published doléances within a radius of a point,
// nearest first.
func (c *Client) NearbyMessages(ctx context.Context, lat, lng, radiusMetres float64, limit int) ([]Message, error) {
	query := url.Values{}
	query.Set("lat", strconv.FormatFloat(lat, 'f', -1, 64))
	query.Set("lng", strconv.FormatFloat(lng, 'f', -1, 64))
	query.Set("radius", strconv.FormatFloat(radiusMetres, 'f', -1, 64))
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}

	var payload struct {
		Messages []Message `json:"messages"`
	}
	if err := c.get(ctx, "/v1/messages/nearby?"+query.Encode(), &payload); err != nil {
		return nil, err
	}
	return payload.Messages, nil
}

// DeleteMessage removes a doléance, authorised by its token.
func (c *Client) DeleteMessage(ctx context.Context, id, deletionToken string) error {
	path := "/v1/messages/" + url.PathEscape(id) + "?token=" + url.QueryEscape(deletionToken)
	return c.send(ctx, http.MethodDelete, path, "", nil)
}
