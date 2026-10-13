package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/nem0z/book-finder/internal/oldump"
)

const (
	// Redirect chains longer than this are treated as loops.
	maxRedirectHops = 10

	worksKeyPrefix  = "/works/"
	authorKeyPrefix = "/authors/"
)

var typeRedirect = []byte("/type/redirect\t")

// redirectStats counts what happened to each redirect line. See docs/redirects.md.
type redirectStats struct {
	Kept         int `json:"kept"`
	FixedTargets int `json:"fixed_targets"` // target had no path; it was added from the ID's letter
	Skipped      int `json:"skipped"`       // not a work or author, malformed, or target of another kind
}

// loadRedirects builds the in-memory map of merged work and author IDs from the optional redirects dump.
func (l *loader) loadRedirects(ctx context.Context) error {
	path := l.opts.Sources.Redirects
	if path == "" {
		l.log.Info("no redirects dump; merged records will not be resolved")
		return nil
	}

	err := oldump.ReadLines(ctx, path, l.progressInterval(), l.progress("redirects"), l.addRedirect)
	l.log.Info("redirects loaded", "kept", l.stats.Redirects.Kept,
		"fixed_targets", l.stats.Redirects.FixedTargets, "skipped", l.stats.Redirects.Skipped)

	return err
}

// addRedirect records one redirect line in canonical form; non-redirect lines are ignored.
func (l *loader) addRedirect(line []byte) (bool, error) {
	if !bytes.HasPrefix(line, typeRedirect) {
		return false, nil
	}

	from, to, fixed, ok := parseRedirect(line)
	if !ok {
		l.stats.Redirects.Skipped++
		return false, nil
	}

	if fixed {
		l.stats.Redirects.FixedTargets++
	}

	l.redirects[from] = to
	l.stats.Redirects.Kept++

	return true, nil
}

// parseRedirect returns the canonical source and target of a work or author redirect, and whether
// the target's path had to be added. Edition redirects are not needed: editions are not stored.
func parseRedirect(line []byte) (from, to string, fixed, ok bool) {
	_, key, data, ok := oldump.SplitRecord(line)
	if !ok {
		return "", "", false, false
	}

	var r struct {
		Location string `json:"location"`
	}

	if json.Unmarshal(data, &r) != nil {
		return "", "", false, false
	}

	from, okFrom := oldump.CanonicalKey(string(key))
	to, okTo := oldump.CanonicalKey(r.Location)
	if !okFrom || !okTo || keyKind(from) != keyKind(to) || !isWorkOrAuthor(from) {
		return "", "", false, false
	}

	return from, to, to != r.Location, true
}

func isWorkOrAuthor(key string) bool {
	return strings.HasPrefix(key, worksKeyPrefix) || strings.HasPrefix(key, authorKeyPrefix)
}

// keyKind returns the path of a canonical key, e.g. "/works/".
func keyKind(key string) string {
	return key[:strings.LastIndexByte(key, '/')+1]
}

// resolve returns the current canonical ID for a work or author ID found in any record: it adds a
// missing path, then follows redirects (merged records). It returns "" when key is not a valid ID.
func (l *loader) resolve(key string) string {
	key, ok := oldump.CanonicalKey(key)
	if !ok {
		return ""
	}

	for range maxRedirectHops {
		next, ok := l.redirects[key]
		if !ok || next == key {
			return key
		}

		key = next
	}

	return key
}
