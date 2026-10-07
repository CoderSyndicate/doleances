package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rs/zerolog/log"
)

// EntityCandidate is one Wikidata entry offered for a subject.
type EntityCandidate struct {
	QID         string
	Label       string
	Description string

	// MatchType is how the search found it — "label", "alias" or
	// "description". It is given to the model because an alias match is the
	// shape of the worst errors: "transports spatiaux" is an alias of
	// spaceflight, and that is the only reason spaceflight was ever offered
	// for "transports".
	MatchType string
}

// EntityPick is the model's choice among them.
type EntityPick struct {
	// QID is the chosen entity, or "" for none.
	QID string

	// Confidence is 0-100 that the entry means the same as the subject.
	//
	// It is what a curator reads to decide how closely to look, and it is
	// what a future policy would use to let the clearest cases through
	// without one. That policy needs a measured false-positive rate per band
	// first, which is why nothing acts on this number yet.
	Confidence int

	Reason string
}

// PickEntity chooses the Wikidata entry that means the same as a subject.
//
// # Why a model does this
//
// The alternative was tried and measured. A blocklist over the candidates'
// descriptions rejected Wikidata's best answers — "moyen de transporter
// plusieurs personnes ensemble" was thrown out for containing "personne" — and
// let spaceflight, solitary confinement and a job-vacancy agency through in
// their place. Telling a concept from an instance is a judgement about
// meaning, and a substring test cannot make one.
//
// The descriptions carry the answer: "état d'isolement d'une personne" against
// "strict form of imprisonment" against "épisode de la série télévisée The
// Walking Dead" is not a hard question for something that reads.
//
// # What it does not do
//
// Attribute anything. The pick is a proposal a curator confirms, and nothing —
// no alias, no merge, no translation — depends on it until they have.
//
// One call per new subject that has candidates, not per message, and new
// subjects stop arriving after a register's first weeks.
func (c *Client) PickEntity(ctx context.Context, model, subject, doleance string,
	candidates []EntityCandidate) (EntityPick, error) {

	if len(candidates) == 0 {
		return EntityPick{}, nil
	}

	system, err := prompts.ReadFile("prompts/pick-entity.md")
	if err != nil {
		return EntityPick{}, fmt.Errorf("llm: read prompt: %w", err)
	}

	answer, err := c.CompleteJSON(ctx, model, []Message{
		{Role: "system", Content: string(system)},
		{Role: "user", Content: describeCandidates(subject, doleance, candidates)},
	})
	if err != nil {
		return EntityPick{}, err
	}

	pick, err := parseEntityPick(answer, candidates)
	if err != nil {
		log.Warn().Str("answer", truncate(answer, 400)).
			Msg("llm: entity pick could not be read")
		return EntityPick{}, err
	}

	log.Debug().Str("subject", subject).Str("qid", pick.QID).
		Int("confidence", pick.Confidence).Str("reason", pick.Reason).
		Msg("llm: entity picked")
	return pick, nil
}

// describeCandidates lays the choice out for the model.
//
// Numbered rather than keyed by QID: a model asked to reproduce "Q134670827"
// exactly will sometimes get a digit wrong, and a wrong QID is indistinguishable
// from a deliberate choice. A number out of range is obviously nonsense and is
// refused.
func describeCandidates(subject, doleance string, candidates []EntityCandidate) string {
	var b strings.Builder

	b.WriteString("Subject: " + subject + "\n\n")
	if doleance = strings.TrimSpace(doleance); doleance != "" {
		b.WriteString("From this doléance:\n" + truncate(doleance, 600) + "\n\n")
	}
	b.WriteString("Candidates:\n")

	for i, candidate := range candidates {
		description := candidate.Description
		if description == "" {
			// Said explicitly rather than left blank: an entity with no
			// description is usually a sparse entry, and that is evidence.
			description = "(no description)"
		}
		fmt.Fprintf(&b, "%d. %s — %s [matched on %s]\n",
			i+1, candidate.Label, description, candidate.MatchType)
	}
	return b.String()
}

// parseEntityPick reads the answer and checks it against what was offered.
func parseEntityPick(answer string, candidates []EntityCandidate) (EntityPick, error) {
	object := extractJSONObject(answer)
	if object == "" {
		return EntityPick{}, fmt.Errorf("%w: no JSON object in the reply", ErrUnusableAnswer)
	}

	var decoded struct {
		Pick       json.Number `json:"pick"`
		Confidence json.Number `json:"confidence"`
		Reason     string      `json:"reason"`
	}
	if err := json.Unmarshal([]byte(object), &decoded); err != nil {
		return EntityPick{}, fmt.Errorf("%w: %v", ErrUnusableAnswer, err)
	}

	pick, err := decoded.Pick.Int64()
	if err != nil {
		return EntityPick{}, fmt.Errorf("%w: pick %q is not a number",
			ErrUnusableAnswer, decoded.Pick.String())
	}

	// Zero is "none of these", which the prompt asks for explicitly and which
	// is the right answer often enough that it must not read as a failure.
	if pick == 0 {
		return EntityPick{Reason: strings.TrimSpace(decoded.Reason)}, nil
	}
	if pick < 0 || pick > int64(len(candidates)) {
		// Out of range is nonsense rather than a choice, and treating it as
		// "none" is safe: the subject simply gets no entity this time.
		log.Debug().Int64("pick", pick).Int("offered", len(candidates)).
			Msg("llm: entity pick was out of range, treated as none")
		return EntityPick{Reason: strings.TrimSpace(decoded.Reason)}, nil
	}

	confidence, _ := decoded.Confidence.Float64()
	if confidence < 0 {
		confidence = 0
	}
	if confidence > 100 {
		confidence = 100
	}

	return EntityPick{
		QID:        candidates[pick-1].QID,
		Confidence: int(confidence + 0.5),
		Reason:     strings.TrimSpace(decoded.Reason),
	}, nil
}
