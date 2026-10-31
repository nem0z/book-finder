package bench

import (
	"encoding/json"
	"io"

	"github.com/nem0z/book-finder/internal/search"
)

// queryResults is one query's pool and final top 10, for comparing runs.
type queryResults struct {
	Query    string      `json:"query"`
	Category string      `json:"category"`
	Length   string      `json:"length"`
	Pool     []ratedWork `json:"pool"`
	Top10    []ratedWork `json:"top10"`
	NDCG10   float64     `json:"ndcg10"` // -1 when no work is rated above 0
}

// ratedWork is a result and its rating, -1 when not rated.
type ratedWork struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Rating int    `json:"rating"`
}

// WriteJSON writes every query's pool, in retrieval order, and final top 10 with their ratings.
func (ev *Eval) WriteJSON(w io.Writer) error {
	out := make([]queryResults, len(ev.Queries))
	for qi, q := range ev.Queries {
		list, final := ev.Retrieved[qi], ev.final(qi)
		out[qi] = queryResults{
			Query: q.Query, Category: q.Category, Length: q.Length,
			Pool:   ev.rated(qi, list[:min(ev.Pool, len(list))]),
			Top10:  ev.rated(qi, final[:min(10, len(final))]),
			NDCG10: ev.ndcg10(ev.final, qi),
		}
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")

	return enc.Encode(out)
}

func (ev *Eval) rated(qi int, list []search.Result) []ratedWork {
	out := make([]ratedWork, len(list))
	for i, w := range list {
		x, ok := ev.rating(qi, w)
		if !ok {
			x = -1
		}

		out[i] = ratedWork{ID: w.ID, Title: w.Title, Rating: x}
	}

	return out
}
