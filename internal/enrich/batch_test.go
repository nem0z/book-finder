package enrich

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeBatchLLM answers single requests like fakeLLM, and batch requests with each book's title as its
// keyword: it leaves out books whose description says SKIP, repeats those that say DUP, and fails the
// whole request when one says BATCHFAIL.
type fakeBatchLLM struct {
	single  fakeLLM
	batches atomic.Int64
}

func (f *fakeBatchLLM) JSON(ctx context.Context, system, user, schemaName string, schema json.RawMessage, out any) error {
	if system == SystemPrompt {
		return f.single.JSON(ctx, system, user, schemaName, schema, out)
	}

	f.batches.Add(1)
	if system != SystemPrompt+batchInstructions || schemaName != BatchSchemaName || string(schema) != string(BatchSchema) {
		return errors.New("unexpected prompt")
	}

	if strings.Contains(user, "BATCHFAIL") {
		return errors.New("bad answer")
	}

	var entries []string
	for _, part := range strings.Split(user, "\n\n---\n\n") {
		lines := strings.SplitN(part, "\n", 3)
		id, title := strings.TrimPrefix(lines[0], "Book "), strings.TrimPrefix(lines[1], "Title: ")
		entry := fmt.Sprintf(`{"id": %q, "keywords": [%q], "setting_places": [], "era_from": 0, "era_to": 0, "era_label": "", "themes": [], "protagonist": ""}`, id, title)
		switch {
		case strings.Contains(part, "SKIP"):
		case strings.Contains(part, "DUP"):
			entries = append(entries, entry, entry)
		default:
			entries = append(entries, entry)
		}
	}

	return json.Unmarshal([]byte(`{"books": [`+strings.Join(entries, ",")+`]}`), out)
}

func TestExtractBatch(t *testing.T) {
	ctx := context.Background()
	books := []Book{{ID: "OL1W", Title: "One", Description: "d"}, {ID: "OL2W", Title: "Two", Description: "SKIP"}}
	llm := &fakeBatchLLM{}

	ks, err := ExtractBatch(ctx, llm, books)
	if err != nil || len(ks) != 1 || ks["OL1W"].Keywords[0] != "One" {
		t.Errorf("missing book: %+v, %v", ks, err)
	}

	books[1].Description = "DUP"
	if _, err := ExtractBatch(ctx, llm, books); !errors.Is(err, ErrMixedUp) {
		t.Errorf("repeated book: %v", err)
	}

	if ks, err := ExtractBatch(ctx, llm, books[:1]); err != nil || ks["OL1W"].Keywords[0] != "One" || llm.batches.Load() != 2 {
		t.Errorf("single book: %+v, %v, %d batches", ks, err, llm.batches.Load())
	}

	if msg := BatchMessage(books); !strings.HasPrefix(msg, "Book OL1W\nTitle: One\n") || !strings.Contains(msg, "\n\n---\n\nBook OL2W\nTitle: Two\n") {
		t.Errorf("message: %q", msg)
	}
}

func TestRunPerRequest(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	insertBooks(t, pool, "one", "SKIP", "three", "DUP", "five", "BATCHFAIL", "FAIL")

	llm := &fakeBatchLLM{}
	stats, err := Run(ctx, pool, llm, "m", Options{PerRequest: 3, Concurrency: 2, ProgressInterval: -1})
	if err != nil {
		t.Fatal(err)
	}

	// (one, SKIP, three) misses SKIP, asked again alone. (DUP, five, BATCHFAIL) fails, so its books are asked
	// one by one, and BATCHFAIL fails alone too. (FAIL) is a single request, which fails.
	if stats.Extracted != 5 || stats.Failed != 2 || llm.batches.Load() != 2 || llm.single.calls.Load() != 5 {
		t.Errorf("stats %+v, %d batches, %d single calls", stats, llm.batches.Load(), llm.single.calls.Load())
	}
}
