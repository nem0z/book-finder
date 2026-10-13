package oldump

import (
	"bytes"
	"strconv"
	"strings"
)

const (
	recordColumns = 5 // type, key, revision, last_modified, JSON

	// fictionWord marks a fiction subject when it appears anywhere in it, in any case:
	// "Fiction", "Juvenile fiction", "Fiction, Romance, General".
	fictionWord = "fiction"

	isbn10Length    = 10
	isbn13Length    = 13
	isbnCheckDigitX = 'X'

	yearDigits = 4

	workIDSuffix = 'W'
)

var (
	worksRefPrefix = []byte(`"` + WorksPath + idPrefix)

	// Subjects that contain the fiction word but describe non-fiction: "Juvenile nonfiction" (about 42k works
	// with an English edition and a description in the 2026-09-30 dump), and books about fiction such as
	// "American fiction, History and criticism" or "Fiction, Technique".
	nonFictionMarkers = []string{"nonfiction", "non-fiction", "non fiction"}
	aboutFiction      = []string{"criticism", "authorship", "technique", "study and teaching", "bibliography",
		"appreciation", "fiction writing"}

	// Byte prefilter: the fiction word's first letter in both cases, used to find candidates quickly.
	fictionFirst = []byte{fictionWord[0], fictionWord[0] - 'a' + 'A'}
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

// IsFiction reports whether any subject is a fiction subject (see isFictionSubject). A work that
// also has a non-fiction subject still counts.
func IsFiction(subjects []string) bool {
	for _, s := range subjects {
		if isFictionSubject(s) {
			return true
		}
	}

	return false
}

// isFictionSubject reports whether s contains the fiction word and is not a non-fiction subject.
func isFictionSubject(s string) bool {
	s = strings.ToLower(s)
	if !strings.Contains(s, fictionWord) {
		return false
	}

	for _, m := range nonFictionMarkers {
		if strings.Contains(s, m) {
			return false
		}
	}

	for _, m := range aboutFiction {
		if strings.Contains(s, m) {
			return false
		}
	}

	return true
}

// MentionsFiction is a cheap byte-level prefilter run before JSON decoding: it looks for the fiction
// word in any case ("FIction" is in the dump). It may accept lines without a fiction subject (the
// word in a description); IsFiction decides.
func MentionsFiction(line []byte) bool {
	for _, first := range fictionFirst {
		for rest := line; ; {
			i := bytes.IndexByte(rest, first)
			if i < 0 || len(rest)-i < len(fictionWord) {
				break
			}

			if bytes.EqualFold(rest[i:i+len(fictionWord)], []byte(fictionWord)) {
				return true
			}

			rest = rest[i+1:]
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
