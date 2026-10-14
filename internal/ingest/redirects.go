package ingest

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/nem0z/book-finder/internal/oldump"
)

// redirectStats counts redirect lines kept and skipped (malformed line, invalid key, or a target of another kind).
type redirectStats struct {
	Kept    int `json:"kept"`
	Skipped int `json:"skipped"`
}

// loadRedirects fills l.redirects from the optional redirects dump.
func (l *loader) loadRedirects(ctx context.Context) error {
	path := l.opts.Sources.Redirects
	if path == "" {
		l.log.Info("no redirects dump; merged records will not be resolved")
		return nil
	}

	err := oldump.ReadLines(ctx, path, l.progressInterval(), l.progress("redirects"), l.addRedirect)
	l.log.Info("redirects loaded", "kept", l.stats.Redirects.Kept, "skipped", l.stats.Redirects.Skipped)

	return err
}

// addRedirect registers one redirect line; other record types are ignored.
func (l *loader) addRedirect(line []byte) (bool, error) {
	if !bytes.HasPrefix(line, typeRedirect) {
		return false, nil
	}

	_, key, data, ok := oldump.SplitRecord(line)

	var r struct {
		Location string `json:"location"`
	}

	if !ok || json.Unmarshal(data, &r) != nil {
		l.stats.Redirects.Skipped++
		return false, nil
	}

	if err := l.redirects.Add(string(key), r.Location); err != nil {
		l.stats.Redirects.Skipped++
		l.log.Debug("redirect skipped", "err", err)
		return false, nil
	}

	l.stats.Redirects.Kept++

	return true, nil
}
