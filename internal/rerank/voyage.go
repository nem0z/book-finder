package rerank

import (
	"context"
	"net/http"
)

const (
	VoyageURL = "https://api.voyageai.com/v1"

	// DefaultVoyageModel is Voyage's recommended re-ranker. rerank-3-lite is faster and cheaper.
	DefaultVoyageModel = "rerank-3"
)

// Voyage calls the hosted re-rankers of Voyage AI. Billed per token: the query's tokens times the number
// of texts, plus the texts' tokens.
type Voyage struct {
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
}

type voyageRequest struct {
	Query      string   `json:"query"`
	Documents  []string `json:"documents"`
	Model      string   `json:"model"`
	Truncation bool     `json:"truncation"`
}

type voyageResponse struct {
	Data []voyageScore `json:"data"`
}

type voyageScore struct {
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
}

func NewVoyage(apiKey, model string) *Voyage {
	return &Voyage{baseURL: VoyageURL, apiKey: apiKey, model: model, client: &http.Client{Timeout: requestTimeout}}
}

// Scores returns one relevance score per text, in input order (higher is more relevant). Texts longer
// than the model's window are truncated by Voyage.
func (v *Voyage) Scores(ctx context.Context, query string, texts []string) ([]float64, error) {
	var out voyageResponse
	req := voyageRequest{Query: query, Documents: texts, Model: v.model, Truncation: true}
	header := http.Header{"Authorization": {"Bearer " + v.apiKey}}
	if err := postJSON(ctx, v.client, v.baseURL+"/rerank", header, req, &out); err != nil {
		return nil, err
	}

	scores := make([]rerankScore, len(out.Data))
	for i, d := range out.Data {
		scores[i] = rerankScore{Index: d.Index, Score: d.RelevanceScore}
	}

	return scoresInOrder(scores, len(texts))
}
