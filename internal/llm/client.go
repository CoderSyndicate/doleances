// Package llm talks to the model.
//
// The endpoint is OpenAI-compatible and points at a Synergia instance, so this
// is a small hand-written client rather than an SDK: two calls are needed —
// chat completions and embeddings — and a dependency covering the whole API
// surface would be more to keep in step than to write.
//
// Nothing here knows what a doléance is. The questions live beside their
// prompts in assess.go and classify.go, so a change to what the model is asked
// is a change to a prompt file rather than to transport code.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// maxAnswerBytes bounds a reply. An assessment is a small object; anything
// approaching this is a model that has lost the thread, and reading it all
// into memory helps nobody.
const maxAnswerBytes = 256 << 10

// ErrUnavailable means the model could not be reached or refused to answer.
//
// It is distinguished from every other failure because it decides what happens
// to a submission: unreachable leaves it pending for a retry, while an answer
// that cannot be used sends it to a human.
var ErrUnavailable = errors.New("llm: unavailable")

// Config is what the client needs, resolved from the settings row.
type Config struct {
	BaseURL string
	APIKey  string
	Timeout time.Duration
}

// Client calls the model.
type Client struct {
	config Config
	http   *http.Client
}

// New returns a client. A zero timeout gets a sane one rather than none at
// all: a request with no deadline is a goroutine that never comes back.
func New(config Config) *Client {
	if config.Timeout <= 0 {
		config.Timeout = 60 * time.Second
	}
	config.BaseURL = strings.TrimSuffix(strings.TrimSpace(config.BaseURL), "/")

	return &Client{
		config: config,
		http:   &http.Client{Timeout: config.Timeout},
	}
}

// Configured reports whether there is anywhere to send a request. An
// unconfigured client is not an error — it is an installation where every
// submission goes to a human, which the curation queue already handles.
func (c *Client) Configured() bool { return c.config.BaseURL != "" }

// Message is one turn of a conversation.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// completionRequest is the wire format.
type completionRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`

	// Temperature is pinned low by callers: this is classification, and a
	// model free to be creative about a score is a model whose thresholds mean
	// nothing.
	Temperature float64 `json:"temperature"`

	// ResponseFormat asks for a JSON object. The endpoint supports it, which
	// makes the answer parseable far more often — never always, so callers
	// still handle a reply that is not what was asked for.
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type completionResponse struct {
	Choices []struct {
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

// CompleteJSON asks the model a question and returns the raw answer, having
// asked for a JSON object.
//
// The answer is returned as text rather than decoded here, because each caller
// knows the shape it expects and because a caller has to be able to log what
// came back when it is not that shape.
func (c *Client) CompleteJSON(ctx context.Context, model string, messages []Message) (string, error) {
	if !c.Configured() {
		return "", fmt.Errorf("%w: no endpoint configured", ErrUnavailable)
	}

	body, err := json.Marshal(completionRequest{
		Model:          model,
		Messages:       messages,
		Temperature:    0,
		ResponseFormat: &responseFormat{Type: "json_object"},
	})
	if err != nil {
		return "", fmt.Errorf("llm: encode request: %w", err)
	}

	raw, err := c.post(ctx, "/chat/completions", body, model)
	if err != nil {
		return "", err
	}

	var decoded completionResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return "", fmt.Errorf("llm: decode response: %w", err)
	}
	if decoded.Error != nil {
		return "", fmt.Errorf("%w: %s", ErrUnavailable, decoded.Error.Message)
	}
	if len(decoded.Choices) == 0 {
		return "", fmt.Errorf("llm: the model returned no answer")
	}

	log.Debug().
		Str("model", model).
		Int("prompt_tokens", decoded.Usage.PromptTokens).
		Int("completion_tokens", decoded.Usage.CompletionTokens).
		Msg("llm: completion")
	return decoded.Choices[0].Message.Content, nil
}

// post sends a request and returns the raw body.
//
// It stops at the transport: status, timing, tracing and the difference
// between "unreachable" and "answered". Decoding belongs to the caller,
// because a completion and an embedding are different shapes and a transport
// that knew about both would have to guess which it was holding.
func (c *Client) post(ctx context.Context, path string, body []byte, model string) ([]byte, error) {
	endpoint := c.config.BaseURL + path

	// TRACE, not DEBUG: this is the full prompt, which carries the contributor's
	// text. A data dump by the level semantics, and never on in production.
	log.Trace().
		Str("url", endpoint).
		Str("model", model).
		Str("request_body", string(body)).
		Msg("llm: request")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("llm: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.config.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.config.APIKey)
	}

	started := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		log.Warn().Err(err).Str("url", endpoint).Msg("llm: unreachable")
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close() //nolint:errcheck

	answer, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswerBytes))
	if err != nil {
		return nil, fmt.Errorf("%w: read answer: %v", ErrUnavailable, err)
	}

	log.Trace().
		Str("url", endpoint).
		Int("status", resp.StatusCode).
		Dur("took", time.Since(started)).
		Str("response_body", string(answer)).
		Msg("llm: response")

	if resp.StatusCode != http.StatusOK {
		// A 404 or 405 on this path almost always means the base URL is
		// missing its version segment: an OpenAI-compatible endpoint lives at
		// {base}/v1/chat/completions, and the convention is that the version
		// is part of the configured base. Saying so turns a puzzling status
		// into a one-line fix.
		hint := ""
		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
			hint = " (does the base URL need its /v1 suffix?)"
		}

		// Every non-200 is unavailability, including a 400. A refused request
		// is not something a contributor can fix, and leaving the submission
		// for a human beats discarding it over a header.
		return nil, fmt.Errorf("%w: %s answered %s%s",
			ErrUnavailable, path, resp.Status, hint)
	}
	return answer, nil
}
