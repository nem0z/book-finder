package shops

import (
	"errors"
	"html"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

var (
	// ErrNotBook: the product has no valid ISBN-13 (DVDs, games, gifts).
	ErrNotBook = errors.New("not a book: no ISBN-13")
	// ErrNoTitle: nothing usable is left of the product title once cleaned (e.g. only an ISBN).
	ErrNoTitle = errors.New("no title")

	// knownFormats expands a format cut short by the title length limit ("[2021] pa").
	knownFormats = []string{"paperback", "hardback"}

	// editionRe matches the "[yyyy] format" suffix of a title, possibly cut short.
	editionRe = regexp.MustCompile(`\s*\[(\d{4})\]\s*([A-Za-z ]*)$`)
	cutRe     = regexp.MustCompile(`\s*\[[^\]]*$`)
	// shopCodeRe matches a trailing shop code such as ": Tpb W2" or " Z90".
	shopCodeRe = regexp.MustCompile(`(?:[\s:]+(?i:tpb|hb|pb)(?:\s+[A-Za-z]\d{1,3})?|\s+[WZ]\d{1,3})$`)
	// usedSKURe matches a second-hand variant's SKU: X, a letter for the condition, then the ISBN-13
	// without its leading "97".
	usedSKURe   = regexp.MustCompile(`^X[A-Z](\d{11})$`)
	noLettersRe = regexp.MustCompile(`^[^\pL]*$`)
	blockTagRe  = regexp.MustCompile(`(?i)<\s*/?\s*(?:p|br|div|li|ul|ol|h[1-6]|tr|table|blockquote)\b[^>]*>`)
	tagRe       = regexp.MustCompile(`<[^>]*>`)
)

// Book is a book a shop lists, cleaned up from its product.
type Book struct {
	Source      string // e.g. "chapters"
	ISBN13      string
	Title       string
	Authors     []string // "First Last"
	Year        int      // edition year; 0 when unknown
	Format      string   // e.g. "paperback"; empty when unknown
	Genres      []string // the shop's category, in sentence case
	Description string   // plain text, paragraphs separated by a blank line
	URL         string   // product page
	CoverURL    string
}

// product is a product node of the Storefront API, as queried by productsQuery.
type product struct {
	ID              string `json:"id"`
	Handle          string `json:"handle"`
	Title           string `json:"title"`
	Vendor          string `json:"vendor"`
	ProductType     string `json:"productType"`
	DescriptionHTML string `json:"descriptionHtml"`
	OnlineStoreURL  string `json:"onlineStoreUrl"`
	FeaturedImage   *struct {
		URL string `json:"url"`
	} `json:"featuredImage"`
	Variants struct {
		Nodes []variant `json:"nodes"`
	} `json:"variants"`
}

type variant struct {
	SKU string `json:"sku"`
}

// ID returns the book's works id, "<source>:<isbn13>".
func (b Book) ID() string {
	return b.Source + ":" + b.ISBN13
}

// parseProduct turns a product of the shop at baseURL into a Book. It returns ErrNotBook or ErrNoTitle for a
// product to skip.
func parseProduct(p product, source, baseURL string) (Book, error) {
	isbn, err := productISBN(p)
	if err != nil {
		return Book{}, err
	}

	title, year, format := cleanTitle(p.Title, authorNames(p.Vendor))
	if noLettersRe.MatchString(title) {
		return Book{}, ErrNoTitle
	}

	b := Book{
		Source:      source,
		ISBN13:      isbn,
		Title:       title,
		Authors:     authorNames(p.Vendor),
		Year:        year,
		Format:      format,
		Description: htmlText(p.DescriptionHTML),
		URL:         p.OnlineStoreURL,
	}
	if g := genre(p.ProductType); g != "" {
		b.Genres = []string{g}
	}

	if b.URL == "" {
		b.URL = baseURL + "/products/" + p.Handle
	}

	if p.FeaturedImage != nil {
		b.CoverURL = p.FeaturedImage.URL
	}

	return b, nil
}

// productISBN returns the ISBN-13 of the product's new variant (its SKU), or one rebuilt from a second-hand
// variant's SKU.
func productISBN(p product) (string, error) {
	for _, v := range p.Variants.Nodes {
		sku := strings.TrimSpace(v.SKU)
		if m := usedSKURe.FindStringSubmatch(sku); m != nil {
			sku = "97" + m[1]
		}

		if validISBN13(sku) {
			return sku, nil
		}
	}

	return "", ErrNotBook
}

// validISBN13 reports whether s is a 978/979 ISBN-13 with a correct check digit.
func validISBN13(s string) bool {
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

// cleanTitle removes the "[yyyy] format" suffix, shop codes and the authors from a product title, and
// title-cases it when it is in capitals.
func cleanTitle(raw string, authors []string) (title string, year int, format string) {
	t := strings.TrimSpace(raw)
	if m := editionRe.FindStringSubmatch(t); m != nil {
		year, _ = strconv.Atoi(m[1])
		format = strings.ToLower(strings.TrimSpace(m[2]))
		for _, f := range knownFormats {
			if format != "" && strings.HasPrefix(f, format) {
				format = f
			}
		}

		t = t[:len(t)-len(m[0])]
	} else {
		t = cutRe.ReplaceAllString(t, "")
	}

	t = shopCodeRe.ReplaceAllString(t, "")
	if i := strings.Index(t, ":"); i > 0 && isAuthor(t[:i], authors) {
		t = t[i+1:]
	} else if i := strings.LastIndex(t, ":"); i > 0 && isAuthor(t[i+1:], authors) {
		t = t[:i]
	}

	t = strings.Trim(t, " :")
	if strings.IndexFunc(t, unicode.IsUpper) >= 0 && strings.IndexFunc(t, unicode.IsLower) < 0 {
		t = titleCase(t)
	}

	return t, year, format
}

// isAuthor reports whether s names one of the authors, or all of them, with the words in any order.
func isAuthor(s string, authors []string) bool {
	k := nameKey(s)
	if k == "" {
		return false
	}

	if len(authors) > 1 && k == nameKey(strings.Join(authors, " ")) {
		return true
	}

	return slices.ContainsFunc(authors, func(a string) bool { return nameKey(a) == k })
}

// nameKey returns the lower-cased words of a name, sorted, so a name matches in either order.
func nameKey(name string) string {
	words := strings.FieldsFunc(strings.ToLower(name), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	slices.Sort(words)

	return strings.Join(words, " ")
}

func titleCase(s string) string {
	words := strings.Fields(strings.ToLower(s))
	for i, w := range words {
		r := []rune(w)
		r[0] = unicode.ToUpper(r[0])
		words[i] = string(r)
	}

	return strings.Join(words, " ")
}

// authorNames turns a vendor ("Last, First", several joined by "&") into names ("First Last"); "Unknown" is
// no author.
func authorNames(vendor string) []string {
	var names []string
	for v := range strings.SplitSeq(vendor, "&") {
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

// genre turns a product type ("BARGAIN FICTION PAPERBACK") into a genre in sentence case ("Fiction
// paperback"); "bargain" is a price, not a genre.
func genre(productType string) string {
	g := strings.ToLower(strings.TrimSpace(productType))
	g = strings.TrimSpace(strings.TrimPrefix(g, "bargain "))
	if g == "" {
		return ""
	}

	r := []rune(g)
	r[0] = unicode.ToUpper(r[0])

	return string(r)
}

// htmlText turns a description's HTML into plain text: block tags become paragraph breaks, other tags are
// dropped, entities are decoded and spaces collapsed.
func htmlText(s string) string {
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
