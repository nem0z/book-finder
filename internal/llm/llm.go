// Package llm asks chat models for answers in a given JSON shape. Each provider implements Client: Azure
// OpenAI and Ollama through their OpenAI-compatible chat completions API with structured outputs, and
// GitHub Copilot through the Copilot SDK.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// Providers, as named in Config.
const (
	ProviderAzure   = "azure"
	ProviderOllama  = "ollama"
	ProviderCopilot = "copilot"
)

var (
	ErrBadResponse = errors.New("unexpected LLM response")
	ErrConfig      = errors.New("bad LLM settings")
)

// Client asks a chat model.
type Client interface {
	// JSON sends the system and user prompts and decodes the model's answer, a JSON value matching schema,
	// into out. schemaName names the schema for the provider.
	JSON(ctx context.Context, system, user, schemaName string, schema json.RawMessage, out any) error
}

// Config chooses a provider and model. Commands read it from LLM_PROVIDER, LLM_URL, LLM_MODEL and
// LLM_API_KEY.
type Config struct {
	Provider string
	// URL is the API's base URL: https://<resource>.openai.azure.com/openai/v1 for Azure, Ollama's
	// address (http://localhost:11434) for Ollama. Unused by Copilot.
	URL string
	// Model is the model name, or the deployment name on Azure.
	Model string
	// APIKey is the Azure OpenAI key, or the GitHub token for Copilot (empty: the account signed in on this
	// machine).
	APIKey string
}

// New returns the client c describes.
func New(c Config) (Client, error) {
	if c.Model == "" {
		return nil, fmt.Errorf("%w: no model", ErrConfig)
	}

	switch c.Provider {
	case ProviderAzure:
		if c.URL == "" || c.APIKey == "" {
			return nil, fmt.Errorf("%w: Azure OpenAI needs a URL and an API key", ErrConfig)
		}

		return NewAzure(c.URL, c.Model, c.APIKey), nil
	case ProviderOllama:
		if c.URL == "" {
			return nil, fmt.Errorf("%w: Ollama needs a URL", ErrConfig)
		}

		return NewOllama(c.URL, c.Model), nil
	case ProviderCopilot:
		return NewCopilot(c.Model, c.APIKey), nil
	default:
		return nil, fmt.Errorf("%w: unknown provider %q, want %s, %s or %s", ErrConfig, c.Provider, ProviderAzure, ProviderOllama, ProviderCopilot)
	}
}
