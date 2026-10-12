// Package oldump reads Open Library monthly dump files
// (https://openlibrary.org/developers/dumps).
//
// Each line is tab-separated: type, key, revision, last_modified, JSON.
// Record shapes follow internetarchive/openlibrary-client olclient/schemata, but real data
// is messier than the schemas, so the decoders below tolerate common variants.
package oldump

import (
	"encoding/json"
	"strconv"
	"strings"
)

// wikidataRemoteID is the author remote_ids key holding the Wikidata item ID.
const wikidataRemoteID = "wikidata"

// TextBlock decodes a field that is either a plain string or {"type": "/type/text", "value": "..."}.
type TextBlock string

// KeyRef decodes a reference that is either "/path/key" or {"key": "/path/key"}.
type KeyRef string

// FlexInt decodes a number that may also appear as a numeric string. Garbage decodes to 0.
type FlexInt int64

// StringList decodes an array of strings, dropping non-string or empty items.
type StringList []string

// WorkAuthor is an entry of a work's "authors" array. Usually
// {"author": {"key": "/authors/OL1A"}, "type": ...}, sometimes {"key": ...} or a bare string.
type WorkAuthor struct {
	Key string
}

type Work struct {
	Key              string       `json:"key"`
	Title            string       `json:"title"`
	Subtitle         string       `json:"subtitle"`
	Authors          []WorkAuthor `json:"authors"`
	Subjects         StringList   `json:"subjects"`
	SubjectPlaces    StringList   `json:"subject_places"`
	SubjectPeople    StringList   `json:"subject_people"`
	SubjectTimes     StringList   `json:"subject_times"`
	Description      TextBlock    `json:"description"`
	FirstPublishDate string       `json:"first_publish_date"`
	Covers           []FlexInt    `json:"covers"`
}

type Edition struct {
	Key            string     `json:"key"`
	Title          string     `json:"title"`
	Works          []KeyRef   `json:"works"`
	Languages      []KeyRef   `json:"languages"`
	TranslatedFrom []KeyRef   `json:"translated_from"`
	NumberOfPages  FlexInt    `json:"number_of_pages"`
	ISBN13         StringList `json:"isbn_13"`
	PublishDate    string     `json:"publish_date"`
	Genres         StringList `json:"genres"`
	Description    TextBlock  `json:"description"`
	Covers         []FlexInt  `json:"covers"`
}

type Author struct {
	Key       string                     `json:"key"`
	Name      string                     `json:"name"`
	BirthDate string                     `json:"birth_date"`
	DeathDate string                     `json:"death_date"`
	RemoteIDs map[string]json.RawMessage `json:"remote_ids"`
}

func (t *TextBlock) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}

		*t = TextBlock(s)
		return nil
	}

	var o struct {
		Value string `json:"value"`
	}

	if json.Unmarshal(b, &o) == nil {
		*t = TextBlock(o.Value)
	}

	return nil
}

func (k *KeyRef) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}

		*k = KeyRef(s)
		return nil
	}

	var o struct {
		Key string `json:"key"`
	}

	if json.Unmarshal(b, &o) == nil {
		*k = KeyRef(o.Key)
	}

	return nil
}

func (f *FlexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if v, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
		*f = FlexInt(v)
	}

	return nil
}

func (l *StringList) UnmarshalJSON(b []byte) error {
	var raw []json.RawMessage
	if json.Unmarshal(b, &raw) != nil {
		return nil
	}

	out := make([]string, 0, len(raw))
	for _, r := range raw {
		var s string
		if json.Unmarshal(r, &s) == nil && strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}

	*l = out
	return nil
}

func (a *WorkAuthor) UnmarshalJSON(b []byte) error {
	var k KeyRef
	if len(b) > 0 && b[0] == '"' {
		if err := k.UnmarshalJSON(b); err != nil {
			return err
		}

		a.Key = string(k)
		return nil
	}

	var o struct {
		Author KeyRef `json:"author"`
		Key    KeyRef `json:"key"`
	}

	if json.Unmarshal(b, &o) == nil {
		a.Key = string(o.Author)
		if a.Key == "" {
			a.Key = string(o.Key)
		}
	}

	return nil
}

// HasLanguage reports whether the edition lists the MARC language code (e.g. "eng").
func (e *Edition) HasLanguage(code string) bool {
	for _, l := range e.Languages {
		if ShortKey(string(l)) == code {
			return true
		}
	}

	return false
}

func (a *Author) WikidataID() string {
	var s string
	if raw, ok := a.RemoteIDs[wikidataRemoteID]; ok && json.Unmarshal(raw, &s) == nil {
		return s
	}

	return ""
}

// ShortKey strips the path from an Open Library key: "/works/OL45804W" -> "OL45804W".
func ShortKey(key string) string {
	return key[strings.LastIndexByte(key, '/')+1:]
}

// FirstCover returns the first valid cover ID (Open Library uses -1 for removed covers).
func FirstCover(covers []FlexInt) int64 {
	for _, c := range covers {
		if c > 0 {
			return int64(c)
		}
	}

	return 0
}
