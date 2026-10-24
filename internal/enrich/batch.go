package enrich

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/nem0z/book-finder/internal/llm"
)

const (
	// BatchSchemaName names BatchSchema for the LLM provider.
	BatchSchemaName = "books_keywords"

	// batchInstructions follow SystemPrompt when a request holds several books.
	batchInstructions = `

You receive several books, each starting with a "Book <id>" line. Index each book on its own, as if it were the only one. Return {"books": [...]} with one entry per book, in the order received, each with its "id" and the fields above.`
)

var ErrMixedUp = errors.New("answer for several books has an unexpected or repeated book ID")

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

// ExtractBatch asks client for the keywords of several books in one request, which saves repeating the
// instructions and the per-request overhead. Each book has an ID in the request and must have it in the
// answer. It returns the keywords of the books the answer covers, by ID: a book missing from the answer is
// missing from the map. When the answer has an ID twice or an ID it was not asked about, the books may have
// been mixed up, so it returns ErrMixedUp and no keywords.
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

	want := map[string]bool{}
	for _, b := range books {
		want[b.ID] = true
	}

	out := map[string]Keywords{}
	for _, a := range answer.Books {
		if _, dup := out[a.ID]; dup || !want[a.ID] {
			return nil, fmt.Errorf("%w: book %q", ErrMixedUp, a.ID)
		}

		out[a.ID] = a.Keywords
	}

	return out, nil
}

// BatchMessage is what the model reads about several books.
func BatchMessage(books []Book) string {
	parts := make([]string, len(books))
	for i, b := range books {
		parts[i] = "Book " + b.ID + "\n" + UserMessage(b)
	}

	return strings.Join(parts, "\n\n---\n\n")
}
