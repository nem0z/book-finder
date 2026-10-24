package embed

import (
	"context"
	"testing"
)

// docEmbedder returns a unit vector and records the texts it embedded.
type docEmbedder struct{ texts []string }

func (f *docEmbedder) Model() string { return "fake" }

func (f *docEmbedder) EmbedDocuments(_ context.Context, docs []Doc) ([][]float32, error) {
	vecs := make([][]float32, len(docs))
	for i, d := range docs {
		f.texts = append(f.texts, d.Title+"|"+d.Text)
		vecs[i] = make([]float32, Dims)
		vecs[i][0] = 1
	}

	return vecs, nil
}

func TestRunKeywords(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, "TRUNCATE work_keywords, work_keyword_embeddings"); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(ctx, `INSERT INTO work_keywords (work_id, model, content_hash, keywords, setting_places, era_label, themes, protagonist, keywords_tsv) VALUES
		('OL1W', 'm', 'h', '{orc,"coffee shop"}', '{Thune}', 'invented world', '{"found family"}', '', ''),
		('OL2W', 'm', 'h', '{insect}', '{}', '', '{}', 'salesman', '')`); err != nil {
		t.Fatal(err)
	}

	emb := &docEmbedder{}
	stats, err := RunKeywords(ctx, pool, emb, Options{})
	if err != nil || stats.Embedded != 2 || stats.Considered != 2 {
		t.Fatalf("first run: %+v, %v", stats, err)
	}

	if len(emb.texts) != 2 || emb.texts[0] != "|orc; coffee shop; Thune; found family; invented world" || emb.texts[1] != "|insect; salesman" {
		t.Errorf("texts: %q", emb.texts)
	}

	if _, err := pool.Exec(ctx, "UPDATE work_keywords SET keywords = '{beetle}' WHERE work_id = 'OL2W'; DELETE FROM work_keywords WHERE work_id = 'OL1W'"); err != nil {
		t.Fatal(err)
	}

	stats, err = RunKeywords(ctx, pool, emb, Options{})
	if err != nil || stats != (Stats{Considered: 1, Embedded: 1, Pruned: 1}) {
		t.Errorf("second run: %+v, %v", stats, err)
	}

	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM work_keyword_embeddings").Scan(&n); err != nil || n != 1 {
		t.Errorf("rows: %d, %v", n, err)
	}
}
