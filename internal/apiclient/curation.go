package apiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// CurationItem is one submission awaiting a human decision.
type CurationItem struct {
	ID        string    `json:"id"`
	Text      string    `json:"text"`
	Nickname  string    `json:"nickname,omitempty"`
	BirthYear int       `json:"birth_year,omitempty"`
	Activity  string    `json:"activity,omitempty"`
	Language  string    `json:"language,omitempty"`
	Place     string    `json:"place,omitempty"`
	CreatedAt time.Time `json:"created_at"`

	Status     string `json:"status"`
	Assessed   bool   `json:"assessed"`
	Confidence int    `json:"confidence"`
	Reason     string `json:"reason,omitempty"`
}

// CurationQueue is what is waiting, and how much of it no classifier has seen.
type CurationQueue struct {
	Items      []CurationItem `json:"items"`
	Unassessed int            `json:"unassessed"`
}

// ListCurationQueue returns the submissions awaiting a decision.
func (c *Client) ListCurationQueue(ctx context.Context) (CurationQueue, error) {
	var queue CurationQueue
	if err := c.get(ctx, "/v1/curation/queue", &queue); err != nil {
		return CurationQueue{}, err
	}
	return queue, nil
}

// AcceptMessage publishes a doléance.
func (c *Client) AcceptMessage(ctx context.Context, actor, id, reason string) error {
	return c.decide(ctx, "accept", actor, id, reason)
}

// RejectMessage refuses a submission, which deletes it.
func (c *Client) RejectMessage(ctx context.Context, actor, id, reason string) error {
	return c.decide(ctx, "reject", actor, id, reason)
}

func (c *Client) decide(ctx context.Context, verb, actor, id, reason string) error {
	body, err := json.Marshal(map[string]string{"actor": actor, "reason": reason})
	if err != nil {
		return fmt.Errorf("encode decision: %w", err)
	}
	path := "/v1/curation/" + url.PathEscape(id) + "/" + verb
	return c.send(ctx, http.MethodPost, path, "application/json", body)
}

// SubjectSide is one of the two subjects in a question.
type SubjectSide struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Slug     string `json:"slug"`
	Language string `json:"language,omitempty"`
	QID      string `json:"qid,omitempty"`
	Entity   string `json:"entity,omitempty"`
	Article  string `json:"article,omitempty"`
}

// QuestionContext is the doléance that raised a subject question.
type QuestionContext struct {
	ID        string    `json:"id"`
	Text      string    `json:"text"`
	Language  string    `json:"language,omitempty"`
	Place     string    `json:"place,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// SubjectQuestion is two subjects that may be one, for a curator to settle.
type SubjectQuestion struct {
	ID   string      `json:"id"`
	New  SubjectSide `json:"new"`
	Into SubjectSide `json:"into"`

	Source        string           `json:"source"`
	QID           string           `json:"qid,omitempty"`
	Entity        string           `json:"entity,omitempty"`
	Similarity    float64          `json:"similarity"`
	CrossLanguage bool             `json:"cross_language"`
	Agreement     string           `json:"agreement"`
	Entities      string           `json:"entities"`
	Message       *QuestionContext `json:"message,omitempty"`
}

// SubjectQuestions is what is waiting.
type SubjectQuestions struct {
	Items []SubjectQuestion `json:"items"`
}

// ListSubjectQuestions returns the subject questions awaiting a decision.
func (c *Client) ListSubjectQuestions(ctx context.Context) (SubjectQuestions, error) {
	var questions SubjectQuestions
	if err := c.get(ctx, "/v1/curation/subjects", &questions); err != nil {
		return SubjectQuestions{}, err
	}
	return questions, nil
}

// MergeSubjects confirms that two subjects are one.
func (c *Client) MergeSubjects(ctx context.Context, actor, id, reason string) error {
	return c.decideSubject(ctx, "merge", actor, id, reason)
}

// KeepSubjectsApart records that two subjects are different things.
func (c *Client) KeepSubjectsApart(ctx context.Context, actor, id, reason string) error {
	return c.decideSubject(ctx, "dismiss", actor, id, reason)
}

func (c *Client) decideSubject(ctx context.Context, verb, actor, id, reason string) error {
	body, err := json.Marshal(map[string]string{"actor": actor, "reason": reason})
	if err != nil {
		return fmt.Errorf("encode decision: %w", err)
	}
	path := "/v1/curation/subjects/" + url.PathEscape(id) + "/" + verb
	return c.send(ctx, http.MethodPost, path, "application/json", body)
}

// VocabularyAlias is one other spelling of a subject.
type VocabularyAlias struct {
	Label    string `json:"label"`
	Language string `json:"language,omitempty"`
	Source   string `json:"source,omitempty"`
}

// VocabularyEntry is one subject with what a curator needs to judge it.
type VocabularyEntry struct {
	ID       string            `json:"id"`
	Label    string            `json:"label"`
	Slug     string            `json:"slug"`
	Language string            `json:"language,omitempty"`
	QID      string            `json:"qid,omitempty"`
	Entity   string            `json:"entity,omitempty"`
	Messages int64             `json:"messages"`
	Aliases  []VocabularyAlias `json:"aliases,omitempty"`
}

// Vocabulary is the subject list as the console shows it.
type Vocabulary struct {
	Subjects []VocabularyEntry `json:"subjects"`
}

// ListVocabulary returns every subject with its aliases and usage.
func (c *Client) ListVocabulary(ctx context.Context) (Vocabulary, error) {
	var vocabulary Vocabulary
	if err := c.get(ctx, "/v1/curation/vocabulary", &vocabulary); err != nil {
		return Vocabulary{}, err
	}
	return vocabulary, nil
}

// RenameSubject changes the label a reader sees.
func (c *Client) RenameSubject(ctx context.Context, actor, id, label, reason string) error {
	body, err := json.Marshal(map[string]string{
		"label": label, "actor": actor, "reason": reason,
	})
	if err != nil {
		return fmt.Errorf("encode rename: %w", err)
	}
	path := "/v1/curation/subjects/" + url.PathEscape(id) + "/rename"
	return c.send(ctx, http.MethodPost, path, "application/json", body)
}

// EntityProposal is a Wikidata identity awaiting a curator's confirmation.
type EntityProposal struct {
	ID          string           `json:"id"`
	Subject     string           `json:"subject"`
	SubjectID   string           `json:"subject_id"`
	Language    string           `json:"language,omitempty"`
	QID         string           `json:"qid"`
	Label       string           `json:"label"`
	Description string           `json:"description,omitempty"`
	Entity      string           `json:"entity,omitempty"`
	Article     string           `json:"article,omitempty"`
	MatchType   string           `json:"match_type,omitempty"`
	Confidence  int              `json:"confidence"`
	Reason      string           `json:"reason,omitempty"`
	Message     *QuestionContext `json:"message,omitempty"`
}

// EntityProposals is what is waiting.
type EntityProposals struct {
	Items []EntityProposal `json:"items"`
}

// ListEntityProposals returns the identities awaiting confirmation.
func (c *Client) ListEntityProposals(ctx context.Context) (EntityProposals, error) {
	var proposals EntityProposals
	if err := c.get(ctx, "/v1/curation/entities", &proposals); err != nil {
		return EntityProposals{}, err
	}
	return proposals, nil
}

// ConfirmEntity attributes a Wikidata identity to a subject. It is the only
// path that writes a QID.
func (c *Client) ConfirmEntity(ctx context.Context, actor, id, reason string) error {
	return c.decideEntity(ctx, "confirm", actor, id, reason)
}

// RejectEntity records that the entity is not that subject.
func (c *Client) RejectEntity(ctx context.Context, actor, id, reason string) error {
	return c.decideEntity(ctx, "reject", actor, id, reason)
}

func (c *Client) decideEntity(ctx context.Context, verb, actor, id, reason string) error {
	body, err := json.Marshal(map[string]string{"actor": actor, "reason": reason})
	if err != nil {
		return fmt.Errorf("encode decision: %w", err)
	}
	path := "/v1/curation/entities/" + url.PathEscape(id) + "/" + verb
	return c.send(ctx, http.MethodPost, path, "application/json", body)
}

// SubjectDetail is one subject with its relations, for the editing page.
type SubjectDetail struct {
	VocabularyEntry
	Parents  []VocabularyEntry `json:"parents,omitempty"`
	Children []VocabularyEntry `json:"children,omitempty"`
}

// WikidataCandidate is one entity offered to a curator.
type WikidataCandidate struct {
	QID         string `json:"qid"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Entity      string `json:"entity"`
	Article     string `json:"article,omitempty"`
	MatchType   string `json:"match_type,omitempty"`
	Held        string `json:"held,omitempty"`
	HeldID      string `json:"held_id,omitempty"`
}

// SearchSubjects finds subjects by any of their names.
func (c *Client) SearchSubjects(ctx context.Context, query string) (Vocabulary, error) {
	var out Vocabulary
	if err := c.get(ctx, "/v1/curation/vocabulary/search?q="+url.QueryEscape(query), &out); err != nil {
		return Vocabulary{}, err
	}
	return out, nil
}

// ListDetachedSubjects returns the subjects outside the hierarchy.
func (c *Client) ListDetachedSubjects(ctx context.Context) (Vocabulary, error) {
	var out Vocabulary
	if err := c.get(ctx, "/v1/curation/vocabulary/detached", &out); err != nil {
		return Vocabulary{}, err
	}
	return out, nil
}

// GetSubject returns one subject with its relations.
func (c *Client) GetSubject(ctx context.Context, id string) (SubjectDetail, error) {
	var out SubjectDetail
	if err := c.get(ctx, "/v1/curation/vocabulary/"+url.PathEscape(id), &out); err != nil {
		return SubjectDetail{}, err
	}
	return out, nil
}

// SearchWikidata looks an entity up through the backend, never from the
// console's browser.
func (c *Client) SearchWikidata(ctx context.Context, query, language string) ([]WikidataCandidate, error) {
	var out struct {
		Candidates []WikidataCandidate `json:"candidates"`
	}
	path := "/v1/curation/wikidata?q=" + url.QueryEscape(query) +
		"&language=" + url.QueryEscape(language)
	if err := c.get(ctx, path, &out); err != nil {
		return nil, err
	}
	return out.Candidates, nil
}

// AttachEntity attributes an entity to a subject, or relates it as a parent or
// a child.
func (c *Client) AttachEntity(ctx context.Context, actor, id, qid, label, relation string) error {
	body, err := json.Marshal(map[string]string{
		"qid": qid, "label": label, "relation": relation, "actor": actor,
	})
	if err != nil {
		return fmt.Errorf("encode attachment: %w", err)
	}
	path := "/v1/curation/vocabulary/" + url.PathEscape(id) + "/entity"
	return c.send(ctx, http.MethodPost, path, "application/json", body)
}

// DetachEntity removes a subject's identity and the names derived from it.
func (c *Client) DetachEntity(ctx context.Context, actor, id string) error {
	body, err := json.Marshal(map[string]string{"actor": actor})
	if err != nil {
		return fmt.Errorf("encode detachment: %w", err)
	}
	path := "/v1/curation/vocabulary/" + url.PathEscape(id) + "/entity"
	return c.send(ctx, http.MethodDelete, path, "application/json", body)
}

// UnlinkSubjects removes a relation between two subjects.
func (c *Client) UnlinkSubjects(ctx context.Context, actor, id, other string) error {
	body, err := json.Marshal(map[string]string{"actor": actor})
	if err != nil {
		return fmt.Errorf("encode unlink: %w", err)
	}
	path := "/v1/curation/vocabulary/" + url.PathEscape(id) +
		"/relations/" + url.PathEscape(other)
	return c.send(ctx, http.MethodDelete, path, "application/json", body)
}
