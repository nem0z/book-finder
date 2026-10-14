// Package embed turns book text and search queries into vectors with EmbeddingGemma, served by
// Ollama (https://ollama.com).
package embed

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
)

const (
	// Dims must match the halfvec size in migration 004_embeddings.sql.
	Dims = 768

	DefaultOllamaURL = "http://localhost:11434"
	DefaultModel     = "embeddinggemma"

	// EmbeddingGemma's context window, in tokens; longer inputs are truncated by Ollama.
	contextTokens = 2048

	requestTimeout = 5 * time.Minute
	maxErrorBody   = 1024
)

var ErrBadResponse = errors.New("unexpected Ollama response")

// Doc is the text embedded for one book. EmbeddingGemma takes the title separately.
type Doc struct {
	Title string
	Text  string
}

// Ollama embeds texts through Ollama's /api/embed endpoint, using the prompt formats EmbeddingGemma
// was trained with (https://ai.google.dev/gemma/docs/embeddinggemma/model_card#prompt-instructions).
type Ollama struct {
	baseURL string
	model   string
	client  *http.Client
}

type embedRequest struct {
	Model    string       `json:"model"`
	Input    []string     `json:"input"`
	Truncate bool         `json:"truncate"`
	Options  embedOptions `json:"options"`
}

type embedOptions struct {
	NumCtx int `json:"num_ctx"`
}

type embedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}

func NewOllama(baseURL, model string) *Ollama {
	return &Ollama{
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		client:  &http.Client{Timeout: requestTimeout},
	}
}

func (o *Ollama) Model() string { return o.model }

// EmbedDocuments returns one vector per document, in order.
func (o *Ollama) EmbedDocuments(ctx context.Context, docs []Doc) ([][]float32, error) {
	input := make([]string, len(docs))
	for i, d := range docs {
		title := d.Title
		if title == "" {
			title = "none"
		}

		input[i] = "title: " + title + " | text: " + d.Text
	}

	return o.embed(ctx, input)
}

// EmbedQuery returns the vector of a search query.
func (o *Ollama) EmbedQuery(ctx context.Context, query string) ([]float32, error) {
	vecs, err := o.embed(ctx, []string{"task: search result | query: " + query})
	if err != nil {
		return nil, err
	}

	return vecs[0], nil
}

func (o *Ollama) embed(ctx context.Context, input []string) ([][]float32, error) {
	body, err := json.Marshal(embedRequest{Model: o.model, Input: input, Truncate: true,
		Options: embedOptions{NumCtx: contextTokens}})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama at %s: %w (is it running, with the model pulled?)", o.baseURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return nil, fmt.Errorf("%w: %s: %s", ErrBadResponse, resp.Status, strings.TrimSpace(string(msg)))
	}

	var out embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBadResponse, err)
	}

	if len(out.Embeddings) != len(input) {
		return nil, fmt.Errorf("%w: %d vectors for %d inputs", ErrBadResponse, len(out.Embeddings), len(input))
	}

	for _, v := range out.Embeddings {
		if len(v) != Dims {
			return nil, fmt.Errorf("%w: model %s returns %d dimensions, the schema expects %d", ErrBadResponse, o.model, len(v), Dims)
		}
	}

	return out.Embeddings, nil
}
