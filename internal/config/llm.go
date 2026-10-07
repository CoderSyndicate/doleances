package config

import (
	"github.com/CoderSyndicate/doleances/internal/models"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// LLM configuration keys.
//
// These **seed** the database on first start. Once settings exist, the
// database is authoritative and the console is where they are changed — a
// deployment sets the initial values, an operator maintains them.
const (
	KeyLLMBaseURL             = "llm-base-url"
	KeyLLMAPIKey              = "llm-api-key"
	KeyLLMClassificationModel = "llm-classification-model"
	KeyLLMTranslationModel    = "llm-translation-model"
	KeyLLMTimeout             = "llm-timeout-seconds"
	KeyLLMAcceptThreshold     = "llm-accept-threshold"
	KeyLLMCurateThreshold     = "llm-curate-threshold"
	KeyLLMEmbeddingModel      = "llm-embedding-model"
	KeyLLMSubjectMerge        = "llm-subject-merge-threshold"
	KeyLLMSubjectSuggest      = "llm-subject-suggest-threshold"
	KeyLLMSubjectSuggestCross = "llm-subject-suggest-cross-language"
)

// LLM is the seed configuration for the language-model service.
type LLM struct {
	BaseURL             string
	APIKey              string
	ClassificationModel string
	TranslationModel    string
	EmbeddingModel      string

	SubjectMergeThreshold       float64
	SubjectSuggestThreshold     float64
	SubjectSuggestCrossLanguage float64
	TimeoutSeconds              int
	AcceptThreshold             int
	CurateThreshold             int
}

// RegisterLLMFlags declares the seed flags. Only the backend registers them:
// it is the only service that talks to the model.
func RegisterLLMFlags(cmd *cobra.Command) {
	defaults := models.DefaultLLMSettings()

	f := cmd.PersistentFlags()
	f.String(KeyLLMBaseURL, "", "OpenAI-compatible endpoint (a Synergia instance) — seeds the database on first start")
	f.String(KeyLLMAPIKey, "", "API key for that endpoint — seeds the database on first start")
	f.String(KeyLLMClassificationModel, defaults.ClassificationModel, "model used to score and classify submissions")
	f.String(KeyLLMTranslationModel, defaults.TranslationModel, "model used to translate historical texts")
	f.Int(KeyLLMTimeout, defaults.TimeoutSeconds, "seconds a single model call may take")
	f.Int(KeyLLMAcceptThreshold, defaults.AcceptThreshold, "confidence at or above which a submission is published")
	f.Int(KeyLLMCurateThreshold, defaults.CurateThreshold, "confidence at or above which a submission reaches a curator")
	f.String(KeyLLMEmbeddingModel, defaults.EmbeddingModel,
		"multilingual embedding model used to deduplicate subjects")
	f.Float64(KeyLLMSubjectMerge, defaults.SubjectMergeThreshold,
		"cosine at or above which a proposed subject is merged into an existing one")
	f.Float64(KeyLLMSubjectSuggest, defaults.SubjectSuggestThreshold,
		"cosine at or above which a merge is suggested to a curator instead")
	f.Float64(KeyLLMSubjectSuggestCross, defaults.SubjectSuggestCrossLanguage,
		"the same, for two labels written in different languages")
}

// LoadLLM reads the resolved seed configuration.
func LoadLLM() LLM {
	return LLM{
		BaseURL:             viper.GetString(KeyLLMBaseURL),
		APIKey:              viper.GetString(KeyLLMAPIKey),
		ClassificationModel: viper.GetString(KeyLLMClassificationModel),
		TranslationModel:    viper.GetString(KeyLLMTranslationModel),
		TimeoutSeconds:      viper.GetInt(KeyLLMTimeout),
		AcceptThreshold:     viper.GetInt(KeyLLMAcceptThreshold),
		CurateThreshold:     viper.GetInt(KeyLLMCurateThreshold),
		EmbeddingModel:      viper.GetString(KeyLLMEmbeddingModel),

		SubjectMergeThreshold:       viper.GetFloat64(KeyLLMSubjectMerge),
		SubjectSuggestThreshold:     viper.GetFloat64(KeyLLMSubjectSuggest),
		SubjectSuggestCrossLanguage: viper.GetFloat64(KeyLLMSubjectSuggestCross),
	}
}
