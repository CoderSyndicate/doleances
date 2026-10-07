package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/rs/zerolog/log"
)

// embeddingRequest is the wire format. The input is a list because a doléance
// yields several subjects and they are embedded together: one request per
// message rather than one per subject.
type embeddingRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embeddingResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Embed turns labels into vectors, in the order they were given.
//
// This is the last and only paid layer of subject deduplication, reached solely
// by labels that the match key and plural folding both failed to recognise. It
// converges: busy on the first day, near-silent by the second week, because by
// then most of what a model proposes is already in the vocabulary.
//
// The model is multilingual (bge-m3), which is what makes the layer worth
// having at all — "santé", "Gesundheit" and "health" land close together, and
// no amount of normalising would ever have discovered that.
func (c *Client) Embed(ctx context.Context, model string, inputs []string) ([][]float32, error) {
	if !c.Configured() {
		return nil, fmt.Errorf("%w: no endpoint configured", ErrUnavailable)
	}
	if len(inputs) == 0 {
		return nil, nil
	}

	body, err := json.Marshal(embeddingRequest{Model: model, Input: inputs})
	if err != nil {
		return nil, fmt.Errorf("llm: encode embedding request: %w", err)
	}

	raw, err := c.post(ctx, "/embeddings", body, model)
	if err != nil {
		return nil, err
	}

	var decoded embeddingResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("llm: decode embeddings: %w", err)
	}
	if decoded.Error != nil {
		return nil, fmt.Errorf("%w: %s", ErrUnavailable, decoded.Error.Message)
	}
	if len(decoded.Data) != len(inputs) {
		return nil, fmt.Errorf("llm: asked for %d embeddings, got %d",
			len(inputs), len(decoded.Data))
	}

	// The API is not required to answer in order, and each entry carries its
	// index precisely because it might not. Sorting is what keeps a vector
	// attached to the label it belongs to — getting this wrong would merge
	// subjects at random and look like a model problem.
	sort.Slice(decoded.Data, func(i, j int) bool {
		return decoded.Data[i].Index < decoded.Data[j].Index
	})

	vectors := make([][]float32, len(inputs))
	for i, entry := range decoded.Data {
		if len(entry.Embedding) == 0 {
			return nil, fmt.Errorf("llm: embedding %d is empty", i)
		}
		vectors[i] = entry.Embedding
	}

	log.Debug().Str("model", model).Int("inputs", len(inputs)).
		Int("dimensions", len(vectors[0])).Msg("llm: embedded")
	return vectors, nil
}
