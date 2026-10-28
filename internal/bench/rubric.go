package bench

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	// PromptVersion is part of the cache key: changing the prompt means bumping it.
	PromptVersion = "rec-1"

	// MaxRating is the best overall rating, MaxDimension the best score of a dimension; NotApplicable marks
	// a dimension the search doesn't ask about.
	MaxRating     = 3
	MaxDimension  = 2
	NotApplicable = -1

	schemaName = "recommendation"

	// prompt rates a book as a recommendation for what the reader asks. The judge sees the book as a reader
	// would (title, authors, year, description), never the keywords or the vector a retrieval used.
	prompt = `You judge book recommendations. A reader describes what they want to read next, and you see one book from a catalogue of fiction: its title, authors, year of first publication and description. Judge it as a recommendation for this reader.

Rate the book from 0 to 3:
3: excellent recommendation: fits everything the reader asks for.
2: good recommendation: fits the main things they ask for, with some differences.
1: weak: shares something (a place, a genre, a theme) but misses what they mainly want.
0: not a recommendation for this reader.

Then score each dimension from 0 to 2 (0: misses it, 1: partly fits, 2: fits), or -1 when the reader doesn't ask for anything about it:
- plot: the plot, premise or situation asked for.
- mood: the mood, tone, vibe or emotional effect asked for.
- place: the setting's place.
- era: the setting's period, or when the book was written.
- subject: a subject, culture, job or experience the reader wants to learn about or live.
- character: the kind of main character or narrator.
- style: likeness to a book or author the reader names, or the writing style asked for.
- constraints: practical constraints: length, audience or age group, original language or translation, publication date, series, things to avoid.

A book the reader names as already read is not a recommendation: rate it 0. Other books by an author the reader names are judged on how well they fit. A book that breaks an explicit constraint (length, age group, period, something to avoid) gets 1 at most. Judge from the information given; do not reward a book for being famous.
Return the rating, the dimension scores and a reason of at most 20 words.`
)

var (
	// Dimensions are what the judge scores besides the overall rating.
	Dimensions = []string{"plot", "mood", "place", "era", "subject", "character", "style", "constraints"}

	schema = answerSchema()
)

func answerSchema() json.RawMessage {
	props := make([]string, len(Dimensions))
	for i, d := range Dimensions {
		props[i] = `"` + d + `": {"type": "integer"}`
	}

	required := `"` + strings.Join(Dimensions, `", "`) + `"`

	return json.RawMessage(`{
	"type": "object",
	"properties": {
		"rating": {"type": "integer"},
		"dimensions": {
			"type": "object",
			"properties": {` + strings.Join(props, ", ") + `},
			"required": [` + required + `],
			"additionalProperties": false
		},
		"reason": {"type": "string"}
	},
	"required": ["rating", "dimensions", "reason"],
	"additionalProperties": false
}`)
}

// checkAnswer returns an error when the rating or a dimension score is out of range, or a dimension is
// missing.
func checkAnswer(rating int, dims map[string]int) error {
	if rating < 0 || rating > MaxRating {
		return fmt.Errorf("rating %d out of range", rating)
	}

	for _, d := range Dimensions {
		if s, ok := dims[d]; !ok || s < NotApplicable || s > MaxDimension {
			return fmt.Errorf("dimension %s: %d (present: %v) out of range", d, s, ok)
		}
	}

	return nil
}
