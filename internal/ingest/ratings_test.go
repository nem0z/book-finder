package ingest

import "testing"

func TestLoadRatings(t *testing.T) {
	dir := t.TempDir()
	l := newLoader(nil, Options{Sources: Sources{Ratings: ratingsFixture(t, dir), Redirects: redirectsFixture(t, dir)},
		ProgressInterval: -1})
	l.works = map[string]*workAgg{"OL1W": {}, "OL4W": {}, "OL6W": {}}
	loadThrough(t, l, l.loadRedirects, l.loadRatings)

	if l.stats.Ratings != 3 {
		t.Errorf("ratings: %d", l.stats.Ratings)
	}

	if a := l.works["OL1W"]; a.ratingCount != 2 || a.ratingAvg() != 4.5 {
		t.Errorf("OL1W: %d ratings, average %v", a.ratingCount, a.ratingAvg())
	}

	if a := l.works["OL6W"]; a.ratingCount != 0 || a.ratingAvg() != 0 {
		t.Errorf("OL6W: %d ratings, average %v", a.ratingCount, a.ratingAvg())
	}
}

func TestLoadRatingsOptional(t *testing.T) {
	l := newLoader(nil, Options{ProgressInterval: -1})
	loadThrough(t, l, l.loadRatings)
}
