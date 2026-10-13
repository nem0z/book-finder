package oldump

import "testing"

func TestCanonicalKey(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"/works/OL45804W", "/works/OL45804W", true},
		{"/authors/OL1A", "/authors/OL1A", true},
		{"/books/OL7M", "/books/OL7M", true},
		{"OL4791619A", "/authors/OL4791619A", true}, // path-less target: path added from the letter
		{"OL2329W", "/works/OL2329W", true},
		{"OL12M", "/books/OL12M", true},
		{"\t/authors/OL3281458A", "/authors/OL3281458A", true}, // stray whitespace, seen once in the dump
		{" OL5W ", "/works/OL5W", true},
		{"/authors/OL24320672W", "", false}, // letter does not match the path
		{"/authors/OL123", "", false},       // missing letter
		{"/books/ia:cihm_12345", "", false}, // Internet Archive identifier
		{"/about/team", "", false},
		{"/olcc123", "", false},
		{"/import-history", "", false},
		{"OL12X", "", false},
		{"OLW", "", false},
		{"/works/OLxW", "", false},
		{"", "", false},
	}

	for _, c := range cases {
		got, ok := CanonicalKey(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("%q: got %q %v, want %q %v", c.in, got, ok, c.want, c.ok)
		}
	}
}
