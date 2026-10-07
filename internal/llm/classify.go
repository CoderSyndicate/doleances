package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/CoderSyndicate/doleances/internal/subjects"
)

// maxSubjectsPerMessage caps what one submission may contribute to the
// vocabulary, whatever the prompt asked for and whatever the model returns.
//
// The cap is here rather than only in the prompt because the prompt is a
// request and this is a rule: a model having a bad day must not be able to
// mint fifteen filters from one doléance.
const maxSubjectsPerMessage = 4

// Classification is the labels and the language they are written in.
type Classification struct {
	// Language is what the model wrote the subjects in, which is the language
	// of the doléance.
	//
	// It comes from the classifier rather than from the page the contributor
	// used, because those differ: somebody writing German on the English
	// interface produces German subjects, and comparing them as English ones
	// applies the wrong similarity threshold. The classifier already knows —
	// it just has to say.
	Language string

	Labels []string
}

// ClassifySubjects names what a doléance is about.
//
// It runs only on submissions that have been accepted, so a classifier outage
// delays filtering rather than publication — which is the whole reason this is
// a separate call from the assessment and not one prompt doing both.
//
// The labels come back as the model wrote them, in the language of the
// submission. Deciding whether a label is one the register already has is the
// caller's job: that is the deduplication, and it needs the database.
func (c *Client) ClassifySubjects(ctx context.Context, model, text string) (Classification, error) {
	system, err := prompts.ReadFile("prompts/classify-subjects.md")
	if err != nil {
		return Classification{}, fmt.Errorf("llm: read prompt: %w", err)
	}

	answer, err := c.CompleteJSON(ctx, model, []Message{
		{Role: "system", Content: string(system)},
		{Role: "user", Content: text},
	})
	if err != nil {
		return Classification{}, err
	}

	classification, err := parseSubjects(answer)
	if err != nil {
		log.Warn().Str("answer", truncate(answer, 400)).
			Msg("llm: subject list could not be read")
		return Classification{}, err
	}

	log.Debug().Str("language", classification.Language).
		Strs("subjects", classification.Labels).Msg("llm: message classified")
	return classification, nil
}

// parseSubjects reads and then polices the model's answer.
//
// Everything after the decode is enforcement rather than parsing: labels are
// validated, trimmed to the cap and deduplicated here, so that no combination
// of prompt drift and model mood can widen what a single submission is allowed
// to add to the vocabulary.
func parseSubjects(answer string) (Classification, error) {
	object := extractJSONObject(answer)
	if object == "" {
		return Classification{}, fmt.Errorf("%w: no JSON object in the reply", ErrUnusableAnswer)
	}

	var decoded struct {
		Language string   `json:"language"`
		Subjects []string `json:"subjects"`
	}
	if err := json.Unmarshal([]byte(object), &decoded); err != nil {
		return Classification{}, fmt.Errorf("%w: %v", ErrUnusableAnswer, err)
	}

	// An empty list is a real answer — the prompt says so — and means the text
	// was too short or too confused to be about anything in particular.
	kept := make([]string, 0, len(decoded.Subjects))
	seen := map[string]bool{}

	for _, label := range decoded.Subjects {
		if err := subjects.Validate(label); err != nil {
			// One bad label does not spoil the rest: a model that returned
			// three good subjects and a sentence should give us the three.
			log.Debug().Err(err).Msg("llm: subject refused")
			continue
		}

		// Two spellings of one thing in a single answer count once, before
		// they ever reach the database.
		key := subjects.MatchKey(label)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true

		kept = append(kept, label)
		if len(kept) == maxSubjectsPerMessage {
			break
		}
	}

	return Classification{
		Language: normaliseLanguage(decoded.Language),
		Labels:   kept,
	}, nil
}

// normaliseLanguage trims a reported code to the two-letter form.
//
// An unrecognisable answer becomes empty rather than being passed on: an empty
// language means "unknown", which makes every comparison cross-language and so
// uses the more cautious threshold. Guessing wrong in the other direction
// would merge subjects on a number chosen for a different question.
func normaliseLanguage(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	if before, _, found := strings.Cut(code, "-"); found {
		code = before
	}
	if len(code) != 2 {
		return ""
	}
	for _, r := range code {
		if r < 'a' || r > 'z' {
			return ""
		}
	}
	return code
}
