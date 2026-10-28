package search

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/nem0z/book-finder/internal/embed"
)

func TestRetrieval(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, "TRUNCATE work_keyword_embeddings"); err != nil {
		t.Fatal(err)
	}

	// Description vectors: OL1W's points the query's way, then OL2W's, OL3W's and OL4W's.
	for i, id := range []string{"OL1W", "OL2W", "OL3W", "OL4W"} {
		if _, err := pool.Exec(ctx, "INSERT INTO works (id, title, description, first_publish_year) VALUES ($1, $1, '', $2)", id, 1900+i); err != nil {
			t.Fatal(err)
		}

		if _, err := pool.Exec(ctx, "INSERT INTO work_embeddings (work_id, model, content_hash, embedding) VALUES ($1, 'fake', 'h', $2::halfvec)",
			id, embed.VectorLiteral(axis(1, 1-0.4*float32(i)))); err != nil {
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
			out = append(out, r.ID)
		}

		return out
	}

	query := "the man who became an insect"
	e := &Engine{Pool: pool, Embedder: fakeQuery{"fake"}}
	if got := ids(e.Retrieve(ctx, query, Filters{}, 2)); !slices.Equal(got, []string{"OL1W", "OL2W"}) {
		t.Errorf("default without keyword vectors (description vectors): %v", got)
	}

	// Keyword vectors: OL3W's points the query's way, OL2W's half-way; OL1W and OL4W have none.
	for id, v := range map[string][]float32{"OL3W": axis(1, 1), "OL2W": axis(1, 0.5)} {
		if _, err := pool.Exec(ctx, "INSERT INTO work_keyword_embeddings (work_id, model, content_hash, embedding) VALUES ($1, 'fake', 'h', $2::halfvec)",
			id, embed.VectorLiteral(v)); err != nil {
			t.Fatal(err)
		}
	}

	e.Retrieval = RetrieveKeywordVectors
	res, err := e.Retrieve(ctx, query, Filters{}, 10)
	if got := ids(res, err); !slices.Equal(got, []string{"OL3W", "OL2W"}) || res[0].Similarity <= res[1].Similarity {
		t.Errorf("keyword vectors: %+v", res)
	}

	e.Retrieval = RetrieveVector
	if got := ids(e.Retrieve(ctx, query, Filters{}, 10)); !slices.Equal(got, []string{"OL1W", "OL2W", "OL3W", "OL4W"}) {
		t.Errorf("description vectors: %v", got)
	}

	// The default: keyword vectors, and works without one by their description vector, by similarity.
	e.Retrieval = ""
	if got := ids(e.Retrieve(ctx, query, Filters{}, 10)); !slices.Equal(got, []string{"OL3W", "OL1W", "OL2W", "OL4W"}) {
		t.Errorf("keyword vectors first: %v", got)
	}

	if got := ids(e.Retrieve(ctx, query, Filters{}, 2)); !slices.Equal(got, []string{"OL3W", "OL1W"}) {
		t.Errorf("keyword vectors first, limit: %v", got)
	}

	if got := ids(e.Retrieve(ctx, query, Filters{MinYear: 1902}, 10)); !slices.Equal(got, []string{"OL3W", "OL4W"}) {
		t.Errorf("keyword vectors first, filter: %v", got)
	}

	e.Reranker = &fakeReranker{scores: []float64{0, 0, 0, 1}}
	if got := ids(e.Search(ctx, query, Filters{}, 1)); !slices.Equal(got, []string{"OL4W"}) {
		t.Errorf("re-ranked: %v", got)
	}

	e.Retrieval = "other"
	if _, err := e.Search(ctx, query, Filters{}, 1); !errors.Is(err, ErrUnknownRetrieval) {
		t.Errorf("unknown retrieval: %v", err)
	}
}
