// Package oldump reads Open Library monthly dump files
// (https://openlibrary.org/developers/dumps).
//
// Each line is tab-separated: type, key, revision, last_modified, JSON.
// Record shapes follow internetarchive/openlibrary-client olclient/schemata, but real data
// is messier than the schemas, so the decoders below tolerate common variants.
package oldump

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// SplitRecord splits a dump line into its type, key and JSON columns.
func SplitRecord(line []byte) (typ, key, data []byte, ok bool) {
	parts := bytes.SplitN(line, []byte{'\t'}, 5)
	if len(parts) != 5 {
		return nil, nil, nil, false
	}
	return parts[0], parts[1], parts[4], true
}

// TextBlock decodes a field that is either a plain string or {"type": "/type/text", "value": "..."}.
type TextBlock string

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

// KeyRef decodes a reference that is either "/path/key" or {"key": "/path/key"}.
type KeyRef string

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

// FlexInt decodes a number that may also appear as a numeric string. Garbage decodes to 0.
type FlexInt int64

func (f *FlexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if v, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
		*f = FlexInt(v)
	}
	return nil
}

// StringList decodes an array of strings, dropping non-string or empty items.
type StringList []string

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

// WorkAuthor is an entry of a work's "authors" array. Usually
// {"author": {"key": "/authors/OL1A"}, "type": ...}, sometimes {"key": ...} or a bare string.
type WorkAuthor struct {
	Key string
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
	ISBN10         StringList `json:"isbn_10"`
	PublishDate    string     `json:"publish_date"`
	PhysicalFormat string     `json:"physical_format"`
	Genres         StringList `json:"genres"`
	Description    TextBlock  `json:"description"`
	FirstSentence  TextBlock  `json:"first_sentence"`
	Covers         []FlexInt  `json:"covers"`
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

type Author struct {
	Key       string                     `json:"key"`
	Name      string                     `json:"name"`
	BirthDate string                     `json:"birth_date"`
	DeathDate string                     `json:"death_date"`
	RemoteIDs map[string]json.RawMessage `json:"remote_ids"`
}

func (a *Author) WikidataID() string {
	var s string
	if raw, ok := a.RemoteIDs["wikidata"]; ok && json.Unmarshal(raw, &s) == nil {
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

var yearRe = regexp.MustCompile(`(?:^|[^0-9])([12][0-9]{3})(?:[^0-9]|$)`)

// ParseYear extracts the first plausible year (1000..maxYear) from free-text dates
// such as "1922", "March 3, 1922" or "c1922". It returns 0 when none is found.
func ParseYear(s string, maxYear int) int {
	for _, m := range yearRe.FindAllStringSubmatch(s, -1) {
		y, _ := strconv.Atoi(m[1])
		if y >= 1000 && y <= maxYear {
			return y
		}
	}
	return 0
}

// IsFiction reports whether any subject mentions fiction, excluding "nonfiction"/"non-fiction".
func IsFiction(subjects []string) bool {
	for _, s := range subjects {
		l := strings.ToLower(s)
		if strings.Contains(l, "fiction") && !strings.Contains(l, "nonfiction") && !strings.Contains(l, "non-fiction") {
			return true
		}
	}
	return false
}

// MentionsFiction is a cheap byte-level prefilter run before JSON decoding.
func MentionsFiction(line []byte) bool {
	return bytes.Contains(line, []byte("iction")) || bytes.Contains(line, []byte("ICTION"))
}

var worksRefPrefix = []byte(`"/works/OL`)

// EditionWorkKey finds the first work key in an edition's raw JSON without decoding it,
// returning e.g. "/works/OL45804W".
func EditionWorkKey(data []byte) (string, bool) {
	i := bytes.Index(data, worksRefPrefix)
	if i < 0 {
		return "", false
	}
	rest := data[i+1:]
	j := bytes.IndexByte(rest, '"')
	if j < 0 {
		return "", false
	}
	return string(rest[:j]), true
}

// NormalizeISBN keeps digits and X; it returns "" unless the result has 10 or 13 characters.
func NormalizeISBN(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == 'x' || r == 'X':
			b.WriteByte('X')
		}
	}
	if n := b.Len(); n == 10 || n == 13 {
		return b.String()
	}
	return ""
}

// Clean trims whitespace, repairs invalid UTF-8 and removes NUL bytes, which Postgres text rejects.
func Clean(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	s = strings.ReplaceAll(s, "\x00", "")
	return strings.TrimSpace(s)
}
