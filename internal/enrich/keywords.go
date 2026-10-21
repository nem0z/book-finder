// Package enrich extracts keywords from book descriptions with an LLM and stores them in work_keywords,
// for keyword search.
package enrich

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	// PromptVersion is part of the content hash: changing the prompt or the schema means bumping it, so
	// that every work is extracted again.
	PromptVersion = "1"

	// SystemPrompt asks for keywords about the book's content. Names are left out: queries describe
	// books, and title or author lookup is another feature.
	SystemPrompt = `You index novels for a keyword search engine. From a book's title, authors and description, return:
- keywords: 15 to 30 short lowercase terms a reader might use when looking for this book: places, period, themes, tropes, genre and subgenre, mood and tone, type of protagonist, notable objects, creatures, jobs and events. Add common synonyms and other word forms ("algeria", "algerian"; "insect", "bug", "vermin"). No character names, title words or author names.
- setting_places: up to 3 places where the story happens, most specific first, as "City, Country" when known.
- era_from, era_to: the first and last years in which the story takes place (negative for BC), 0 when unknown. Infer them from clues in the description (events, technology), never from when the book was published.
- era_label: the period in a few words ("2020s", "Victorian era", "Middle Ages", "far future", "invented world"), empty when unknown.
- themes: up to 6 themes, a few words each.
- protagonist: the main character's type in a few words ("expat woman in her thirties", "orphan boy wizard"), without names, empty when unclear.
Use only what the title and description support: leave a field empty rather than guess.`

	maxDescriptionBytes = 4000
	maxKeywords         = 30
	maxPlaces           = 3
	maxThemes           = 6
	maxTermBytes        = 80
	minEra, maxEra      = -5000, 2200
	hashBytes           = 16
)

// Schema is the structured output the model must return. Strict mode needs every property required and
// no others, hence 0 and "" rather than missing fields.
var Schema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"keywords": {"type": "array", "items": {"type": "string"}},
		"setting_places": {"type": "array", "items": {"type": "string"}},
		"era_from": {"type": "integer"},
		"era_to": {"type": "integer"},
		"era_label": {"type": "string"},
		"themes": {"type": "array", "items": {"type": "string"}},
		"protagonist": {"type": "string"}
	},
	"required": ["keywords", "setting_places", "era_from", "era_to", "era_label", "themes", "protagonist"],
	"additionalProperties": false
}`)

// Book is a work to extract keywords from. Hash is the content hash its keywords must have.
type Book struct {
	ID          string
	Title       string
	Authors     []string
	Description string
	Hash        string
}

// Keywords is the model's answer, as described by Schema.
type Keywords struct {
	Keywords    []string `json:"keywords"`
	Places      []string `json:"setting_places"`
	EraFrom     int      `json:"era_from"`
	EraTo       int      `json:"era_to"`
	EraLabel    string   `json:"era_label"`
	Themes      []string `json:"themes"`
	Protagonist string   `json:"protagonist"`
}

// UserMessage is what the model reads about b.
func UserMessage(b Book) string {
	var s strings.Builder
	s.WriteString("Title: " + b.Title + "\n")
	if len(b.Authors) > 0 {
		s.WriteString("Authors: " + strings.Join(b.Authors, ", ") + "\n")
	}

	s.WriteString("\nDescription:\n" + truncate(strings.TrimSpace(b.Description), maxDescriptionBytes))

	return s.String()
}

// ContentHash identifies the model, prompt and text keywords are extracted from, so that unchanged works
// are not extracted again after a reload.
func ContentHash(model string, b Book) string {
	text := strings.Join([]string{model, PromptVersion, b.Title, strings.Join(b.Authors, "\x01"), b.Description}, "\x00")
	h := sha256.Sum256([]byte(text))
	return hex.EncodeToString(h[:hashBytes])
}

// Normalize returns k cleaned up: trimmed terms without empty ones or duplicates (in any case), no more
// than the prompt asks for, lowercase keywords, and era years only when plausible and in order.
func (k Keywords) Normalize() Keywords {
	k.Keywords = terms(k.Keywords, maxKeywords, true)
	k.Places = terms(k.Places, maxPlaces, false)
	k.Themes = terms(k.Themes, maxThemes, false)
	k.EraLabel = term(k.EraLabel)
	k.Protagonist = term(k.Protagonist)

	if k.EraFrom < minEra || k.EraFrom > maxEra {
		k.EraFrom = 0
	}

	if k.EraTo < minEra || k.EraTo > maxEra {
		k.EraTo = 0
	}

	if k.EraFrom != 0 && k.EraTo != 0 && k.EraFrom > k.EraTo {
		k.EraFrom, k.EraTo = 0, 0
	}

	return k
}

// Text is every field as one text, for full-text search.
func (k Keywords) Text() string {
	parts := slices.Concat(k.Keywords, k.Places, k.Themes)
	for _, s := range []string{k.EraLabel, k.Protagonist} {
		if s != "" {
			parts = append(parts, s)
		}
	}

	return strings.Join(parts, "; ")
}

// terms trims each term, drops empty ones and duplicates, and keeps at most max; lower lowercases them.
func terms(list []string, max int, lower bool) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, t := range list {
		t = term(t)
		if lower {
			t = strings.ToLower(t)
		}

		k := strings.ToLower(t)
		if t == "" || seen[k] {
			continue
		}

		seen[k] = true
		out = append(out, t)
		if len(out) == max {
			break
		}
	}

	return out
}

// term trims s, joins its lines and cuts it to maxTermBytes.
func term(s string) string {
	return truncate(strings.Join(strings.Fields(s), " "), maxTermBytes)
}

// truncate cuts s to at most max bytes, at a word boundary when there is one in the second half, and
// never inside a UTF-8 character.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}

	cut := strings.LastIndexAny(s[:max], " \n")
	if cut < max/2 {
		cut = max
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
	}

	return strings.TrimSpace(s[:cut])
}
