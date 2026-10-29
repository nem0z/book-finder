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

// fakeLLM answers with each book's title as its keyword. In a request for several books, it leaves out
// books whose description says SKIP, repeats those saying DUP, and fails the request when one says
// BATCHFAIL. Books saying EMPTY get no keywords, NEVER fail every request, and ONCE are left out of the first
// request for several books only.
type fakeLLM struct {
	singles, batches atomic.Int64
	once             atomic.Bool
}

func (f *fakeLLM) JSON(_ context.Context, system, user, schemaName string, schema json.RawMessage, out any) error {
	entry := func(id, part string) string {
		title, _, _ := strings.Cut(strings.SplitN(part, "Title: ", 2)[1], "\n")
		kw := fmt.Sprintf("%q", title)
		if strings.Contains(part, "EMPTY") {
			kw = ""
		}

		return fmt.Sprintf(`{"id": %q, "keywords": [%s], "setting_places": [], "era_from": 0, "era_to": 0, "era_label": "", "themes": [], "protagonist": ""}`, id, kw)
	}

	if system == SystemPrompt && schemaName == SchemaName && string(schema) == string(Schema) {
		f.singles.Add(1)
		if strings.Contains(user, "NEVER") {
			return errors.New("bad answer")
		}

		return json.Unmarshal([]byte(entry("", user)), out)
	}

	f.batches.Add(1)
	if system != SystemPrompt+batchInstructions || schemaName != BatchSchemaName || string(schema) != string(BatchSchema) {
		return errors.New("unexpected prompt")
	}

	if strings.Contains(user, "BATCHFAIL") {
		return errors.New("bad answer")
	}

	first := f.once.CompareAndSwap(false, true)
	var entries []string
	for _, part := range strings.Split(user, "\n\n---\n\n") {
		id, _, _ := strings.Cut(strings.TrimPrefix(part, "Book "), "\n")
		switch {
		case strings.Contains(part, "SKIP"), strings.Contains(part, "NEVER"), first && strings.Contains(part, "ONCE"):
		case strings.Contains(part, "DUP"):
			entries = append(entries, entry(id, part), entry(id, part))
		default:
			entries = append(entries, entry(id, part))
		}
	}

	return json.Unmarshal([]byte(`{"books": [`+strings.Join(entries, ",")+`]}`), out)
}

// books returns books titled like their description, with IDs OL1W, OL2W...
func books(descriptions ...string) []Book {
	out := make([]Book, len(descriptions))
	for i, d := range descriptions {
		out[i] = Book{ID: fmt.Sprintf("OL%dW", i+1), Title: "T" + d, Description: d, Hash: "h" + d}
	}

	return out
}

func doneIDs(r Result) map[string]string {
	out := map[string]string{}
	for _, e := range r.Done {
		out[e.WorkID] = e.Keywords.Keywords[0]
	}

	return out
}

func TestExtractBatch(t *testing.T) {
	ctx := context.Background()
	llm := &fakeLLM{}
	bs := books("one", "SKIP")

	ks, err := ExtractBatch(ctx, llm, bs)
	if err != nil || len(ks) != 1 || ks["OL1W"].Keywords[0] != "Tone" {
		t.Errorf("missing book: %+v, %v", ks, err)
	}

	bs[1] = books("one", "DUP")[1]
	if _, err := ExtractBatch(ctx, llm, bs); !errors.Is(err, ErrMixedUp) {
		t.Errorf("repeated book: %v", err)
	}

	if ks, err := ExtractBatch(ctx, llm, bs[:1]); err != nil || ks["OL1W"].Keywords[0] != "Tone" || llm.singles.Load() != 1 {
		t.Errorf("single book: %+v, %v", ks, err)
	}

	if msg := BatchMessage(bs); !strings.HasPrefix(msg, "Book OL1W\nTitle: Tone\n") || !strings.Contains(msg, "\n\n---\n\nBook OL2W\nTitle: TDUP\n") {
		t.Errorf("message: %q", msg)
	}
}

func TestExtractGroup(t *testing.T) {
	ctx := context.Background()

	// Round 1 misses ONCE, round 2 gets it; nothing is left for round 3.
	llm := &fakeLLM{}
	r := ExtractGroup(ctx, llm, books("one", "ONCE", "three"), 3)
	if got := doneIDs(r); len(got) != 3 || got["OL2W"] != "TONCE" || len(r.Failed) != 0 || r.Requests != 2 {
		t.Errorf("missing once: %v, %+v, %d requests", got, r.Failed, r.Requests)
	}

	if r.Done[0].Hash != "hone" {
		t.Errorf("hash: %+v", r.Done[0])
	}

	// A mixed-up answer keeps nothing: rounds 1 and 2 fail on DUP, round 3 asks each book alone.
	llm = &fakeLLM{}
	r = ExtractGroup(ctx, llm, books("one", "DUP"), 3)
	if got := doneIDs(r); len(got) != 2 || llm.batches.Load() != 2 || llm.singles.Load() != 2 {
		t.Errorf("mixed up: %v, %d batches, %d singles", got, llm.batches.Load(), llm.singles.Load())
	}

	// Books always failing or without keywords fail, with their last error; the others are kept.
	llm = &fakeLLM{}
	r = ExtractGroup(ctx, llm, books("one", "NEVER", "EMPTY", "BATCHFAIL"), 3)
	errs := map[string]error{}
	for _, f := range r.Failed {
		errs[f.Book.ID] = f.Err
	}

	if got := doneIDs(r); len(got) != 2 || got["OL1W"] != "Tone" || got["OL4W"] != "TBATCHFAIL" {
		t.Errorf("done: %v", got)
	}

	if len(errs) != 2 || errs["OL2W"] == nil || !errors.Is(errs["OL3W"], ErrNoKeywords) || r.Requests != 6 {
		t.Errorf("failed: %v, %d requests", errs, r.Requests)
	}

	// One attempt: a single request, whatever it misses.
	llm = &fakeLLM{}
	r = ExtractGroup(ctx, llm, books("one", "SKIP"), 1)
	if len(r.Done) != 1 || len(r.Failed) != 1 || !errors.Is(r.Failed[0].Err, ErrMissing) || r.Requests != 1 {
		t.Errorf("one attempt: %+v", r)
	}

	// A cancelled context stops before asking.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if r := ExtractGroup(cancelled, &fakeLLM{}, books("one"), 3); len(r.Failed) != 1 || !errors.Is(r.Failed[0].Err, context.Canceled) || r.Requests != 0 {
		t.Errorf("cancelled: %+v", r)
	}
}
