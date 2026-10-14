package ingest

import (
	"bytes"
	"context"
	"strconv"

	"github.com/nem0z/book-finder/internal/oldump"
)

const (
	ratingsColumns = 4 // work key, edition key or \N, rating, date
	minRating      = 1
	maxRating      = 5
)

// loadRatings adds each rating of a candidate work to its aggregate in memory. The ratings dump is
// small (about 1M lines) and has no JSON, so it is read on one goroutine. It is optional.
func (l *loader) loadRatings(ctx context.Context) error {
	path := l.opts.Sources.Ratings
	if path == "" {
		l.log.Info("no ratings dump; works will have no ratings")
		return nil
	}

	err := oldump.ReadLines(ctx, path, l.progressInterval(), l.progress("ratings"), l.addRating)
	l.log.Info("ratings read", "ratings", l.stats.Ratings)

	return err
}

// addRating records one ratings line. Lines for other works, or that cannot be read, are skipped.
func (l *loader) addRating(line []byte) (bool, error) {
	cols := bytes.SplitN(line, []byte{'\t'}, ratingsColumns)
	if len(cols) != ratingsColumns {
		return false, nil
	}

	rating, err := strconv.Atoi(string(cols[2]))
	if err != nil || rating < minRating || rating > maxRating {
		return false, nil
	}

	key, err := l.redirects.Resolve(string(cols[0]))
	if err != nil {
		return false, nil
	}

	a, ok := l.works[oldump.ShortKey(key)]
	if !ok {
		return false, nil
	}

	a.ratingSum += int32(rating)
	a.ratingCount++
	l.stats.Ratings++

	return true, nil
}

// ratingAvg returns the average rating, or 0 when the work has none.
func (a *workAgg) ratingAvg() float32 {
	if a.ratingCount == 0 {
		return 0
	}

	return float32(a.ratingSum) / float32(a.ratingCount)
}
