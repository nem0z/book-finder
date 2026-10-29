package embed

import (
	"context"
	"errors"
	"testing"
)

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
	if _, err := pool.Exec(ctx, "TRUNCATE work_keyword_embeddings"); err != nil {
		t.Fatal(err)
	}

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
