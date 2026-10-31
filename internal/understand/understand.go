// Package understand reads a book search query with an LLM: it pulls out the hard filters the search
// supports, and rewrites the query as the keywords a matching book would have, in the shape of the books'
// own keywords (enrich), which retrieval searches. On the recommendation benchmark, these keywords found
// more relevant books than the query or a sentence describing the wanted book (docs/benchmarks.md).
package understand

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/nem0z/book-finder/internal/enrich"
	"github.com/nem0z/book-finder/internal/llm"
	"github.com/nem0z/book-finder/internal/search"
)

const (
	// maxPages is the longest length accepted as a filter; larger values are taken as mistakes.
	maxPages = 10_000

	schemaName = "book_query"

	// systemPrompt takes the current year. The keyword fields are those enrich.SystemPrompt asks of a book.
	// Language is not extracted as a filter: most translated works have original_language "eng" in the
	// catalogue, so a language filter would hide right answers.
	systemPrompt = `You index searches for a keyword search engine over novels. A reader describes the book they want; return the keywords the index of a matching book would have, and the hard filters the reader states:
- keywords: 15 to 30 short lowercase terms: places, period, themes, tropes, genre and subgenre, mood and tone, type of protagonist, notable objects, creatures, jobs and events. Add common synonyms and other word forms ("holland", "netherlands", "dutch"; "insect", "bug", "vermin"). No words about the reader or the search itself ("looking for", "recommend", "book").
- setting_places: up to 3 places where the story happens, most specific first, as "City, Country" when known.
- era_from, era_to: the first and last years in which the story takes place (negative for BC), 0 when unknown.
- era_label: the period in a few words ("2020s", "Victorian era", "Middle Ages", "far future", "invented world"), empty when unknown.
- themes: up to 6 themes, a few words each.
- protagonist: the main character's type in a few words, empty when unclear.
For these, use only what the search says or clearly implies: leave a field empty rather than guess, and do not name a specific book.
- min_year, max_year: bounds on the year the book was first published, 0 when not set. Set them only from what the reader says about when the book was written or published ("published after 2015", "a novel from the 60s"), or from a story set in a recent period the reader dates ("set in 2020s Berlin", "modern Amsterdam (2020+)"): a book about that period was published then or later, so set min_year only. A story set in the past says nothing about when it was written: "set in Victorian London" sets no year. Words like "modern" or "recent" without a date set no year.
- min_pages, max_pages: bounds on the length in pages, 0 when not set. Only from an explicit number ("under 200 pages", "800+ pages"). For vague lengths ("short", "long") set nothing and keep the word in the keywords.

The current year is %d.`
)

var (
	// datePattern finds a year (1925, 2020+), a decade (1960s, '60s) or a century (19th).
	datePattern = regexp.MustCompile(`(?i)\b\d{4}\b|\d0s\b|\b\d{1,2}(st|nd|rd|th)\b`)

	// queryJSONSchema is the answer the model must return: enrich.Schema's fields and the filters. Strict
	// structured outputs need every property required and no others, hence 0 and "" rather than missing fields.
	queryJSONSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"keywords": {"type": "array", "items": {"type": "string"}},
		"setting_places": {"type": "array", "items": {"type": "string"}},
		"era_from": {"type": "integer"},
		"era_to": {"type": "integer"},
		"era_label": {"type": "string"},
		"themes": {"type": "array", "items": {"type": "string"}},
		"protagonist": {"type": "string"},
		"min_year": {"type": "integer"},
		"max_year": {"type": "integer"},
		"min_pages": {"type": "integer"},
		"max_pages": {"type": "integer"}
	},
	"required": ["keywords", "setting_places", "era_from", "era_to", "era_label", "themes", "protagonist",
		"min_year", "max_year", "min_pages", "max_pages"],
	"additionalProperties": false
}`)
)

// Interpreter reads queries with a chat model.
type Interpreter struct {
	llm llm.Client
	now func() time.Time
}

// queryAnswer is the model's answer, as described by queryJSONSchema.
type queryAnswer struct {
	enrich.Keywords
	MinYear  int `json:"min_year"`
	MaxYear  int `json:"max_year"`
	MinPages int `json:"min_pages"`
	MaxPages int `json:"max_pages"`
}

func New(c llm.Client) *Interpreter {
	return &Interpreter{llm: c, now: time.Now}
}

// Interpret returns the filters query states and the keywords of the book it asks for.
func (in *Interpreter) Interpret(ctx context.Context, query string) (search.Interpretation, error) {
	year := in.now().Year()

	var a queryAnswer
	if err := in.llm.JSON(ctx, fmt.Sprintf(systemPrompt, year), query, schemaName, queryJSONSchema, &a); err != nil {
		return search.Interpretation{}, err
	}

	return a.interpretation(query, year), nil
}

// interpretation checks the answer. A filter hides books for good, so it needs grounds in the query: year
// bounds a year, decade or century ("2020+", "the 60s", "19th century"), page bounds the word "page". Small
// models invent bounds otherwise. Out-of-range or contradictory bounds are dropped too. The keywords are
// cleaned up and joined as a book's are (enrich.Keywords.Text); without any, retrieval searches the query.
func (a queryAnswer) interpretation(query string, year int) search.Interpretation {
	var f search.Filters
	if datePattern.MatchString(query) {
		f.MinYear, f.MaxYear = bounds(a.MinYear, a.MaxYear, year+1)
	}

	if strings.Contains(strings.ToLower(query), "page") {
		f.MinPages, f.MaxPages = bounds(a.MinPages, a.MaxPages, maxPages)
	}

	text := strings.TrimSpace(a.Keywords.Normalize().Text())
	if text == "" {
		text = query
	}

	return search.Interpretation{Filters: f, Text: text}
}

// bounds keeps lo and hi when they are in [1, limit]. Bounds that contradict each other are both dropped.
func bounds(lo, hi, limit int) (int, int) {
	if lo < 1 || lo > limit {
		lo = 0
	}

	if hi < 1 || hi > limit {
		hi = 0
	}

	if lo > 0 && hi > 0 && lo > hi {
		return 0, 0
	}

	return lo, hi
}
