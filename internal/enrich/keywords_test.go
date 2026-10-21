package enrich

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestNormalize(t *testing.T) {
	k := Keywords{
		Keywords:    []string{" Amsterdam ", "amsterdam", "", "Expat\nLife", strings.Repeat("x", 200)},
		Places:      []string{"Amsterdam, Netherlands", "Utrecht", "Delft", "Leiden"},
		EraFrom:     2020,
		EraTo:       2023,
		EraLabel:    " 2020s ",
		Themes:      nil,
		Protagonist: "expat woman",
	}.Normalize()

	if len(k.Keywords) != 3 || k.Keywords[0] != "amsterdam" || k.Keywords[1] != "expat life" || len(k.Keywords[2]) != maxTermBytes {
		t.Errorf("keywords: %q", k.Keywords)
	}

	if !slices.Equal(k.Places, []string{"Amsterdam, Netherlands", "Utrecht", "Delft"}) || k.Themes == nil {
		t.Errorf("places %q, themes %#v", k.Places, k.Themes)
	}

	if k.EraFrom != 2020 || k.EraTo != 2023 || k.EraLabel != "2020s" {
		t.Errorf("era: %+v", k)
	}

	for _, tc := range []struct{ from, to, wantFrom, wantTo int }{
		{-50, 30, -50, 30},
		{1900, 1850, 0, 0},
		{9999, 2000, 0, 2000},
		{1850, 0, 1850, 0},
	} {
		if k := (Keywords{EraFrom: tc.from, EraTo: tc.to}).Normalize(); k.EraFrom != tc.wantFrom || k.EraTo != tc.wantTo {
			t.Errorf("era %d-%d: got %d-%d", tc.from, tc.to, k.EraFrom, k.EraTo)
		}
	}
}

func TestText(t *testing.T) {
	k := Keywords{Keywords: []string{"orc", "coffee shop"}, Places: []string{"Thune"}, Themes: []string{"found family"}, EraLabel: "invented world"}
	if got := k.Text(); got != "orc; coffee shop; Thune; found family; invented world" {
		t.Errorf("got %q", got)
	}
}

func TestUserMessage(t *testing.T) {
	b := Book{Title: "Rebecca", Authors: []string{"Daphne du Maurier"}, Description: " x" + strings.Repeat("é", 3000)}
	msg := UserMessage(b)
	if !strings.HasPrefix(msg, "Title: Rebecca\nAuthors: Daphne du Maurier\n\nDescription:\nxé") {
		t.Errorf("message: %q", msg[:80])
	}

	// No space to cut at: the cut moves back to the start of a character.
	if desc := msg[strings.Index(msg, "xé"):]; len(desc) != maxDescriptionBytes-1 || !utf8.ValidString(desc) {
		t.Errorf("description cut at %d bytes", len(desc))
	}

	if got := truncate("one two three four", 12); got != "one two" {
		t.Errorf("word cut: %q", got)
	}

	if msg := UserMessage(Book{Title: "T", Description: "d"}); msg != "Title: T\n\nDescription:\nd" {
		t.Errorf("without authors: %q", msg)
	}
}

func TestContentHash(t *testing.T) {
	b := Book{Title: "T", Authors: []string{"A", "B"}, Description: "d"}
	h := ContentHash("m", b)
	for _, other := range []string{
		ContentHash("m2", b),
		ContentHash("m", Book{Title: "T", Authors: []string{"AB"}, Description: "d"}),
		ContentHash("m", Book{Title: "T", Authors: []string{"A", "B"}, Description: "d2"}),
	} {
		if other == h {
			t.Error("hash should change with the model, authors and description")
		}
	}

	if len(h) != 2*hashBytes || ContentHash("m", b) != h {
		t.Errorf("hash %q", h)
	}
}

// TestSchema checks that the schema and the Keywords struct list the same fields, all required.
func TestSchema(t *testing.T) {
	var s struct {
		Properties map[string]any `json:"properties"`
		Required   []string       `json:"required"`
	}
	if err := json.Unmarshal(Schema, &s); err != nil {
		t.Fatal(err)
	}

	out, err := json.Marshal(Keywords{})
	if err != nil {
		t.Fatal(err)
	}

	var fields map[string]any
	if err := json.Unmarshal(out, &fields); err != nil {
		t.Fatal(err)
	}

	if len(s.Properties) != len(fields) || len(s.Required) != len(fields) {
		t.Fatalf("schema has %d properties and %d required, struct %d fields", len(s.Properties), len(s.Required), len(fields))
	}

	for _, name := range s.Required {
		if _, ok := fields[name]; !ok {
			t.Errorf("%s is not a Keywords field", name)
		}
	}
}
