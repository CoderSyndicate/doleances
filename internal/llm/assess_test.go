package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// modelStub answers like an OpenAI-compatible endpoint, returning whatever
// content a test asks for.
func modelStub(t *testing.T, content string, status int) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("Authorization = %q, want the configured key",
				r.Header.Get("Authorization"))
		}

		var request completionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		// Classification with a free-running temperature makes the thresholds
		// mean nothing from one call to the next.
		if request.Temperature != 0 {
			t.Errorf("temperature = %v, want 0", request.Temperature)
		}
		if request.ResponseFormat == nil || request.ResponseFormat.Type != "json_object" {
			t.Error("the request did not ask for a JSON object")
		}
		// The submission must be its own turn, not spliced into the prompt.
		if len(request.Messages) != 2 || request.Messages[1].Role != "user" {
			t.Errorf("messages = %+v, want a system prompt and a user turn", request.Messages)
		}

		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(completionResponse{
			Choices: []struct {
				Message      Message `json:"message"`
				FinishReason string  `json:"finish_reason"`
			}{{Message: Message{Role: "assistant", Content: content}}},
		})
	}))
	t.Cleanup(server.Close)
	return server
}

func testClient(t *testing.T, content string, status int) *Client {
	t.Helper()
	server := modelStub(t, content, status)
	return New(Config{BaseURL: server.URL, APIKey: "test-key", Timeout: 5 * time.Second})
}

func TestAssessMessage(t *testing.T) {
	client := testClient(t, `{"score": 94, "reason": "Doléance claire sur un service public."}`, http.StatusOK)

	assessment, err := client.AssessMessage(context.Background(), "test-model",
		"La maternité de mon canton a fermé.")
	if err != nil {
		t.Fatalf("AssessMessage: %v", err)
	}
	if assessment.Score != 94 {
		t.Errorf("score = %d, want 94", assessment.Score)
	}
	if assessment.Reason == "" {
		t.Error("no reason was carried through for the curator")
	}
}

// TestAssessMessageReadsAwkwardAnswers: JSON mode makes a clean object usual,
// not guaranteed. Being strict here costs a curator's time on submissions that
// did not need a human.
func TestAssessMessageReadsAwkwardAnswers(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    int
	}{
		{"plain", `{"score":80,"reason":"ok"}`, 80},
		{"fenced", "```json\n{\"score\": 80, \"reason\": \"ok\"}\n```", 80},
		{"fenced without a language", "```\n{\"score\": 80}\n```", 80},
		{"prose first", `Here is my answer: {"score": 80, "reason": "ok"}`, 80},
		{"prose after", `{"score": 80, "reason": "ok"} — I hope that helps.`, 80},
		{"score as a string", `{"score":"80","reason":"ok"}`, 80},
		{"score as a float", `{"score":79.6,"reason":"ok"}`, 80},
		{"nested object", `{"score":80,"detail":{"a":{"b":1}},"reason":"ok"}`, 80},
		{"braces inside a string", `{"score":80,"reason":"it said {weird}"}`, 80},
		{"extra fields", `{"score":80,"reason":"ok","confidence":"high"}`, 80},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := testClient(t, tt.content, http.StatusOK)
			assessment, err := client.AssessMessage(context.Background(), "m", "texte")
			if err != nil {
				t.Fatalf("AssessMessage: %v", err)
			}
			if assessment.Score != tt.want {
				t.Errorf("score = %d, want %d", assessment.Score, tt.want)
			}
		})
	}
}

// TestUnusableAnswersAreDistinguished is the rule that keeps a doléance from
// being lost: an answer that cannot be read is reported as unusable, which
// sends the submission to a human rather than into a retry loop.
func TestUnusableAnswersAreDistinguished(t *testing.T) {
	for _, tt := range []struct {
		name    string
		content string
	}{
		{"prose only", "I think this is probably a genuine grievance."},
		{"empty", ""},
		{"no score", `{"reason":"ok"}`},
		{"score out of range", `{"score":140}`},
		{"negative score", `{"score":-5}`},
		{"score not a number", `{"score":"very high"}`},
		{"unclosed object", `{"score": 80, "reason": "ok"`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := testClient(t, tt.content, http.StatusOK)

			_, err := client.AssessMessage(context.Background(), "m", "texte")
			if !errors.Is(err, ErrUnusableAnswer) {
				t.Errorf("err = %v, want ErrUnusableAnswer", err)
			}
			// Unusable must never be reported as unavailable: the caller
			// treats those completely differently.
			if errors.Is(err, ErrUnavailable) {
				t.Error("an unusable answer was reported as unavailability, which would retry it forever")
			}
		})
	}
}

// TestUnreachableIsUnavailable: a model that cannot be reached leaves the
// submission pending for a retry, so it has to be told apart from a bad answer.
func TestUnreachableIsUnavailable(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
	}{
		{"rate limited", http.StatusTooManyRequests},
		{"server error", http.StatusInternalServerError},
		{"bad request", http.StatusBadRequest},
		{"unauthorised", http.StatusUnauthorized},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := testClient(t, "", tt.status)

			_, err := client.AssessMessage(context.Background(), "m", "texte")
			if !errors.Is(err, ErrUnavailable) {
				t.Errorf("err = %v, want ErrUnavailable", err)
			}
		})
	}

	// And an endpoint that is simply not there.
	client := New(Config{BaseURL: "http://127.0.0.1:1", APIKey: "k", Timeout: time.Second})
	if _, err := client.AssessMessage(context.Background(), "m", "t"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
}

// TestUnconfiguredIsUnavailable: an installation with no endpoint is not
// broken, it is one where every submission goes to a human.
func TestUnconfiguredIsUnavailable(t *testing.T) {
	client := New(Config{})
	if client.Configured() {
		t.Error("a client with no base URL reports itself configured")
	}
	if _, err := client.AssessMessage(context.Background(), "m", "t"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
}

func TestExtractJSONObject(t *testing.T) {
	tests := map[string]string{
		`{"a":1}`:                     `{"a":1}`,
		"```json\n{\"a\":1}\n```":     `{"a":1}`,
		`prefix {"a":{"b":2}} suffix`: `{"a":{"b":2}}`,
		`{"a":"}"}`:                   `{"a":"}"}`,
		`{"a":"\""}`:                  `{"a":"\""}`,
		`no object here`:              "",
		`{"unclosed":`:                "",
	}
	for answer, want := range tests {
		if got := extractJSONObject(answer); got != want {
			t.Errorf("extractJSONObject(%q) = %q, want %q", answer, got, want)
		}
	}
}

// TestPromptSaysWhatMatters guards the parts of the prompt that are policy
// rather than wording. They have been argued for; a later tidy-up should not
// quietly drop them.
func TestPromptSaysWhatMatters(t *testing.T) {
	prompt, err := prompts.ReadFile("prompts/assess-message.md")
	if err != nil {
		t.Fatalf("read prompt: %v", err)
	}
	text := strings.ToLower(string(prompt))

	for _, phrase := range []string{
		"spelling",          // never a reason to score lower
		"length",            // one line is as valid as ten pages
		"political",         // a view you disagree with is not a lower score
		"when you hesitate", // the middle band is the safe answer
	} {
		if !strings.Contains(text, phrase) {
			t.Errorf("the assessment prompt no longer mentions %q", phrase)
		}
	}
}

// TestParseAssessmentReadsTheRefusal covers the second field: a submission can
// be a genuine doléance and still be unpublishable, and one number could never
// say both.
func TestParseAssessmentReadsTheRefusal(t *testing.T) {
	tests := []struct {
		name   string
		answer string
		score  int
		refuse string
	}{
		{
			name:   "genuine and unpublishable at once",
			answer: `{"score": 95, "refuse": "contact", "reason": "donne son numéro"}`,
			score:  95,
			refuse: RefuseContact,
		},
		{
			name:   "a threat scores as the doléance it is",
			answer: `{"score": 85, "refuse": "threat", "reason": "menace"}`,
			score:  85,
			refuse: RefuseThreat,
		},
		{
			name:   "nothing to refuse",
			answer: `{"score": 95, "refuse": "none", "reason": "claire"}`,
			score:  95,
			refuse: RefuseNone,
		},
		{
			// The field did not exist until now, and an older or terser model
			// simply omits it. That must read as "nothing to refuse" rather
			// than failing the assessment.
			name:   "field absent",
			answer: `{"score": 95, "reason": "claire"}`,
			score:  95,
			refuse: RefuseNone,
		},
		{
			// A model inventing a category must not be able to discard
			// somebody's doléance on a word nobody defined.
			name:   "invented category",
			answer: `{"score": 95, "refuse": "impolite", "reason": "brusque"}`,
			score:  95,
			refuse: RefuseNone,
		},
		{
			name:   "case and spacing",
			answer: `{"score": 85, "refuse": "  Threat ", "reason": "menace"}`,
			score:  85,
			refuse: RefuseThreat,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseAssessment(test.answer)
			if err != nil {
				t.Fatalf("parseAssessment: %v", err)
			}
			if got.Score != test.score {
				t.Errorf("score = %d, want %d", got.Score, test.score)
			}
			if got.Refuse != test.refuse {
				t.Errorf("refuse = %q, want %q", got.Refuse, test.refuse)
			}
			if got.Refused() != (test.refuse != RefuseNone) {
				t.Errorf("Refused() = %v for %q", got.Refused(), got.Refuse)
			}
		})
	}
}

// TestParseAssessmentReadsTheLanguage: assessment reports it because
// classification only runs on what was published, so a queued or dropped
// submission would otherwise have no language — and an unknown language skips
// the pre-filled alias layer entirely.
func TestParseAssessmentReadsTheLanguage(t *testing.T) {
	tests := []struct {
		answer string
		want   string
	}{
		{`{"score": 95, "language": "fr", "reason": "claire"}`, "fr"},
		{`{"score": 95, "language": "DE", "reason": "klar"}`, "de"},
		{`{"score": 95, "language": "fr-BE", "reason": "claire"}`, "fr"},
		// Unknown stays unknown. A guess would send the text to the wrong
		// similarity threshold and the wrong alias language.
		{`{"score": 95, "language": "", "reason": "claire"}`, ""},
		{`{"score": 95, "reason": "claire"}`, ""},
		{`{"score": 95, "language": "français", "reason": "claire"}`, ""},
		{`{"score": 95, "language": "xx1", "reason": "claire"}`, ""},
	}

	for _, test := range tests {
		got, err := parseAssessment(test.answer)
		if err != nil {
			t.Fatalf("parseAssessment(%s): %v", test.answer, err)
		}
		if got.Language != test.want {
			t.Errorf("language = %q, want %q, from %s", got.Language, test.want, test.answer)
		}
	}
}
