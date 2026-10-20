// Package rerank re-orders search candidates with a cross-encoder: a model that reads the query and
// one book together and scores how well they match. Two back ends: Voyage AI's hosted re-rankers
// (https://docs.voyageai.com/docs/reranker), and a local model served by Hugging Face Text Embeddings
// Inference (https://github.com/huggingface/text-embeddings-inference).
package rerank

import (
	"context"
	"errors"
	"time"
)

const (
	requestTimeout = time.Minute
	maxErrorBody   = 1024
)

var ErrBadResponse = errors.New("unexpected re-ranker response")

// Scorer scores how well each text matches the query, in input order (higher is better). *Voyage and *TEI
// implement it.
type Scorer interface {
	Scores(ctx context.Context, query string, texts []string) ([]float64, error)
}

// Config chooses the re-ranker: Voyage AI when VoyageAPIKey is set, else the TEI server at TEIURL.
type Config struct {
	TEIURL       string
	VoyageAPIKey string
	// VoyageModel defaults to DefaultVoyageModel.
	VoyageModel string
}

// New returns the re-ranker c chooses, and a name for logs (without the API key).
func New(c Config) (Scorer, string) {
	if c.VoyageAPIKey == "" {
		return NewTEI(c.TEIURL), "tei " + c.TEIURL
	}

	model := c.VoyageModel
	if model == "" {
		model = DefaultVoyageModel
	}

	return NewVoyage(c.VoyageAPIKey, model), "voyage " + model
}
