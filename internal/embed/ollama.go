// Package embed turns book text and search queries into vectors and stores them for semantic search.
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Dims must match the halfvec size in migration 003_embeddings.sql.
const Dims = 768

// Doc is the text embedded for one book. Some models (EmbeddingGemma) treat the title separately.
type Doc struct {
	Title string
	Text  string
}

// Embedder converts texts to vectors. Implementations return one vector of Dims floats per input,
// in input order.
type Embedder interface {
	Model() string
	EmbedDocuments(ctx context.Context, docs []Doc) ([][]float32, error)
	EmbedQuery(ctx context.Context, query string) ([]float32, error)
}

const (
	DefaultOllamaURL = "http://localhost:11434"
	DefaultModel     = "embeddinggemma"
)

// Ollama calls a local Ollama server (https://ollama.com) through its /api/embed endpoint.
type Ollama struct {
	BaseURL string
	Name    string
	NumCtx  int // context window requested from Ollama; 0 keeps Ollama's default
	Client  *http.Client

	formatDoc   func(Doc) string
	formatQuery func(string) string
}

// NewOllama returns a client for model, using the prompt formats the model was trained with.
func NewOllama(baseURL, model string) *Ollama {
	o := &Ollama{
		BaseURL:     strings.TrimRight(baseURL, "/"),
		Name:        model,
		Client:      &http.Client{Timeout: 5 * time.Minute},
		formatDoc:   func(d Doc) string { return joinDoc(d) },
		formatQuery: func(q string) string { return q },
	}
	switch {
	case strings.HasPrefix(model, "embeddinggemma"):
		// https://ai.google.dev/gemma/docs/embeddinggemma/model_card#prompt-instructions
		o.NumCtx = 2048
		o.formatDoc = func(d Doc) string {
			title := d.Title
			if title == "" {
				title = "none"
			}
			return "title: " + title + " | text: " + d.Text
		}
		o.formatQuery = func(q string) string { return "task: search result | query: " + q }
	case strings.HasPrefix(model, "nomic-embed-text"):
		o.NumCtx = 8192
		o.formatDoc = func(d Doc) string { return "search_document: " + joinDoc(d) }
		o.formatQuery = func(q string) string { return "search_query: " + q }
	}
	return o
}

func joinDoc(d Doc) string {
	if d.Title == "" {
		return d.Text
	}
	return d.Title + "\n" + d.Text
}

func (o *Ollama) Model() string { return o.Name }

func (o *Ollama) EmbedDocuments(ctx context.Context, docs []Doc) ([][]float32, error) {
	input := make([]string, len(docs))
	for i, d := range docs {
		input[i] = o.formatDoc(d)
	}
	return o.embed(ctx, input)
}

func (o *Ollama) EmbedQuery(ctx context.Context, query string) ([]float32, error) {
	vecs, err := o.embed(ctx, []string{o.formatQuery(query)})
	if err != nil {
		return nil, err
	}
	return vecs[0], nil
}

func (o *Ollama) embed(ctx context.Context, input []string) ([][]float32, error) {
	reqBody := map[string]any{"model": o.Name, "input": input, "truncate": true}
	if o.NumCtx > 0 {
		reqBody["options"] = map[string]any{"num_ctx": o.NumCtx}
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.BaseURL+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama: %w (is Ollama running and the model pulled? `ollama pull %s`)", err, o.Name)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("ollama: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	var out struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("ollama: decode response: %w", err)
	}
	if len(out.Embeddings) != len(input) {
		return nil, fmt.Errorf("ollama: got %d embeddings for %d inputs", len(out.Embeddings), len(input))
	}
	for _, v := range out.Embeddings {
		if len(v) != Dims {
			return nil, fmt.Errorf("ollama: model %s returns %d dimensions, schema expects %d", o.Name, len(v), Dims)
		}
	}
	return out.Embeddings, nil
}

// VectorLiteral formats v as a pgvector text literal ("[0.1,0.2,...]") for use with ::halfvec.
func VectorLiteral(v []float32) string {
	b := make([]byte, 0, len(v)*10)
	b = append(b, '[')
	for i, f := range v {
		if i > 0 {
			b = append(b, ',')
		}
		b = strconv.AppendFloat(b, float64(f), 'g', -1, 32)
	}
	return string(append(b, ']'))
}
