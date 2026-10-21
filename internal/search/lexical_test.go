package search

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/nem0z/book-finder/internal/embed"
)

func TestLexical(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, "TRUNCATE work_keywords"); err != nil {
		t.Fatal(err)
	}

	// Keywords and descriptions differ, so that each search finds its own order.
	books := []struct{ id, keywords, description string }{
		{"OL1W", "insect; transformation; family; salesman", "a family man"},
		{"OL2W", "man; family; city", "an insect in the city"},
		{"OL3W", "man; city; night; love", "a man"},
		{"OL4W", "orc; coffee shop", "an insect, a man and his family"},
	}
	for i, b := range books {
		if _, err := pool.Exec(ctx, `INSERT INTO works (id, title, description, first_publish_year, rating_count, search_tsv)
			VALUES ($1, $1, $2, $3, $4, to_tsvector('english', $2))`, b.id, b.description, 1900+i, 10-i); err != nil {
			t.Fatal(err)
		}

		if _, err := pool.Exec(ctx, `INSERT INTO work_keywords (work_id, model, content_hash, keywords, keywords_tsv)
			VALUES ($1, 'm', 'h', '{}', to_tsvector('english', $2))`, b.id, b.keywords); err != nil {
			t.Fatal(err)
		}

		if _, err := pool.Exec(ctx, "INSERT INTO work_embeddings (work_id, model, content_hash, embedding) VALUES ($1, 'fake', 'h', $2::halfvec)",
			b.id, embed.VectorLiteral(axis(i+1, 1))); err != nil {
			t.Fatal(err)
		}
	}

	ids := func(res []Result, err error) []string {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}

		var out []string
		for _, r := range res {
			if r.LexicalScore <= 0 {
				t.Errorf("%s: score %v", r.ID, r.LexicalScore)
			}

			out = append(out, r.ID)
		}

		return out
	}

	// "insect" is in one keyword list, "man" in two: the rare word counts more. "The" is a stop word.
	query := "the man who became an insect"
	if got := ids(Keywords(ctx, pool, query, Filters{}, 10)); !slices.Equal(got, []string{"OL1W", "OL2W", "OL3W"}) {
		t.Errorf("keywords: %v", got)
	}

	// OL4W matches both words; at an equal match, the shorter text wins (OL3W over OL1W).
	if got := ids(FullText(ctx, pool, query, Filters{}, 10)); !slices.Equal(got, []string{"OL4W", "OL2W", "OL3W", "OL1W"}) {
		t.Errorf("full text: %v", got)
	}

	if got := ids(Keywords(ctx, pool, query, Filters{MinYear: 1901}, 1)); !slices.Equal(got, []string{"OL2W"}) {
		t.Errorf("filter and limit: %v", got)
	}

	if got := ids(Keywords(ctx, pool, "the and of", Filters{}, 10)); len(got) != 0 {
		t.Errorf("stop words only: %v", got)
	}

	if _, err := Keywords(ctx, pool, " ", Filters{}, 10); !errors.Is(err, ErrEmptyQuery) {
		t.Errorf("empty query: %v", err)
	}

	e := &Engine{Pool: pool, Retrieval: RetrieveKeywords, Reranker: &fakeReranker{scores: []float64{0, 0, 1}}}
	if got := ids(e.Search(ctx, query, Filters{}, 1)); !slices.Equal(got, []string{"OL3W"}) {
		t.Errorf("engine with keywords and re-ranking: %v", got)
	}

	e = &Engine{Pool: pool, Embedder: fakeQuery{"fake"}}
	if res, err := e.Search(ctx, query, Filters{}, 1); err != nil || len(res) != 1 || res[0].ID != "OL1W" {
		t.Errorf("engine default (vector): %+v, %v", res, err)
	}

	e.Retrieval = "other"
	if _, err := e.Search(ctx, query, Filters{}, 1); !errors.Is(err, ErrUnknownRetrieval) {
		t.Errorf("unknown retrieval: %v", err)
	}
}
