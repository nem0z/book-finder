// Package bench measures how relevant the search's results are: an LLM rates every result of a set of
// queries in which readers ask for a kind of book, as a recommendation for that reader.
package bench

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

//go:embed queries.json
var builtinQueries []byte

var ErrNoQueries = errors.New("no queries")

// Query is a reader asking for a kind of book. Category is what the reader asks about (plot, vibe...),
// Length its length (S, M, L, XL). Flag notes a known catalogue limit that may sink the query whatever the
// search.
type Query struct {
	Query    string `json:"query"`
	Category string `json:"category"`
	Length   string `json:"length"`
	Flag     string `json:"flag,omitempty"`
}

// LoadQueries reads queries from a JSON file, or returns the built-in ones when path is empty.
func LoadQueries(path string) ([]Query, error) {
	data := builtinQueries
	if path != "" {
		var err error
		if data, err = os.ReadFile(path); err != nil {
			return nil, err
		}
	}

	var qs []Query
	if err := json.Unmarshal(data, &qs); err != nil {
		return nil, fmt.Errorf("queries: %w", err)
	}

	if len(qs) == 0 {
		return nil, ErrNoQueries
	}

	return qs, nil
}
