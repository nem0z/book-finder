package enrich

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// fakeLLM answers with one keyword per call, the work's title, or fails when the description says so.
type fakeLLM struct{ calls atomic.Int64 }

func (f *fakeLLM) JSON(_ context.Context, system, user, schemaName string, schema json.RawMessage, out any) error {
	f.calls.Add(1)
	if system != SystemPrompt || schemaName != SchemaName || string(schema) != string(Schema) {
		return errors.New("unexpected prompt")
	}

	if strings.Contains(user, "FAIL") {
		return errors.New("bad answer")
	}

	title := strings.TrimPrefix(strings.SplitN(user, "\n", 2)[0], "Title: ")
	return json.Unmarshal(fmt.Appendf(nil, `{"keywords": [%q], "setting_places": [], "era_from": 0, "era_to": 0,
		"era_label": "", "themes": [], "protagonist": ""}`, title), out)
}

func insertBooks(t *testing.T, pool *pgxpool.Pool, descriptions ...string) {
	t.Helper()
	ctx := context.Background()
	vec := "[" + strings.Repeat("0,", 767) + "1]"
	for i, d := range descriptions {
		id := fmt.Sprintf("OL%dW", i+1)
		if _, err := pool.Exec(ctx, "INSERT INTO works (id, title, description, rating_count) VALUES ($1, $2, $3, $4)",
			id, "Title "+id, d, 100-i); err != nil {
			t.Fatal(err)
		}

		if _, err := pool.Exec(ctx, "INSERT INTO work_embeddings (work_id, model, content_hash, embedding) VALUES ($1, 'e', 'h', $2::halfvec)",
			id, vec); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRun(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	insertBooks(t, pool, "one", "two", "FAIL", "four")

	llm := &fakeLLM{}
	opts := Options{Concurrency: 2, PerMinute: 6000, ProgressInterval: -1}
	stats, err := Run(ctx, pool, llm, "m", Options{Limit: 2, Concurrency: 2, ProgressInterval: -1})
	if err != nil || stats.Considered != 2 || stats.Extracted != 2 {
		t.Fatalf("limit: %+v, %v", stats, err)
	}

	stats, err = Run(ctx, pool, llm, "m", opts)
	if err != nil || stats != (Stats{Considered: 4, Extracted: 1, UpToDate: 2, Failed: 1}) {
		t.Fatalf("second run: %+v, %v", stats, err)
	}

	var kw string
	if err := pool.QueryRow(ctx, "SELECT keywords[1] FROM work_keywords WHERE work_id = 'OL4W'").Scan(&kw); err != nil || kw != "title ol4w" {
		t.Errorf("stored keyword: %q, %v", kw, err)
	}

	calls := llm.calls.Load()
	if stats, err = Run(ctx, pool, llm, "m", Options{IDs: []string{"OL1W", "OL3W"}, ProgressInterval: -1}); err != nil ||
		stats.Considered != 2 || stats.UpToDate != 1 || llm.calls.Load() != calls+1 {
		t.Errorf("by id: %+v, %v", stats, err)
	}
}

func TestRunTooManyFailures(t *testing.T) {
	pool := testPool(t)
	descs := make([]string, 12)
	for i := range descs {
		descs[i] = "FAIL"
	}
	insertBooks(t, pool, descs...)

	if _, err := Run(context.Background(), pool, &fakeLLM{}, "m", Options{ProgressInterval: -1}); !errors.Is(err, ErrTooManyFailures) {
		t.Errorf("got %v", err)
	}
}
