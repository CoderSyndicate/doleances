// Package apiclient talks to the backend's API.
//
// The console and the frontend reach all data through here. They hold no
// database credentials and no LLM key of their own, which is what lets them
// ship as near-empty container images.
package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// defaultTimeout bounds a call to the backend. A page that waits forever on a
// dependency is worse than a page that reports it is having trouble.
const defaultTimeout = 10 * time.Second

// Client is a handle on the backend API.
type Client struct {
	baseURL string
	http    *http.Client

	// session is the reader's own session token, forwarded as a bearer header.
	//
	// It is empty on the client a service holds and set on the per-request copy
	// As returns. That split is deliberate: the browser's credential belongs to
	// one request, and a client that carried it on a shared handle would leak
	// whoever spoke last into everybody else's page.
	session string
}

// New returns a client for the backend at baseURL.
func New(baseURL string) (*Client, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid backend url %q: %w", baseURL, err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid backend url %q: want scheme://host", baseURL)
	}

	return &Client{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		http:    &http.Client{Timeout: defaultTimeout},
	}, nil
}

// As returns a client that speaks for one signed-in reader.
//
// A copy rather than a mutation, and a shallow one: the HTTP client and its
// connection pool are shared, because the pool is the expensive part and
// nothing in it is per-reader.
//
// An empty session gives back a client that sends no credential, which is the
// right answer for a page a reader has not signed in for.
func (c *Client) As(session string) *Client {
	copied := *c
	copied.session = session
	return &copied
}

// authorize attaches the reader's session, if there is one.
//
// A bearer header rather than a cookie, because the backend is never reached by
// a browser: the frontend holds the cookie, the backend holds the rules, and no
// request to the backend is ambiently authenticated.
func (c *Client) authorize(req *http.Request) {
	if c.session != "" {
		req.Header.Set("Authorization", "Bearer "+c.session)
	}
}

// Subject is a theme the classifier assigns to messages.
type Subject struct {
	ID    string `json:"id"`
	Slug  string `json:"slug"`
	Label string `json:"label"`

	// Depth is how far below a root this subject sits, so the filter can show
	// the hierarchy by indenting. Zero for a subject with nothing above it.
	Depth int `json:"depth"`
}

// Indent renders the depth as leading space for a filter option.
//
// Figure spaces rather than the ordinary kind: they are non-breaking and
// fixed-width, so the indentation survives in a native form control where
// regular spaces would collapse.
func (s Subject) Indent() string {
	return strings.Repeat("\u2007\u2007", s.Depth)
}

// ListSubjects returns the subjects the register can be filtered by, named in
// the reader's language where the vocabulary knows the word.
//
// An empty language gets the labels as they were written, which is a mix of
// whichever spelling of each subject happened to arrive first.
func (c *Client) ListSubjects(ctx context.Context, language string) ([]Subject, error) {
	path := "/v1/subjects"
	if language != "" {
		path += "?language=" + url.QueryEscape(language)
	}

	var payload struct {
		Subjects []Subject `json:"subjects"`
	}
	if err := c.get(ctx, path, &payload); err != nil {
		return nil, err
	}
	return payload.Subjects, nil
}

// Ready reports whether the backend is reachable, for the readiness probe of
// a service that depends on it.
func (c *Client) Ready(ctx context.Context) error {
	var ignored json.RawMessage
	return c.get(ctx, "/v1/subjects", &ignored)
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("build request for %s: %w", path, err)
	}
	req.Header.Set("Accept", "application/json")
	c.authorize(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("call backend %s: %w", path, err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("backend %s returned %s", path, resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response from %s: %w", path, err)
	}
	return nil
}

// ActiveThemeCSS returns the active theme rendered as CSS override blocks.
//
// The caller serves this after its static defaults. An error is worth
// reporting but never worth failing a page over: the defaults are already
// loaded and remain correct.
func (c *Client) ActiveThemeCSS(ctx context.Context) ([]byte, error) {
	return c.getRaw(ctx, "/v1/theme.css")
}

// ThemeSummary describes one theme in the library.
type ThemeSummary struct {
	Name    string `json:"name"`
	Active  bool   `json:"active"`
	BuiltIn bool   `json:"built_in"`

	// Colors are the theme's colourways and ActiveColor the chosen one.
	Colors      []string `json:"colors"`
	ActiveColor string   `json:"active_color"`

	HasStructure bool `json:"has_structure"`
	Files        int  `json:"files"`

	// Swatches previews a palette; Sample previews a structure.
	Swatches map[string]string `json:"swatches,omitempty"`
	Sample   map[string]string `json:"sample,omitempty"`

	Warnings []ContrastWarning `json:"warnings,omitempty"`
}

// ContrastWarning is one pairing that fails WCAG AA.
type ContrastWarning struct {
	Mode  string  `json:"mode"`
	Pair  string  `json:"pair"`
	Ratio float64 `json:"ratio"`
	Min   float64 `json:"min"`
}

// ListThemes returns the theme library, each theme with its colourways.
func (c *Client) ListThemes(ctx context.Context) ([]ThemeSummary, error) {
	var payload struct {
		Themes []ThemeSummary `json:"themes"`
	}
	if err := c.get(ctx, "/v1/themes", &payload); err != nil {
		return nil, err
	}
	return payload.Themes, nil
}

// DownloadTheme returns a theme's DTCG document.
func (c *Client) DownloadTheme(ctx context.Context, name string) ([]byte, error) {
	return c.getRaw(ctx, "/v1/themes/"+url.PathEscape(name))
}

// UploadTheme stores a DTCG document, optionally selecting it at once.
func (c *Client) UploadTheme(ctx context.Context, name string, document []byte, activate bool) error {
	path := "/v1/themes/" + url.PathEscape(name)
	if activate {
		path += "?activate=true"
	}
	return c.send(ctx, http.MethodPut, path, "application/json", document)
}

// SetActiveTheme selects a theme by name, or restores the built-in palette.
func (c *Client) SetActiveTheme(ctx context.Context, name, colour string) error {
	body, err := json.Marshal(map[string]string{"name": name, "color": colour})
	if err != nil {
		return fmt.Errorf("encode theme selection: %w", err)
	}
	return c.send(ctx, http.MethodPut, "/v1/themes/active", "application/json", body)
}

// DeleteTheme removes a theme from the library.
func (c *Client) DeleteTheme(ctx context.Context, name string) error {
	return c.send(ctx, http.MethodDelete, "/v1/themes/"+url.PathEscape(name), "", nil)
}

func (c *Client) getRaw(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("build request for %s: %w", path, err)
	}
	c.authorize(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call backend %s: %w", path, err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("backend %s returned %s", path, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// send performs a write and reports the backend's own error message, which is
// what the console shows the operator.
func (c *Client) send(ctx context.Context, method, path, contentType string, body []byte) error {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("build request for %s: %w", path, err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	c.authorize(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("call backend %s: %w", path, err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode >= http.StatusBadRequest {
		return fmt.Errorf("%s", backendError(resp))
	}
	return nil
}

// backendError extracts the problem detail the API returned, falling back to
// the status line.
func backendError(resp *http.Response) string {
	var problem struct {
		Detail string `json:"detail"`
		Title  string `json:"title"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&problem); err == nil {
		if problem.Detail != "" {
			return problem.Detail
		}
		if problem.Title != "" {
			return problem.Title
		}
	}
	return resp.Status
}

// LLMSettings is how the backend reaches the language model. The API key is
// deliberately absent: it never leaves the backend.
type LLMSettings struct {
	BaseURL             string `json:"base_url"`
	ClassificationModel string `json:"classification_model"`
	TranslationModel    string `json:"translation_model"`
	TimeoutSeconds      int    `json:"timeout_seconds"`
	AcceptThreshold     int    `json:"accept_threshold"`
	CurateThreshold     int    `json:"curate_threshold"`
	APIKeySet           bool   `json:"api_key_set"`
}

// LLMSettingsUpdate carries edited settings. An empty APIKey keeps the stored
// one, which is what makes the other fields editable without re-entering the
// credential.
type LLMSettingsUpdate struct {
	BaseURL             string `json:"base_url"`
	APIKey              string `json:"api_key,omitempty"`
	ClassificationModel string `json:"classification_model"`
	TranslationModel    string `json:"translation_model"`
	TimeoutSeconds      int    `json:"timeout_seconds"`
	AcceptThreshold     int    `json:"accept_threshold"`
	CurateThreshold     int    `json:"curate_threshold"`
}

// LLMTestResult reports whether the configured endpoint answered.
type LLMTestResult struct {
	OK      bool     `json:"ok"`
	Message string   `json:"message"`
	Models  []string `json:"models,omitempty"`
}

// GetLLMSettings reads the LLM service configuration.
func (c *Client) GetLLMSettings(ctx context.Context) (LLMSettings, error) {
	var settings LLMSettings
	if err := c.get(ctx, "/v1/llm", &settings); err != nil {
		return LLMSettings{}, err
	}
	return settings, nil
}

// SaveLLMSettings writes the configuration, recorded against actor.
func (c *Client) SaveLLMSettings(ctx context.Context, actor string, update LLMSettingsUpdate) error {
	body, err := json.Marshal(update)
	if err != nil {
		return fmt.Errorf("encode llm settings: %w", err)
	}
	return c.sendAs(ctx, http.MethodPut, "/v1/llm", "application/json", actor, body)
}

// ClearLLMAPIKey removes the stored credential.
func (c *Client) ClearLLMAPIKey(ctx context.Context, actor string) error {
	return c.sendAs(ctx, http.MethodDelete, "/v1/llm/key", "", actor, nil)
}

// TestLLM asks the backend to call the configured endpoint.
func (c *Client) TestLLM(ctx context.Context) (LLMTestResult, error) {
	var result LLMTestResult
	if err := c.post(ctx, "/v1/llm/test", &result); err != nil {
		return LLMTestResult{}, err
	}
	return result, nil
}

func (c *Client) post(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("build request for %s: %w", path, err)
	}
	req.Header.Set("Accept", "application/json")
	c.authorize(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("call backend %s: %w", path, err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode >= http.StatusBadRequest {
		return fmt.Errorf("%s", backendError(resp))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// sendAs is send with the acting identity attached, so the backend can record
// who made the change.
func (c *Client) sendAs(ctx context.Context, method, path, contentType, actor string, body []byte) error {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("build request for %s: %w", path, err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if actor != "" {
		req.Header.Set("X-Actor", actor)
	}
	c.authorize(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("call backend %s: %w", path, err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode >= http.StatusBadRequest {
		return fmt.Errorf("%s", backendError(resp))
	}
	return nil
}

// ThemeAssetSummary describes one image slot of a theme.
type ThemeAssetSummary struct {
	Slot string `json:"slot"`
	// Path mirrors Slot for package files, which the console lists by path.
	Path        string `json:"path"`
	Description string `json:"description"`
	ContentType string `json:"content_type,omitempty"`
	Size        int    `json:"size,omitempty"`
	Custom      bool   `json:"custom"`
	HasDefault  bool   `json:"has_default"`
}

// Asset is an image with its media type.
type Asset struct {
	ContentType string
	Data        []byte
}

// ListThemeAssets returns every image slot of a theme, filled or not.
func (c *Client) ListThemeAssets(ctx context.Context, themeName string) ([]ThemeAssetSummary, error) {
	var payload struct {
		Assets []ThemeAssetSummary `json:"assets"`
	}
	if err := c.get(ctx, "/v1/theme-assets/"+url.PathEscape(themeName), &payload); err != nil {
		return nil, err
	}
	return payload.Assets, nil
}

// UploadThemeAsset replaces one image of a theme.
func (c *Client) UploadThemeAsset(ctx context.Context, themeName, slot, contentType string, data []byte) error {
	path := "/v1/theme-assets/" + url.PathEscape(themeName) + "/" + url.PathEscape(slot)
	return c.send(ctx, http.MethodPut, path, contentType, data)
}

// DeleteThemeAsset removes one image, so the slot falls back to the default.
func (c *Client) DeleteThemeAsset(ctx context.Context, themeName, slot string) error {
	path := "/v1/theme-assets/" + url.PathEscape(themeName) + "/" + url.PathEscape(slot)
	return c.send(ctx, http.MethodDelete, path, "", nil)
}

// ActiveThemeAsset resolves an image of the active theme, falling back to the
// built-in default.
func (c *Client) ActiveThemeAsset(ctx context.Context, slot string) (Asset, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/v1/theme-asset/"+url.PathEscape(slot), nil)
	if err != nil {
		return Asset{}, fmt.Errorf("build asset request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return Asset{}, fmt.Errorf("call backend for asset %q: %w", slot, err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		return Asset{}, fmt.Errorf("backend returned %s for asset %q", resp.Status, slot)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAssetBytes))
	if err != nil {
		return Asset{}, fmt.Errorf("read asset %q: %w", slot, err)
	}
	return Asset{ContentType: resp.Header.Get("Content-Type"), Data: data}, nil
}

// maxAssetBytes bounds what a web service will hold in memory for one image.
const maxAssetBytes = 1 << 20

func (c *Client) postJSON(ctx context.Context, path string, body []byte, out any) error {
	return c.sendJSON(ctx, http.MethodPost, path, body, out)
}

// sendJSON is postJSON for the verbs that are not POST. PATCH and DELETE both
// carry a management token in the body here, so they need the same decode.
func (c *Client) sendJSON(ctx context.Context, method, path string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request for %s: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	c.authorize(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("call backend %s: %w", path, err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode >= http.StatusBadRequest {
		return fmt.Errorf("%s", backendError(resp))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// GetHistorical reads one passage with its translations.
func (c *Client) GetHistorical(ctx context.Context, id string) (HistoricalText, error) {
	var text HistoricalText
	if err := c.get(ctx, "/v1/historical/"+url.PathEscape(id), &text); err != nil {
		return HistoricalText{}, err
	}
	return text, nil
}

// LikeHistorical records one more reader who recognised themselves in a
// passage, and returns the new count.
func (c *Client) LikeHistorical(ctx context.Context, id string) (int, error) {
	var answer struct {
		Likes int `json:"likes"`
	}
	path := "/v1/historical/" + url.PathEscape(id) + "/like"
	if err := c.postJSON(ctx, path, []byte("{}"), &answer); err != nil {
		return 0, err
	}
	return answer.Likes, nil
}

// LikeMessage records one more "me too" and returns the new count.
func (c *Client) LikeMessage(ctx context.Context, id string) (int, error) {
	var answer struct {
		Likes int `json:"likes"`
	}
	path := "/v1/messages/" + url.PathEscape(id) + "/like"
	if err := c.postJSON(ctx, path, []byte("{}"), &answer); err != nil {
		return 0, err
	}
	return answer.Likes, nil
}

// ReportMessage asks a human to look at a published doléance, which takes it
// off the register until one has.
//
// A 404 covers every refusal — not published, already reported, already
// verified, never existed — so a caller cannot learn the state of a submission
// that is not on the register by trying.
func (c *Client) ReportMessage(ctx context.Context, id string) error {
	var ignored json.RawMessage
	path := "/v1/messages/" + url.PathEscape(id) + "/report"
	return c.postJSON(ctx, path, []byte("{}"), &ignored)
}

// SpamItem is one dropped submission.
type SpamItem struct {
	ID          string    `json:"id"`
	Text        string    `json:"text"`
	Language    string    `json:"language,omitempty"`
	Confidence  int       `json:"confidence"`
	Refusal     string    `json:"refusal,omitempty"`
	Reason      string    `json:"reason,omitempty"`
	DuplicateOf string    `json:"duplicate_of,omitempty"`
	DroppedAt   time.Time `json:"dropped_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// SpamSample is what the spam page shows: a bounded sample plus the context to
// read it by.
type SpamSample struct {
	Items          []SpamItem `json:"items"`
	Held           int64      `json:"held"`
	RetentionHours int        `json:"retention_hours"`

	// Page and PerPage, so the console can walk a sample bigger than a screen
	// rather than showing its first page and calling that the whole of it.
	Page    int `json:"page"`
	PerPage int `json:"per_page"`
}

// ListSpam returns the dropped submissions still inside the retention window.
func (c *Client) ListSpam(ctx context.Context, page int) (SpamSample, error) {
	if page < 1 {
		page = 1
	}
	var sample SpamSample
	if err := c.get(ctx, "/v1/spam?page="+strconv.Itoa(page), &sample); err != nil {
		return SpamSample{}, err
	}
	return sample, nil
}

// RescueSpam sends a wrongly dropped submission to the curation queue.
func (c *Client) RescueSpam(ctx context.Context, actor, id string) error {
	return c.sendAs(ctx, http.MethodPost, "/v1/spam/"+url.PathEscape(id)+"/rescue", "", actor, nil)
}

// DeleteSpam removes a dropped submission now rather than at expiry.
func (c *Client) DeleteSpam(ctx context.Context, actor, id string) error {
	return c.sendAs(ctx, http.MethodDelete, "/v1/spam/"+url.PathEscape(id), "", actor, nil)
}

// CurationSettings are the policies curators work under.
type CurationSettings struct {
	SpamRetentionHours int `json:"spam_retention_hours"`
}

// GetCurationSettings reads the policies.
func (c *Client) GetCurationSettings(ctx context.Context) (CurationSettings, error) {
	var settings CurationSettings
	if err := c.get(ctx, "/v1/curation", &settings); err != nil {
		return CurationSettings{}, err
	}
	return settings, nil
}

// SaveCurationSettings writes the policies, recorded against actor.
func (c *Client) SaveCurationSettings(ctx context.Context, actor string, settings CurationSettings) error {
	body, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("encode curation settings: %w", err)
	}
	return c.sendAs(ctx, http.MethodPut, "/v1/curation", "application/json", actor, body)
}

// HistoricalText is one passage from an earlier register.
type HistoricalText struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	Text          string `json:"text"`
	Language      string `json:"language"`
	Period        string `json:"period,omitempty"`
	Region        string `json:"region,omitempty"`
	DocumentTitle string `json:"document_title,omitempty"`
	Source        string `json:"source,omitempty"`
	Placeholder   bool   `json:"placeholder"`

	// Latitude and Longitude are the commune the document was written for, so
	// a passage can sit on the register's map beside the doléances written
	// where it was written. Absent for a passage the corpus did not place.
	Latitude  float64 `json:"latitude,omitempty"`
	Longitude float64 `json:"longitude,omitempty"`

	// Likes is how many readers recognised themselves in it.
	Likes int `json:"likes"`

	// Translations are renderings into other languages.
	Translations []HistoricalTranslation `json:"translations,omitempty"`

	// Kept says whether the signed-in reader has this in their own list.
	// Always sent, never omitted: a boolean that disappears when false makes
	// "no" and "not asked" the same answer. See backend.MessageItem.
	Kept bool `json:"kept"`
}

// HistoricalTranslation is one passage in another language.
type HistoricalTranslation struct {
	Language      string `json:"language"`
	Title         string `json:"title"`
	Text          string `json:"text"`
	DocumentTitle string `json:"document_title,omitempty"`
	Region        string `json:"region,omitempty"`
	Source        string `json:"source,omitempty"`
}

// ListHistorical returns the historical corpus.
func (c *Client) ListHistorical(ctx context.Context) ([]HistoricalText, error) {
	var payload struct {
		Texts []HistoricalText `json:"texts"`
	}
	if err := c.get(ctx, "/v1/historical", &payload); err != nil {
		return nil, err
	}
	return payload.Texts, nil
}

// ThemeFile returns one file from a theme's package.
func (c *Client) ThemeFile(ctx context.Context, name, filePath string) (Asset, error) {
	target := c.baseURL + "/v1/theme-files/" + url.PathEscape(name) + "/" + filePath

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return Asset{}, fmt.Errorf("build theme-file request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return Asset{}, fmt.Errorf("call backend for theme file %q: %w", filePath, err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		return Asset{}, fmt.Errorf("backend returned %s for theme file %q", resp.Status, filePath)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAssetBytes))
	if err != nil {
		return Asset{}, fmt.Errorf("read theme file %q: %w", filePath, err)
	}
	return Asset{ContentType: resp.Header.Get("Content-Type"), Data: data}, nil
}

// DroppedItem is one refused submission as the public page reads it.
//
// It carries no score and no model sentence: see backend.DroppedItem for why
// publishing either would make the page a tuning instrument for flooding.
type DroppedItem struct {
	ID        string    `json:"id"`
	Reason    string    `json:"reason,omitempty"`
	Text      string    `json:"text,omitempty"`
	Language  string    `json:"language,omitempty"`
	Withheld  bool      `json:"withheld"`
	Hidden    bool      `json:"hidden"`
	Pleas     int       `json:"pleas"`
	DroppedAt time.Time `json:"dropped_at"`
}

// DroppedSample is the listing and how long a refusal stays readable.
type DroppedSample struct {
	Items          []DroppedItem `json:"items"`
	Held           int64         `json:"held"`
	RetentionHours int           `json:"retention_hours"`
	Page           int           `json:"page"`
	PerPage        int           `json:"per_page"`
}

// ListDropped reads what the register refused.
func (c *Client) ListDropped(ctx context.Context, page int) (DroppedSample, error) {
	if page < 1 {
		page = 1
	}
	var sample DroppedSample
	err := c.get(ctx, "/v1/dropped?page="+strconv.Itoa(page), &sample)
	return sample, err
}

// PleadForDropped records a reader saying a refusal was wrong.
func (c *Client) PleadForDropped(ctx context.Context, id string) (int, error) {
	var out struct {
		Pleas int `json:"pleas"`
	}
	path := "/v1/dropped/" + url.PathEscape(id) + "/plea"
	if err := c.postJSON(ctx, path, []byte("{}"), &out); err != nil {
		return 0, err
	}
	return out.Pleas, nil
}
