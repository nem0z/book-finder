package oldump

import (
	"bytes"
	"strconv"
	"strings"
)

const (
	recordColumns = 5 // type, key, revision, last_modified, JSON

	// fictionSubject is the subject that marks a work as fiction (compared case-insensitively).
	fictionSubject = "Fiction"

	isbn10Length    = 10
	isbn13Length    = 13
	isbnCheckDigitX = 'X'
)

var (
	worksRefPrefix = []byte(`"/works/OL`)

	// Byte prefilter: the fiction subject as a quoted JSON string, in its two common spellings.
	fictionQuoted      = []byte(`"` + fictionSubject + `"`)
	fictionQuotedLower = []byte(`"` + strings.ToLower(fictionSubject) + `"`)
)

// SplitRecord splits a dump line into its type, key and JSON columns.
func SplitRecord(line []byte) (typ, key, data []byte, ok bool) {
	parts := bytes.SplitN(line, []byte{'\t'}, recordColumns)
	if len(parts) != recordColumns {
		return nil, nil, nil, false
	}

	return parts[0], parts[1], parts[recordColumns-1], true
}

// ParseYear returns s as a year when it is exactly a positive integer no greater than maxYear,
// such as "1922". Free-text dates ("March 3, 1922") are not trusted and return 0.
func ParseYear(s string, maxYear int) int {
	y, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || y <= 0 || y > maxYear {
		return 0
	}

	return y
}

// IsFiction reports whether the subjects include the fiction subject.
func IsFiction(subjects []string) bool {
	for _, s := range subjects {
		if strings.EqualFold(strings.TrimSpace(s), fictionSubject) {
			return true
		}
	}

	return false
}

// MentionsFiction is a cheap byte-level prefilter run before JSON decoding.
func MentionsFiction(line []byte) bool {
	return bytes.Contains(line, fictionQuoted) || bytes.Contains(line, fictionQuotedLower)
}

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

// NormalizeISBN keeps digits and X; it returns "" unless the result has an ISBN-10 or ISBN-13 length.
func NormalizeISBN(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == 'x' || r == isbnCheckDigitX:
			b.WriteByte(isbnCheckDigitX)
		}
	}

	if n := b.Len(); n == isbn10Length || n == isbn13Length {
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
