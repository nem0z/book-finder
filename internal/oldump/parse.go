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

	yearDigits = 4

	workIDSuffix = 'W'
)

var (
	worksRefPrefix = []byte(`"/works/OL`)

	// Byte prefilter: the fiction subject as a quoted JSON string, in the spellings seen in the dumps.
	fictionQuoted = [][]byte{
		[]byte(`"` + fictionSubject + `"`),
		[]byte(`"` + strings.ToLower(fictionSubject) + `"`),
		[]byte(`"` + strings.ToUpper(fictionSubject) + `"`),
	}
)

// SplitRecord splits a dump line into its type, key and JSON columns.
func SplitRecord(line []byte) (typ, key, data []byte, ok bool) {
	parts := bytes.SplitN(line, []byte{'\t'}, recordColumns)
	if len(parts) != recordColumns {
		return nil, nil, nil, false
	}

	return parts[0], parts[1], parts[recordColumns-1], true
}

// ParseYear returns the year in a date: s itself when it is a positive integer ("1922", "850"),
// else the first standalone 4-digit number in it ("March 3, 1922", "c1922", "[1985?]").
// It returns 0 when no year is found or the year is after maxYear.
func ParseYear(s string, maxYear int) int {
	s = strings.TrimSpace(s)
	y, err := strconv.Atoi(s)
	if err != nil {
		y = firstYearInText(s)
	}

	if y <= 0 || y > maxYear {
		return 0
	}

	return y
}

// firstYearInText returns the first run of exactly 4 digits in s, or 0.
func firstYearInText(s string) int {
	for i := 0; i < len(s); {
		if !isDigit(s[i]) {
			i++
			continue
		}

		j := i
		for j < len(s) && isDigit(s[j]) {
			j++
		}

		if j-i == yearDigits {
			y, _ := strconv.Atoi(s[i:j])
			return y
		}

		i = j
	}

	return 0
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

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
	for _, q := range fictionQuoted {
		if bytes.Contains(line, q) {
			return true
		}
	}

	return false
}

// EditionWorkKey finds the first work key in an edition's raw JSON without decoding it,
// returning e.g. "/works/OL45804W". The key ends at its W, so an escaped key inside HTML text
// (`\"/works/OL1W\"`) is read without the backslash.
func EditionWorkKey(data []byte) (string, bool) {
	i := bytes.Index(data, worksRefPrefix)
	if i < 0 {
		return "", false
	}

	start := i + 1
	j := i + len(worksRefPrefix)
	for j < len(data) && isDigit(data[j]) {
		j++
	}

	if j == i+len(worksRefPrefix) || j >= len(data) || data[j] != workIDSuffix {
		return "", false
	}

	return string(data[start : j+1]), true
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
