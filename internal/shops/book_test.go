package shops

import (
	"errors"
	"reflect"
	"testing"
)

func TestCleanTitle(t *testing.T) {
	for _, tc := range []struct {
		raw, vendor, title string
		year               int
		format             string
	}{
		{"Professor Lloyd Llewellyn-Jones: Persians: Tpb W2 [2022] paperback", "Llewellyn-Jones, Professor Lloyd", "Persians", 2022, "paperback"},
		{"Where the Crawdads Sing: Delia Owens [2019] paperback", "Owens, Delia", "Where the Crawdads Sing", 2019, "paperback"},
		{"Gabrielle Zevin: Tomorrow, and Tomorrow, and Tomorrow [2023] paperback", "Zevin, Gabrielle", "Tomorrow, and Tomorrow, and Tomorrow", 2023, "paperback"},
		{"Colleen Hoover: 9780008742775 [2024] paperback", "Hoover, Colleen", "9780008742775", 2024, "paperback"},
		{"Virginia Evans: The Correspondent [2026]", "Evans, Virginia", "The Correspondent", 2026, ""},
		{"Knapp Florence: The Names [2026]", "Florence Knapp", "The Names", 2026, ""},
		{"McConaghy Charlotte: Wild Dark Shore [2025]", "Mcconaghy, Charlotte", "Wild Dark Shore", 2025, ""},
		{"Fannie Flagg: CANT WAIT TO GET TO HEAVEN [2007] paperback", "Flagg, Fannie", "Cant Wait To Get To Heaven", 2007, "paperback"},
		{"Bridgerton: To Sir Phillip, With Love (Bridgertons Book 5): Inspiration for the Netflix Original Series Bridgerton: Eloise's story (Bridgerton Family) [2021] pa", "Quinn, Julia",
			"Bridgerton: To Sir Phillip, With Love (Bridgertons Book 5): Inspiration for the Netflix Original Series Bridgerton: Eloise's story (Bridgerton Family)", 2021, "paperback"},
		{"Some Very Long Title That Was Cut [20", "Unknown", "Some Very Long Title That Was Cut", 0, ""},
		{"Anna Burns: Milkman [2018] hardback", "", "Anna Burns: Milkman", 2018, "hardback"},
		{"Confusion Of Karen Carpenter Z90 [2013] paperback", "Harvey, Jonathan", "Confusion Of Karen Carpenter", 2013, "paperback"},
		{"Agatha Christie: They Do It With Mirrors [2003]", "Christie, Agatha & Hickson, Joan", "They Do It With Mirrors", 2003, ""},
		{"Catch-22 [1994] paperback", "Heller, Joseph", "Catch-22", 1994, "paperback"},
	} {
		title, year, format := cleanTitle(tc.raw, authorNames(tc.vendor))
		if title != tc.title || year != tc.year || format != tc.format {
			t.Errorf("%q: got %q, %d, %q", tc.raw, title, year, format)
		}
	}
}

func TestProductISBN(t *testing.T) {
	for _, tc := range []struct {
		skus []string
		want string
	}{
		{[]string{"9781472277299", "XG81472277299"}, "9781472277299"},
		{[]string{"XV81472277299"}, "9781472277299"},
		{[]string{" 9780008742775 "}, "9780008742775"},
		{[]string{"4260110581646"}, ""}, // a DVD's EAN
		{[]string{"9781472277298"}, ""}, // bad check digit
		{[]string{"XG81472277298"}, ""}, // bad check digit
		{[]string{"978147227729X"}, ""}, // not digits
		{nil, ""},
	} {
		var p product
		for _, s := range tc.skus {
			p.Variants.Nodes = append(p.Variants.Nodes, variant{s})
		}

		got, err := productISBN(p)
		if got != tc.want || (tc.want == "") != errors.Is(err, ErrNotBook) {
			t.Errorf("%q: got %q, %v", tc.skus, got, err)
		}
	}
}

func TestAuthorNames(t *testing.T) {
	for in, want := range map[string][]string{
		"Owens, Delia":                     {"Delia Owens"},
		"Llewellyn-Jones, Professor Lloyd": {"Professor Lloyd Llewellyn-Jones"},
		"Florence Knapp":                   {"Florence Knapp"},
		"Christie, Agatha & Hickson, Joan": {"Agatha Christie", "Joan Hickson"},
		"Unknown":                          nil,
		" ":                                nil,
		"Homer,":                           {"Homer"},
	} {
		if got := authorNames(in); !reflect.DeepEqual(got, want) {
			t.Errorf("%q: got %q", in, got)
		}
	}
}

func TestGenre(t *testing.T) {
	for in, want := range map[string]string{
		"FICTION PAPERBACK":         "Fiction paperback",
		"BARGAIN FICTION PAPERBACK": "Fiction paperback",
		"IRISH FICTION":             "Irish fiction",
		"BOOKTOK":                   "Booktok",
		"BARGAIN HISTORY":           "History",
		"":                          "",
	} {
		if got := genre(in); got != want {
			t.Errorf("%q: got %q", in, got)
		}
	}
}

func TestHTMLText(t *testing.T) {
	in := "<p></p><b>THE PERSIANS is a definitive new history.</b><p></p><p></p>The Great Kings of Persia&nbsp;ruled.<p></p>  <p></p>Herodotus &amp; others<br/>wrote  about it."
	want := "THE PERSIANS is a definitive new history.\n\nThe Great Kings of Persia ruled.\n\nHerodotus & others\n\nwrote about it."
	if got := htmlText(in); got != want {
		t.Errorf("got %q", got)
	}
}

func TestParseProduct(t *testing.T) {
	p := product{
		Handle:          "persians",
		Title:           "Professor Lloyd Llewellyn-Jones: Persians: Tpb W2 [2022] paperback",
		Vendor:          "Llewellyn-Jones, Professor Lloyd",
		ProductType:     "HISTORY",
		DescriptionHTML: "<p>A history.</p>",
	}
	p.Variants.Nodes = []variant{{"9781472277299"}}

	got, err := parseProduct(p, "chapters", "https://shop.example")
	want := Book{
		Source: "chapters", ISBN13: "9781472277299", Title: "Persians", Authors: []string{"Professor Lloyd Llewellyn-Jones"},
		Year: 2022, Format: "paperback", Genres: []string{"History"}, Description: "A history.",
		URL: "https://shop.example/products/persians",
	}
	if err != nil || !reflect.DeepEqual(got, want) || got.ID() != "chapters:9781472277299" {
		t.Errorf("got %+v, %v", got, err)
	}

	p.Title, p.Vendor = "Colleen Hoover: 9780008742775 [2024] paperback", "Hoover, Colleen"
	if _, err := parseProduct(p, "chapters", ""); !errors.Is(err, ErrNoTitle) {
		t.Errorf("ISBN as title: got %v", err)
	}

	p.Title, p.Vendor, p.Variants.Nodes[0].SKU = "German Wehrmacht Eastern Front Dvd Z11", "Unknown", "4260110581646"
	if _, err := parseProduct(p, "chapters", ""); !errors.Is(err, ErrNotBook) {
		t.Errorf("DVD: got %v", err)
	}
}
