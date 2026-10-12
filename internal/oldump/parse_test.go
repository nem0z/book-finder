package oldump

import "testing"

func TestSplitRecord(t *testing.T) {
	typ, key, data, ok := SplitRecord([]byte("/type/work\t/works/OL1W\t3\t2026-01-01T00:00:00\t{\"a\": \"b\\tc\"}"))
	if !ok || string(typ) != "/type/work" || string(key) != "/works/OL1W" || string(data) != `{"a": "b\tc"}` {
		t.Errorf("got %q %q %q %v", typ, key, data, ok)
	}

	if _, _, _, ok := SplitRecord([]byte("too\tfew\tcolumns")); ok {
		t.Error("short line should not split")
	}
}

func TestParseYear(t *testing.T) {
	for in, want := range map[string]int{
		"1922": 1922, " 1922 ": 1922, "850": 850, "March 3, 1922": 0, "c1922": 0, "19--": 0, "": 0,
		"3000": 0, "-50": 0,
	} {
		if got := ParseYear(in, 2027); got != want {
			t.Errorf("%q: got %d want %d", in, got, want)
		}
	}
}

func TestIsFiction(t *testing.T) {
	cases := []struct {
		subjects []string
		want     bool
	}{
		{[]string{"Fiction"}, true},
		{[]string{"Thrillers", " fiction "}, true},
		{[]string{"Detective and mystery fiction"}, false},
		{[]string{"Juvenile nonfiction"}, false},
		{[]string{"Cooking"}, false},
		{nil, false},
	}

	for _, c := range cases {
		if got := IsFiction(c.subjects); got != c.want {
			t.Errorf("%v: got %v", c.subjects, got)
		}
	}

	if !MentionsFiction([]byte(`"subjects": ["Fiction"]`)) || !MentionsFiction([]byte(`["fiction"]`)) ||
		MentionsFiction([]byte(`["Science fiction"]`)) {
		t.Error("MentionsFiction")
	}
}

func TestEditionWorkKey(t *testing.T) {
	if k, ok := EditionWorkKey([]byte(`{"key": "/books/OL1M", "works": [{"key": "/works/OL9W"}]}`)); !ok || k != "/works/OL9W" {
		t.Errorf("got %q %v", k, ok)
	}

	if _, ok := EditionWorkKey([]byte(`{"key": "/books/OL1M"}`)); ok {
		t.Error("should fail without works")
	}
}

func TestNormalizeISBNAndClean(t *testing.T) {
	if got := NormalizeISBN("978-0-14-303943-3"); got != "9780143039433" {
		t.Errorf("isbn13: %q", got)
	}

	if got := NormalizeISBN("0 14 303943 x"); got != "014303943X" {
		t.Errorf("isbn10: %q", got)
	}

	if got := NormalizeISBN("123"); got != "" {
		t.Errorf("short: %q", got)
	}

	if got := Clean("  a\x00b \n"); got != "ab" {
		t.Errorf("clean: %q", got)
	}
}
