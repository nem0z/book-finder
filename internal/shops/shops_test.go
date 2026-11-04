package shops

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"testing"
)

// fakeCatalog serves pages of items, then fails with err when set.
type fakeCatalog struct {
	pages [][]Item
	err   error
}

func (f fakeCatalog) List(_ context.Context, page func([]Item) error) error {
	for _, p := range f.pages {
		if err := page(p); err != nil {
			return err
		}
	}

	return f.err
}

func book(isbn, title string) Item {
	return Item{Book: Book{ISBN13: isbn, Title: title}}
}

func TestWalk(t *testing.T) {
	r := Reader{Shop: Shop{ID: "test"}, Client: NewClient(), Catalog: fakeCatalog{pages: [][]Item{
		{book("9781472277299", "Persians"), {Err: ErrNotBook}},
		{{Err: ErrNoTitle}, book("9781472277299", "Again"), book("9780008742775", "Verity")},
	}}}

	var got []string
	stats, err := r.Walk(context.Background(), func(books []Book, s Stats) error {
		for _, b := range books {
			got = append(got, b.ID()+" "+b.Title)
		}

		return nil
	})

	want := Stats{Products: 5, Books: 2, NotBooks: 1, Untitled: 1, Duplicates: 1}
	if err != nil || fmt.Sprint(got) != "[test:9781472277299 Persians test:9780008742775 Verity]" || stats != want {
		t.Errorf("got %q, %+v, %v", got, stats, err)
	}
}

func TestWalkErrors(t *testing.T) {
	broken := errors.New("broken")
	stop := errors.New("stop")
	for name, tc := range map[string]struct {
		catalog fakeCatalog
		fn      func([]Book, Stats) error
		want    error
	}{
		"catalog":       {fakeCatalog{err: broken}, nil, broken},
		"product":       {fakeCatalog{pages: [][]Item{{{Err: broken}}}}, nil, broken},
		"callback stop": {fakeCatalog{pages: [][]Item{{book("9781472277299", "Persians")}}}, func([]Book, Stats) error { return stop }, stop},
	} {
		fn := tc.fn
		if fn == nil {
			fn = func([]Book, Stats) error { return nil }
		}

		r := Reader{Shop: Shop{ID: "test"}, Client: NewClient(), Catalog: tc.catalog}
		if _, err := r.Walk(context.Background(), fn); !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v", name, err)
		}
	}
}

// TestShops checks the registry: unique IDs that can be a works source, a name, a store and a platform.
func TestShops(t *testing.T) {
	idRe := regexp.MustCompile(`^[a-z0-9]+$`)
	seen := map[string]bool{}
	for _, s := range All {
		if !idRe.MatchString(s.ID) || seen[s.ID] || s.ID == "openlibrary" || s.Name == "" || s.URL == "" || s.Platform == "" {
			t.Errorf("bad shop %+v", s)
		}

		seen[s.ID] = true
		if got, err := Find(s.ID); err != nil || !reflect.DeepEqual(got, s) {
			t.Errorf("find %s: %+v, %v", s.ID, got, err)
		}
	}

	if _, err := Find("hodgesfiggis"); !errors.Is(err, ErrUnknownShop) {
		t.Errorf("unknown shop: %v", err)
	}
}

func TestValidISBN13(t *testing.T) {
	for in, want := range map[string]bool{
		"9781472277299": true, "9798891606166": true, "9781472277298": false, "4260110581646": false,
		"978147227729X": false, "978147227729": false, "": false,
	} {
		if got := ValidISBN13(in); got != want {
			t.Errorf("%q: got %v", in, got)
		}
	}
}

func TestAuthorNames(t *testing.T) {
	for in, want := range map[string][]string{
		"Owens, Delia":                     {"Delia Owens"},
		"Llewellyn-Jones, Professor Lloyd": {"Professor Lloyd Llewellyn-Jones"},
		"Florence Knapp":                   {"Florence Knapp"},
		"Christie, Agatha & Hickson, Joan": {"Agatha Christie", "Joan Hickson"},
		"Terry Pratchett; Neil Gaiman":     {"Terry Pratchett", "Neil Gaiman"},
		"Unknown":                          nil,
		" ":                                nil,
		"Homer,":                           {"Homer"},
	} {
		if got := AuthorNames(in); !reflect.DeepEqual(got, want) {
			t.Errorf("%q: got %q", in, got)
		}
	}
}

func TestHTMLText(t *testing.T) {
	in := "<p></p><b>THE PERSIANS is a definitive new history.</b><p></p><p></p>The Great Kings of Persia&nbsp;ruled.<p></p>  <p></p>Herodotus &amp; others<br/>wrote  about it."
	want := "THE PERSIANS is a definitive new history.\n\nThe Great Kings of Persia ruled.\n\nHerodotus & others\n\nwrote about it."
	if got := HTMLText(in); got != want {
		t.Errorf("got %q", got)
	}
}

func TestTitleCase(t *testing.T) {
	if got := TitleCase("CANT WAIT TO GET TO HEAVEN"); got != "Cant Wait To Get To Heaven" {
		t.Errorf("got %q", got)
	}
}
