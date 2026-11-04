package shopify

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/nem0z/book-finder/internal/shops"
)

var (
	// knownFormats expands a format cut short by the title length limit ("[2021] pa").
	knownFormats = []string{"paperback", "hardback"}

	// editionRe matches the "[yyyy] format" suffix of a title, possibly cut short.
	editionRe = regexp.MustCompile(`\s*\[(\d{4})\]\s*([A-Za-z ]*)$`)
	cutRe     = regexp.MustCompile(`\s*\[[^\]]*$`)
	// shopCodeRe matches a trailing shop code such as ": Tpb W2" or " Z90".
	shopCodeRe = regexp.MustCompile(`(?:[\s:]+(?i:tpb|hb|pb)(?:\s+[A-Za-z]\d{1,3})?|\s+[WZ]\d{1,3})$`)
	// usedSKURe matches a second-hand variant's SKU: X, a letter for the condition, then the ISBN-13 without its
	// leading "97".
	usedSKURe   = regexp.MustCompile(`^X[A-Z](\d{11})$`)
	noLettersRe = regexp.MustCompile(`^[^\pL]*$`)
)

// product is a product node of the Storefront API, as queried by productsQuery.
type product struct {
	ID               string `json:"id"`
	Handle           string `json:"handle"`
	Title            string `json:"title"`
	Vendor           string `json:"vendor"`
	ProductType      string `json:"productType"`
	DescriptionHTML  string `json:"descriptionHtml"`
	OnlineStoreURL   string `json:"onlineStoreUrl"`
	AvailableForSale bool   `json:"availableForSale"`
	FeaturedImage    *struct {
		URL string `json:"url"`
	} `json:"featuredImage"`
	Variants struct {
		Nodes []variant `json:"nodes"`
	} `json:"variants"`
}

type variant struct {
	SKU string `json:"sku"`
}

// parseProduct turns a product of the store at storeURL into a Book. It returns shops.ErrNotBook or
// shops.ErrNoTitle for a product to skip.
func parseProduct(p product, storeURL string) (shops.Book, error) {
	isbn, err := productISBN(p)
	if err != nil {
		return shops.Book{}, err
	}

	authors := shops.AuthorNames(p.Vendor)
	title, year, format := cleanTitle(p.Title, authors)
	if noLettersRe.MatchString(title) {
		return shops.Book{}, shops.ErrNoTitle
	}

	b := shops.Book{
		ISBN13:      isbn,
		Title:       title,
		Authors:     authors,
		Year:        year,
		Format:      format,
		Description: shops.HTMLText(p.DescriptionHTML),
		URL:         p.OnlineStoreURL,
		InStock:     p.AvailableForSale,
	}
	if g := genre(p.ProductType); g != "" {
		b.Genres = []string{g}
	}

	if b.URL == "" {
		b.URL = storeURL + "/products/" + p.Handle
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

		if shops.ValidISBN13(sku) {
			return sku, nil
		}
	}

	return "", shops.ErrNotBook
}

// cleanTitle removes the "[yyyy] format" suffix, shop codes and the authors from a product title, and title-cases
// it when it is in capitals.
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
		t = shops.TitleCase(t)
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

// genre turns a product type ("BARGAIN FICTION PAPERBACK") into a genre in sentence case ("Fiction paperback");
// "bargain" is a price, not a genre.
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
