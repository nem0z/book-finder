package enrich

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/nem0z/book-finder/internal/llm"
)

const (
	// SchemaName names Schema for the LLM provider, BatchSchemaName BatchSchema.
	SchemaName      = "book_keywords"
	BatchSchemaName = "books_keywords"

	// batchInstructions follow SystemPrompt when a request holds several books.
	batchInstructions = `

You receive several books, each starting with a "Book <number>" line. Index each book on its own, as if it were the only one. Return {"books": [...]} with one entry per book, in the order received, each with its number as "id" and the fields above.`
)

var (
	// ErrMixedUp is an answer for several books with a book ID twice or one that was not asked about: the
	// books may have been mixed up, so none of its keywords are kept.
	ErrMixedUp = errors.New("answer for several books has an unexpected or repeated book ID")

	// ErrMissing is a book the answer for several books left out; ErrNoKeywords an answer without keywords.
	ErrMissing    = errors.New("book missing from the answer")
	ErrNoKeywords = errors.New("answer without keywords")
)

// BatchSchema is the answer for several books: Schema's fields and the book's ID, per book.
var BatchSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"books": {
			"type": "array",
			"items": {
				"type": "object",
				"properties": {
					"id": {"type": "string"},
					"keywords": {"type": "array", "items": {"type": "string"}},
					"setting_places": {"type": "array", "items": {"type": "string"}},
					"era_from": {"type": "integer"},
					"era_to": {"type": "integer"},
					"era_label": {"type": "string"},
					"themes": {"type": "array", "items": {"type": "string"}},
					"protagonist": {"type": "string"}
				},
				"required": ["id", "keywords", "setting_places", "era_from", "era_to", "era_label", "themes", "protagonist"],
				"additionalProperties": false
			}
		}
	},
	"required": ["books"],
	"additionalProperties": false
}`)

// Failure is a book whose keywords could not be extracted, and the last error.
type Failure struct {
	Book Book
	Err  error
}

// Result is what ExtractGroup got: the keywords to store, the books that failed every attempt, and how many
// requests it made.
type Result struct {
	Done     []Extracted
	Failed   []Failure
	Requests int
}

// Extract asks client for b's keywords.
func Extract(ctx context.Context, client llm.Client, b Book) (Keywords, error) {
	var k Keywords
	if err := client.JSON(ctx, SystemPrompt, UserMessage(b), SchemaName, Schema, &k); err != nil {
		return Keywords{}, fmt.Errorf("%s: %w", b.ID, err)
	}

	return k, nil
}

// ExtractBatch asks client for the keywords of several books in one request, which saves repeating the
// instructions and the per-request overhead. The request numbers the books from 1, and the answer must give
// each book its number: models copy a short number reliably, unlike some work IDs ("chapters:978…" came back
// as the bare ISBN). It returns the keywords of the books the answer covers, by work ID: a book missing from
// the answer is missing from the map. When the answer has a number twice or one it was not asked about, it
// returns ErrMixedUp and no keywords.
func ExtractBatch(ctx context.Context, client llm.Client, books []Book) (map[string]Keywords, error) {
	if len(books) == 1 {
		k, err := Extract(ctx, client, books[0])
		if err != nil {
			return nil, err
		}

		return map[string]Keywords{books[0].ID: k}, nil
	}

	var answer struct {
		Books []struct {
			ID string `json:"id"`
			Keywords
		} `json:"books"`
	}

	if err := client.JSON(ctx, SystemPrompt+batchInstructions, BatchMessage(books), BatchSchemaName, BatchSchema, &answer); err != nil {
		return nil, fmt.Errorf("%s and %d more: %w", books[0].ID, len(books)-1, err)
	}

	want := map[string]string{} // number in the request -> work ID
	for i, b := range books {
		want[strconv.Itoa(i+1)] = b.ID
	}

	out := map[string]Keywords{}
	for _, a := range answer.Books {
		id, ok := want[strings.TrimSpace(a.ID)]
		if _, dup := out[id]; dup || !ok {
			return nil, fmt.Errorf("%w: book %q", ErrMixedUp, a.ID)
		}

		out[id] = a.Keywords
	}

	return out, nil
}

// ExtractGroup extracts the keywords of books in up to attempts rounds. The first rounds ask for all the
// books still missing in one request; the last, when there are several rounds, asks for each book alone.
// A book is missing after a round when the request failed, the answer may mix books up (ErrMixedUp), left
// the book out or gave it no keywords.
func ExtractGroup(ctx context.Context, client llm.Client, books []Book, attempts int) Result {
	var r Result
	pending := books
	lastErr := map[string]error{}
	for round := 1; round <= max(attempts, 1) && len(pending) > 0 && ctx.Err() == nil; round++ {
		groups := [][]Book{pending}
		if round == attempts && round > 1 {
			groups = nil
			for _, b := range pending {
				groups = append(groups, []Book{b})
			}
		}

		var next []Book
		for _, g := range groups {
			ks, err := ExtractBatch(ctx, client, g)
			r.Requests++
			for _, b := range g {
				k, ok := ks[b.ID]
				switch {
				case err != nil:
					lastErr[b.ID] = err
				case !ok:
					lastErr[b.ID] = fmt.Errorf("%s: %w", b.ID, ErrMissing)
				case len(k.Normalize().Keywords) == 0:
					lastErr[b.ID], ok = fmt.Errorf("%s: %w", b.ID, ErrNoKeywords), false
				}

				if err != nil || !ok {
					next = append(next, b)
					continue
				}

				r.Done = append(r.Done, Extracted{WorkID: b.ID, Hash: b.Hash, Keywords: k})
			}
		}

		pending = next
	}

	for _, b := range pending {
		err := lastErr[b.ID]
		if err == nil {
			err = ctx.Err()
		}

		r.Failed = append(r.Failed, Failure{Book: b, Err: err})
	}

	return r
}

// BatchMessage is what the model reads about several books, numbered from 1.
func BatchMessage(books []Book) string {
	parts := make([]string, len(books))
	for i, b := range books {
		parts[i] = "Book " + strconv.Itoa(i+1) + "\n" + UserMessage(b)
	}

	return strings.Join(parts, "\n\n---\n\n")
}
