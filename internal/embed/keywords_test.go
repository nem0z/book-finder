package embed

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nem0z/book-finder/internal/db"
)

// Set BOOKFINDER_TEST_DATABASE_URL to a disposable database to run the database tests; they empty
// the catalogue.
const testDBEnv = "BOOKFINDER_TEST_DATABASE_URL"

// fakeEmbedder returns a unit vector and records the titles of what it embedded.
type fakeEmbedder struct{ titles []string }

func (f *fakeEmbedder) Model() string { return "fake" }

func (f *fakeEmbedder) EmbedDocuments(_ context.Context, docs []Doc) ([][]float32, error) {
	vecs := make([][]float32, len(docs))
	for i, d := range docs {
		f.titles = append(f.titles, d.Title)
		vecs[i] = make([]float32, Dims)
		vecs[i][0] = 1
	}

	return vecs, nil
}

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv(testDBEnv)
	if url == "" {
		t.Skipf("%s not set", testDBEnv)
	}

	ctx := context.Background()
	pool, err := db.Connect(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(ctx, "TRUNCATE works, work_authors, authors, work_keyword_embeddings"); err != nil {
		t.Fatal(err)
	}

	return pool
}

// TestKeywordHash pins the hash of a keyword vector stored by the run that indexed the catalogue, so that a
// change to it, which would embed every work again, is deliberate.
func TestKeywordHash(t *testing.T) {
	text := "small town; sheriff; cozy mystery; murder mystery; rural county; county commissioner; corruption; " +
		"political corruption; arson; fire; hunting shack; drought; heat wave; hot summer; reporter; journalist; " +
		"local politics; good ol boy; southern; whodunit; police procedural; rural life; suspects; election; " +
		"rural county, United States; political corruption; small-town politics; revenge and motive; justice; " +
		"community secrets; small-town sheriff"
	if got := KeywordHash("embeddinggemma", text); got != "711ccc1920cba80b6216dd6bb486be3d" {
		t.Errorf("hash %s", got)
	}
}

func TestStoreKeywordVectors(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, "INSERT INTO works (id, title) VALUES ('OL1W', 'One'), ('OL2W', 'Two')"); err != nil {
		t.Fatal(err)
	}

	emb := &fakeEmbedder{}
	if err := StoreKeywordVectors(ctx, pool, emb, nil); err != nil || len(emb.titles) != 0 {
		t.Errorf("nothing to store: %v, %d embedded", err, len(emb.titles))
	}

	texts := []KeywordText{{ID: "OL1W", Text: "orc; coffee shop"}, {ID: "OL2W", Text: "insect"}, {ID: "OL3W", Text: "gone"}}
	if err := StoreKeywordVectors(ctx, pool, emb, texts); err != nil {
		t.Fatal(err)
	}

	// Stored again with another text: replaced.
	if err := StoreKeywordVectors(ctx, pool, emb, []KeywordText{{ID: "OL1W", Text: "orc"}}); err != nil {
		t.Fatal(err)
	}

	var n int
	var hash, model string
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM work_keyword_embeddings), content_hash, model
		FROM work_keyword_embeddings WHERE work_id = 'OL1W'`).Scan(&n, &hash, &model); err != nil {
		t.Fatal(err)
	}

	if n != 3 || hash != KeywordHash("fake", "orc") || model != "fake" || len(emb.titles) != 4 {
		t.Errorf("stored %d, hash %s, model %s, %d embedded", n, hash, model, len(emb.titles))
	}

	// OL3W is not in the catalogue.
	if pruned, err := PruneKeywordVectors(ctx, pool); err != nil || pruned != 1 {
		t.Errorf("prune: %d, %v", pruned, err)
	}

	if _, err := pool.Exec(ctx, "DELETE FROM works"); err != nil {
		t.Fatal(err)
	}

	if _, err := PruneKeywordVectors(ctx, pool); !errors.Is(err, ErrEmptyCatalogue) {
		t.Errorf("prune on an empty catalogue: %v", err)
	}
}
