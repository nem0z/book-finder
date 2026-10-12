package oldump

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"
)

const (
	recordColumns = 5 // type, key, revision, last_modified, JSON

	minYear = 1000

	fictionWord    = "fiction"
	nonfictionWord = "nonfiction"
	nonFictionWord = "non-fiction"
	// Byte prefilter: "iction" matches "Fiction" and "fiction".
	fictionLower = "iction"
	fictionUpper = "ICTION"

	isbn10Length    = 10
	isbn13Length    = 13
	isbnCheckDigitX = 'X'
)

var (
	// yearRe matches a standalone 4-digit number starting with 1 or 2.
	yearRe = regexp.MustCompile(`(?:^|[^0-9])([12][0-9]{3})(?:[^0-9]|$)`)

	worksRefPrefix = []byte(`"/works/OL`)
)

// SplitRecord splits a dump line into its type, key and JSON columns.
func SplitRecord(line []byte) (typ, key, data []byte, ok bool) {
	parts := bytes.SplitN(line, []byte{'\t'}, recordColumns)
	if len(parts) != recordColumns {
		return nil, nil, nil, false
	}

	return parts[0], parts[1], parts[recordColumns-1], true
}

// ParseYear extracts the first plausible year (minYear..maxYear) from free-text dates
// such as "1922", "March 3, 1922" or "c1922". It returns 0 when none is found.
func ParseYear(s string, maxYear int) int {
	for _, m := range yearRe.FindAllStringSubmatch(s, -1) {
		y, _ := strconv.Atoi(m[1])
		if y >= minYear && y <= maxYear {
			return y
		}
	}

	return 0
}

// IsFiction reports whether any subject mentions fiction, excluding "nonfiction"/"non-fiction".
func IsFiction(subjects []string) bool {
	for _, s := range subjects {
		l := strings.ToLower(s)
		if strings.Contains(l, fictionWord) && !strings.Contains(l, nonfictionWord) && !strings.Contains(l, nonFictionWord) {
			return true
		}
	}

	return false
}

// MentionsFiction is a cheap byte-level prefilter run before JSON decoding.
func MentionsFiction(line []byte) bool {
	return bytes.Contains(line, []byte(fictionLower)) || bytes.Contains(line, []byte(fictionUpper))
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
