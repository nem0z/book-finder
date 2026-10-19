// Package rerank re-orders search candidates with a cross-encoder: a model that reads the query and
// one book together and scores how well they match. It is served locally by Hugging Face Text
// Embeddings Inference (https://github.com/huggingface/text-embeddings-inference).
package rerank

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

const (
	DefaultURL = "http://localhost:8081"

	requestTimeout = time.Minute
	maxErrorBody   = 1024
)

var ErrBadResponse = errors.New("unexpected re-ranker response")

// TEI calls the /rerank endpoint of a Text Embeddings Inference server running a re-ranking model.
type TEI struct {
	baseURL string
	client  *http.Client
}

type rerankRequest struct {
	Query    string   `json:"query"`
	Texts    []string `json:"texts"`
	Truncate bool     `json:"truncate"`
}

type rerankScore struct {
	Index int     `json:"index"`
	Score float64 `json:"score"`
}

func NewTEI(baseURL string) *TEI {
	return &TEI{baseURL: strings.TrimRight(baseURL, "/"), client: &http.Client{Timeout: requestTimeout}}
}

// Scores returns one relevance score per text, in input order (higher is more relevant). Texts longer
// than the model's window are truncated by the server.
func (t *TEI) Scores(ctx context.Context, query string, texts []string) ([]float64, error) {
	body, err := json.Marshal(rerankRequest{Query: query, Texts: texts, Truncate: true})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+"/rerank", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("re-ranker at %s: %w (is it running?)", t.baseURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return nil, fmt.Errorf("%w: %s: %s", ErrBadResponse, resp.Status, strings.TrimSpace(string(msg)))
	}

	var out []rerankScore
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBadResponse, err)
	}

	return scoresInOrder(out, len(texts))
}

// scoresInOrder places the server's scores (sorted by score) back in input order and checks that every
// text got exactly one.
func scoresInOrder(out []rerankScore, n int) ([]float64, error) {
	if len(out) != n {
		return nil, fmt.Errorf("%w: %d scores for %d texts", ErrBadResponse, len(out), n)
	}

	// NaN marks a text without a score yet: JSON cannot encode NaN, so no real score is NaN (whereas 0
	// is possible when the server's sigmoid underflows).
	scores := make([]float64, n)
	for i := range scores {
		scores[i] = math.NaN()
	}

	for _, s := range out {
		if s.Index < 0 || s.Index >= n || !math.IsNaN(scores[s.Index]) {
			return nil, fmt.Errorf("%w: bad or repeated index %d", ErrBadResponse, s.Index)
		}

		scores[s.Index] = s.Score
	}

	return scores, nil
}
