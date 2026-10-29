package embed

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
)

const (
	maxTopics = 15
	hashBytes = 16
)

var (
	ErrEmptyCatalogue = errors.New("no works in the catalogue; load it first")

	// genericTopics carry no signal in an all-fiction catalogue.
	genericTopics = map[string]bool{"fiction": true, "fiction, general": true}
)

// Embedder turns texts into vectors of Dims floats. *Ollama implements it; tests use a fake.
type Embedder interface {
	Model() string
	EmbedDocuments(ctx context.Context, docs []Doc) ([][]float32, error)
}

// Topics returns genres then subjects, without duplicates (any case) or generic labels, at most 15.
func Topics(genres, subjects []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, list := range [][]string{genres, subjects} {
		for _, t := range list {
			t = strings.TrimSpace(t)
			k := strings.ToLower(t)
			if k == "" || genericTopics[k] || seen[k] {
				continue
			}

			seen[k] = true
			out = append(out, t)
			if len(out) == maxTopics {
				return out
			}
		}
	}

	return out
}

// ContentHash identifies the model and text a vector was computed from, so that unchanged works are
// not embedded again after a reload.
func ContentHash(model string, d Doc) string {
	h := sha256.Sum256([]byte(model + "\x00" + d.Title + "\x00" + d.Text))
	return hex.EncodeToString(h[:hashBytes])
}

// VectorLiteral formats v as a pgvector text literal ("[0.1,0.2,...]"), cast to halfvec in SQL.
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
