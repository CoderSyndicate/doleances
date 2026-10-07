package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/CoderSyndicate/doleances/internal/config"
	"github.com/CoderSyndicate/doleances/internal/models"
	"github.com/danielgtaylor/huma/v2"
	"github.com/rs/zerolog/log"
)

func (a *API) registerLLMRoutes(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "get-llm-settings",
		Method:      http.MethodGet,
		Path:        "/v1/llm",
		Summary:     "Read the LLM service settings",
		Description: "The API key is never returned — only whether one is set.",
		Tags:        []string{"LLM"},
	}, a.getLLMSettings)

	huma.Register(api, huma.Operation{
		OperationID: "save-llm-settings",
		Method:      http.MethodPut,
		Path:        "/v1/llm",
		Summary:     "Update the LLM service settings",
		Description: "An empty api_key leaves the stored key unchanged.",
		Tags:        []string{"LLM"},
	}, a.saveLLMSettings)

	huma.Register(api, huma.Operation{
		OperationID: "clear-llm-key",
		Method:      http.MethodDelete,
		Path:        "/v1/llm/key",
		Summary:     "Remove the stored API key",
		Tags:        []string{"LLM"},
	}, a.clearLLMKey)

	huma.Register(api, huma.Operation{
		OperationID: "test-llm",
		Method:      http.MethodPost,
		Path:        "/v1/llm/test",
		Summary:     "Check the LLM service is reachable",
		Description: "Calls the configured endpoint's model listing and reports what came back.",
		Tags:        []string{"LLM"},
	}, a.testLLM)
}

// LLMSettingsBody is the settings as the console sees them — without the key.
type LLMSettingsBody struct {
	BaseURL             string `json:"base_url"`
	ClassificationModel string `json:"classification_model"`
	TranslationModel    string `json:"translation_model"`
	TimeoutSeconds      int    `json:"timeout_seconds"`
	AcceptThreshold     int    `json:"accept_threshold"`
	CurateThreshold     int    `json:"curate_threshold"`

	// APIKeySet reports whether a credential is stored. The key itself never
	// leaves the backend: an operator can replace it, never read it back.
	APIKeySet bool `json:"api_key_set"`
}

// LLMSettingsOutput returns the settings.
type LLMSettingsOutput struct {
	Body LLMSettingsBody
}

func (a *API) getLLMSettings(ctx context.Context, _ *struct{}) (*LLMSettingsOutput, error) {
	settings, err := a.store.LLMSettings(ctx)
	if err != nil {
		log.Error().Err(err).Msg("cannot read llm settings")
		return nil, huma.Error500InternalServerError("cannot read the LLM settings")
	}

	out := &LLMSettingsOutput{Body: LLMSettingsBody{
		BaseURL:             settings.BaseURL,
		ClassificationModel: settings.ClassificationModel,
		TranslationModel:    settings.TranslationModel,
		TimeoutSeconds:      settings.TimeoutSeconds,
		AcceptThreshold:     settings.AcceptThreshold,
		CurateThreshold:     settings.CurateThreshold,
		APIKeySet:           settings.APIKey != "",
	}}
	return out, nil
}

// SaveLLMSettingsInput carries the edited settings.
type SaveLLMSettingsInput struct {
	// Actor is the identity the change is recorded against. It comes from the
	// console's authenticated session; until OIDC is wired it says so.
	Actor string `header:"X-Actor"`

	Body struct {
		BaseURL             string `json:"base_url"`
		APIKey              string `json:"api_key,omitempty" doc:"Leave empty to keep the stored key"`
		ClassificationModel string `json:"classification_model"`
		TranslationModel    string `json:"translation_model"`
		TimeoutSeconds      int    `json:"timeout_seconds"`
		AcceptThreshold     int    `json:"accept_threshold"`
		CurateThreshold     int    `json:"curate_threshold"`
	}
}

func (a *API) saveLLMSettings(ctx context.Context, in *SaveLLMSettingsInput) (*EmptyOutput, error) {
	settings := models.LLMSettings{
		BaseURL:             strings.TrimSpace(in.Body.BaseURL),
		APIKey:              strings.TrimSpace(in.Body.APIKey),
		ClassificationModel: strings.TrimSpace(in.Body.ClassificationModel),
		TranslationModel:    strings.TrimSpace(in.Body.TranslationModel),
		TimeoutSeconds:      in.Body.TimeoutSeconds,
		AcceptThreshold:     in.Body.AcceptThreshold,
		CurateThreshold:     in.Body.CurateThreshold,
	}
	if err := validateLLMSettings(settings); err != nil {
		return nil, huma.Error422UnprocessableEntity(err.Error())
	}

	if err := a.store.SaveLLMSettings(ctx, settings); err != nil {
		log.Error().Err(err).Msg("cannot save llm settings")
		return nil, huma.Error500InternalServerError("cannot save the LLM settings")
	}

	// Changing where submissions are sent, and the thresholds that decide what
	// is published, is a privileged act: it belongs in the audit log.
	a.recordConfigChange(ctx, in.Actor, models.LLMSettingsID, "llm settings updated")
	log.Info().Str("base_url", settings.BaseURL).Msg("llm settings updated")
	return &EmptyOutput{}, nil
}

// validateLLMSettings rejects a configuration that cannot work, before it is
// stored rather than when the first submission arrives.
func validateLLMSettings(s models.LLMSettings) error {
	if s.BaseURL != "" {
		parsed, err := url.Parse(s.BaseURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("base url must look like https://host")
		}
	}
	if s.TimeoutSeconds < 1 || s.TimeoutSeconds > 600 {
		return fmt.Errorf("timeout must be between 1 and 600 seconds")
	}
	for name, value := range map[string]int{
		"accept threshold": s.AcceptThreshold,
		"curate threshold": s.CurateThreshold,
	} {
		if value < 0 || value > 100 {
			return fmt.Errorf("%s must be between 0 and 100", name)
		}
	}
	if s.AcceptThreshold <= s.CurateThreshold {
		return fmt.Errorf("the accept threshold must be above the curate threshold, or nothing would ever reach a human")
	}
	return nil
}

type clearKeyInput struct {
	Actor string `header:"X-Actor"`
}

func (a *API) clearLLMKey(ctx context.Context, in *clearKeyInput) (*EmptyOutput, error) {
	if err := a.store.ClearLLMAPIKey(ctx); err != nil {
		log.Error().Err(err).Msg("cannot clear llm api key")
		return nil, huma.Error500InternalServerError("cannot clear the API key")
	}
	a.recordConfigChange(ctx, in.Actor, models.LLMSettingsID, "llm api key removed")
	log.Info().Msg("llm api key removed")
	return &EmptyOutput{}, nil
}

// LLMTestOutput reports what the configured endpoint answered.
type LLMTestOutput struct {
	Body struct {
		OK      bool     `json:"ok"`
		Message string   `json:"message"`
		Models  []string `json:"models,omitempty"`
	}
}

// testLLM calls the endpoint's model listing, which is the cheapest request
// that proves the URL, the key and the network path all work.
func (a *API) testLLM(ctx context.Context, _ *struct{}) (*LLMTestOutput, error) {
	out := &LLMTestOutput{}

	settings, err := a.store.LLMSettings(ctx)
	if err != nil {
		log.Error().Err(err).Msg("cannot read llm settings")
		return nil, huma.Error500InternalServerError("cannot read the LLM settings")
	}
	if settings.BaseURL == "" {
		out.Body.Message = "No endpoint is configured yet."
		return out, nil
	}

	timeout := time.Duration(settings.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimSuffix(settings.BaseURL, "/")+"/v1/models", nil)
	if err != nil {
		out.Body.Message = err.Error()
		return out, nil
	}
	if settings.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+settings.APIKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// A failed check is an answer, not a server error: the operator is
		// asking precisely whether this works.
		out.Body.Message = err.Error()
		return out, nil
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		out.Body.Message = "The endpoint answered " + resp.Status
		return out, nil
	}

	var listing struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listing); err != nil {
		out.Body.Message = "The endpoint answered, but not with a model listing."
		return out, nil
	}
	for _, model := range listing.Data {
		out.Body.Models = append(out.Body.Models, model.ID)
	}

	out.Body.OK = true
	out.Body.Message = fmt.Sprintf("Reachable — %d model(s) available.", len(out.Body.Models))
	return out, nil
}

// recordConfigChange appends to the audit log. A failure to record is logged
// but never fails the change itself.
func (a *API) recordConfigChange(ctx context.Context, actor, subject, reason string) {
	if strings.TrimSpace(actor) == "" {
		actor = "unknown"
	}
	entry := models.AuditEntry{
		ID:          models.NewID(),
		CreatedAt:   time.Now(),
		Actor:       actor,
		Action:      models.AuditConfigChange,
		SubjectType: "settings",
		SubjectID:   subject,
		Reason:      reason,
	}
	if err := a.store.DB().WithContext(ctx).Create(&entry).Error; err != nil {
		log.Error().Err(err).Msg("cannot write audit entry")
	}
}

// seedLLMSettings writes the deployment's configuration into the database on
// first start only.
//
// Flags, environment variables and the config file seed; the database is then
// authoritative, because the console is where these are maintained. Re-seeding
// on every start would silently undo an operator's change on the next restart.
func (a *API) seedLLMSettings(ctx context.Context, seed config.LLM) error {
	exists, err := a.store.LLMSettingsExist(ctx)
	if err != nil {
		return err
	}
	if exists {
		log.Debug().Msg("llm settings already stored, configuration not re-applied")
		return nil
	}

	settings := models.LLMSettings{
		Model:               models.Model{ID: models.LLMSettingsID},
		BaseURL:             strings.TrimSpace(seed.BaseURL),
		APIKey:              strings.TrimSpace(seed.APIKey),
		ClassificationModel: strings.TrimSpace(seed.ClassificationModel),
		TranslationModel:    strings.TrimSpace(seed.TranslationModel),
		TimeoutSeconds:      seed.TimeoutSeconds,
		AcceptThreshold:     seed.AcceptThreshold,
		CurateThreshold:     seed.CurateThreshold,
		EmbeddingModel:      strings.TrimSpace(seed.EmbeddingModel),

		SubjectMergeThreshold:       seed.SubjectMergeThreshold,
		SubjectSuggestThreshold:     seed.SubjectSuggestThreshold,
		SubjectSuggestCrossLanguage: seed.SubjectSuggestCrossLanguage,
	}
	if err := validateLLMSettings(settings); err != nil {
		return fmt.Errorf("llm configuration: %w", err)
	}
	if err := a.store.SaveLLMSettings(ctx, settings); err != nil {
		return err
	}

	log.Info().
		Str("base_url", settings.BaseURL).
		Str("model", settings.ClassificationModel).
		Bool("api_key_set", settings.APIKey != "").
		Msg("llm settings seeded from configuration")
	return nil
}
