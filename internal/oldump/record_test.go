package oldump

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestTextBlock(t *testing.T) {
	for in, want := range map[string]string{
		`"plain"`:                              "plain",
		`{"type": "/type/text", "value": "v"}`: "v",
		`null`:                                 "",
		`42`:                                   "",
	} {
		var tb TextBlock
		if err := json.Unmarshal([]byte(in), &tb); err != nil {
			t.Fatalf("%s: %v", in, err)
		}

		if string(tb) != want {
			t.Errorf("%s: got %q want %q", in, tb, want)
		}
	}
}

func TestWorkDecodeVariants(t *testing.T) {
	data := `{"key": "/works/OL1W", "title": "T",
		"authors": [
			{"author": {"key": "/authors/OL1A"}, "type": {"key": "/type/author_role"}},
			{"key": "/authors/OL2A"},
			"/authors/OL3A",
			{"author": "/authors/OL4A"}
		],
		"subjects": ["Fiction", 3, "", "Thrillers"],
		"covers": [-1, 123, "456"],
		"description": {"type": "/type/text", "value": "D"}}`
	var w Work
	if err := json.Unmarshal([]byte(data), &w); err != nil {
		t.Fatal(err)
	}

	var keys []string
	for _, a := range w.Authors {
		keys = append(keys, a.Key)
	}

	if want := []string{"/authors/OL1A", "/authors/OL2A", "/authors/OL3A", "/authors/OL4A"}; !slices.Equal(keys, want) {
		t.Errorf("authors: %v", keys)
	}

	if !slices.Equal(w.Subjects, []string{"Fiction", "Thrillers"}) {
		t.Errorf("subjects: %v", w.Subjects)
	}

	if FirstCover(w.Covers) != 123 {
		t.Errorf("cover: %d", FirstCover(w.Covers))
	}

	if w.Description != "D" {
		t.Errorf("description: %q", w.Description)
	}
}

func TestEditionDecode(t *testing.T) {
	data := []byte(`{"key": "/books/OL1M", "works": [{"key": "/works/OL9W"}], "languages": [{"key": "/languages/eng"}], "number_of_pages": "320"}`)
	var e Edition
	if err := json.Unmarshal(data, &e); err != nil {
		t.Fatal(err)
	}

	if !e.HasLanguage("eng") || e.HasLanguage("fre") {
		t.Error("HasLanguage")
	}

	if e.NumberOfPages != 320 {
		t.Errorf("pages: %d", e.NumberOfPages)
	}

	if len(e.Works) != 1 || e.Works[0] != "/works/OL9W" {
		t.Errorf("works: %v", e.Works)
	}
}

func TestAuthorWikidataID(t *testing.T) {
	var a Author
	if err := json.Unmarshal([]byte(`{"name": "N", "remote_ids": {"wikidata": "Q123", "viaf": 42}}`), &a); err != nil {
		t.Fatal(err)
	}

	if a.WikidataID() != "Q123" {
		t.Errorf("wikidata: %q", a.WikidataID())
	}
}

func TestShortKey(t *testing.T) {
	for in, want := range map[string]string{"/works/OL45804W": "OL45804W", "OL1A": "OL1A", "": ""} {
		if got := ShortKey(in); got != want {
			t.Errorf("%q: got %q", in, got)
		}
	}
}
