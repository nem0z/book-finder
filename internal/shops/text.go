package shops

import (
	"html"
	"regexp"
	"strings"
	"unicode"
)

var (
	blockTagRe = regexp.MustCompile(`(?i)<\s*/?\s*(?:p|br|div|li|ul|ol|h[1-6]|tr|table|blockquote)\b[^>]*>`)
	tagRe      = regexp.MustCompile(`<[^>]*>`)
)

// ValidISBN13 reports whether s is a 978/979 ISBN-13 with a correct check digit.
func ValidISBN13(s string) bool {
	if len(s) != 13 || !(strings.HasPrefix(s, "978") || strings.HasPrefix(s, "979")) {
		return false
	}

	sum := 0
	for i, r := range s {
		if r < '0' || r > '9' {
			return false
		}

		sum += int(r-'0') * (1 + 2*(i%2))
	}

	return sum%10 == 0
}

// HTMLText turns HTML into plain text: block tags become paragraph breaks, other tags are dropped, entities are
// decoded and spaces collapsed.
func HTMLText(s string) string {
	s = blockTagRe.ReplaceAllString(s, "\n")
	s = html.UnescapeString(tagRe.ReplaceAllString(s, ""))

	var paras []string
	for line := range strings.SplitSeq(s, "\n") {
		if p := strings.Join(strings.Fields(line), " "); p != "" {
			paras = append(paras, p)
		}
	}

	return strings.Join(paras, "\n\n")
}

// AuthorNames turns a list of names joined by "&" or ";", each "Last, First" or "First Last", into "First Last"
// names; "Unknown" is no author.
func AuthorNames(s string) []string {
	var names []string
	for v := range strings.FieldsFuncSeq(s, func(r rune) bool { return r == '&' || r == ';' }) {
		v = strings.TrimSpace(v)
		if v == "" || strings.EqualFold(v, "unknown") {
			continue
		}

		if last, first, ok := strings.Cut(v, ","); ok {
			v = strings.TrimSpace(strings.TrimSpace(first) + " " + strings.TrimSpace(last))
		}

		names = append(names, v)
	}

	return names
}

// TitleCase capitalises the first letter of each word of s and lowers the others.
func TitleCase(s string) string {
	words := strings.Fields(strings.ToLower(s))
	for i, w := range words {
		r := []rune(w)
		r[0] = unicode.ToUpper(r[0])
		words[i] = string(r)
	}

	return strings.Join(words, " ")
}
