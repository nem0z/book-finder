package bench

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nem0z/book-finder/internal/llm"
	"github.com/nem0z/book-finder/internal/search"
)

const (
	judgeDescriptionBytes = 2000
	defaultJudgeWorkers   = 4

	// minJudgeFailures is how many failed ratings are tolerated before checking whether most fail, which
	// means the LLM settings are wrong rather than a few odd answers.
	minJudgeFailures = 10
)

var ErrTooManyFailures = errors.New("most ratings fail; check the LLM settings")

// Judgment is one cached rating, a line of the cache file.
type Judgment struct {
	Key        string         `json:"key"`
	Query      string         `json:"query"`
	WorkID     string         `json:"work_id"`
	Title      string         `json:"title"`
	Rating     int            `json:"rating"`
	Dimensions map[string]int `json:"dimensions,omitempty"`
	Reason     string         `json:"reason"`
}

// Judge rates books as recommendations for queries with an LLM. Ratings are appended to a JSONL cache file,
// so each (query, book) pair is rated once, across runs.
type Judge struct {
	client llm.Client
	model  string
	// Workers is how many ratings run at once; 0 means 4. PerMinute caps the ratings asked per minute;
	// 0 means no cap.
	Workers, PerMinute int
	// DryRun counts the ratings that would be asked (Pending) instead of asking them.
	DryRun  bool
	Pending int
	Logger  *slog.Logger

	mu     sync.Mutex
	cache  map[string]Judgment
	file   *os.File
	asked  int
	failed int
}

// OpenJudge returns a judge asking client (model is its model, part of the cache key) and caching ratings in
// the file at path, created if needed.
func OpenJudge(client llm.Client, model, path string) (*Judge, error) {
	f, judgments, err := openJSONL[Judgment](path)
	if err != nil {
		return nil, err
	}

	j := &Judge{client: client, model: model, cache: map[string]Judgment{}, file: f}
	for _, jd := range judgments {
		j.cache[jd.Key] = jd
	}

	return j, nil
}

func (j *Judge) Close() error { return j.file.Close() }

// Stats returns how many ratings are cached, and how many were asked and failed by this judge.
func (j *Judge) Stats() (cached, asked, failed int) {
	j.mu.Lock()
	defer j.mu.Unlock()

	return len(j.cache), j.asked, j.failed
}

// Judgment returns the cached judgment of book for query, if there is one.
func (j *Judge) Judgment(query string, book search.Result) (Judgment, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()

	jd, ok := j.cache[j.key(query, book)]

	return jd, ok
}

// RateAll rates the books not rated yet for query. A failed rating is logged and skipped; it stops with
// ErrTooManyFailures when most fail.
func (j *Judge) RateAll(ctx context.Context, query string, books []search.Result) error {
	var todo []search.Result
	for _, b := range books {
		if _, ok := j.Judgment(query, b); !ok {
			todo = append(todo, b)
		}
	}

	if j.DryRun {
		j.Pending += len(todo)
		return nil
	}

	var tick <-chan time.Time
	if j.PerMinute > 0 {
		t := time.NewTicker(time.Minute / time.Duration(j.PerMinute))
		defer t.Stop()
		tick = t.C
	}

	workers := j.Workers
	if workers <= 0 {
		workers = defaultJudgeWorkers
	}

	jobs := make(chan search.Result)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for b := range jobs {
				j.rateOne(ctx, query, b)
			}
		})
	}

	err := feed(ctx, jobs, todo, tick)
	close(jobs)
	wg.Wait()
	if err != nil {
		return err
	}

	_, asked, failed := j.Stats()
	if failed >= minJudgeFailures && failed > asked-failed {
		return fmt.Errorf("%w: %d of %d failed", ErrTooManyFailures, failed, asked)
	}

	return nil
}

func (j *Judge) rateOne(ctx context.Context, query string, b search.Result) {
	var a struct {
		Rating     int            `json:"rating"`
		Dimensions map[string]int `json:"dimensions"`
		Reason     string         `json:"reason"`
	}

	err := j.client.JSON(ctx, prompt, judgeMessage(query, b), schemaName, schema, &a)
	if err == nil {
		err = checkAnswer(a.Rating, a.Dimensions)
	}

	j.mu.Lock()
	defer j.mu.Unlock()

	j.asked++
	if err != nil {
		if ctx.Err() == nil {
			j.failed++
			j.logger().Warn("rating failed", "work", b.ID, "err", err)
		}

		return
	}

	jd := Judgment{Key: j.key(query, b), Query: query, WorkID: b.ID, Title: b.Title, Rating: a.Rating, Dimensions: a.Dimensions, Reason: a.Reason}
	line, _ := json.Marshal(jd)
	if _, err := j.file.Write(append(line, '\n')); err != nil {
		j.logger().Warn("cache write failed", "err", err)
	}

	j.cache[jd.Key] = jd
}

// key identifies a rating: the model, prompt version, query and what the judge reads of the book.
func (j *Judge) key(query string, b search.Result) string {
	h := sha256.Sum256([]byte(strings.Join([]string{j.model, PromptVersion, query, b.ID, judgeMessage(query, b)}, "\x00")))
	return hex.EncodeToString(h[:16])
}

func (j *Judge) logger() *slog.Logger {
	if j.Logger == nil {
		return slog.Default()
	}

	return j.Logger
}

// judgeMessage is what the judge reads: the query, then the book.
func judgeMessage(query string, b search.Result) string {
	var s strings.Builder
	s.WriteString("Search: " + query + "\n\nBook: " + b.Title + "\n")
	if len(b.Authors) > 0 {
		s.WriteString("Authors: " + strings.Join(b.Authors, ", ") + "\n")
	}

	if b.Year > 0 {
		s.WriteString("First published: " + strconv.Itoa(b.Year) + "\n")
	}

	s.WriteString("Description:\n" + cutText(strings.TrimSpace(b.Description), judgeDescriptionBytes))

	return s.String()
}

// feed sends books to jobs, one per tick when tick is set, until ctx ends.
func feed(ctx context.Context, jobs chan<- search.Result, books []search.Result, tick <-chan time.Time) error {
	for _, b := range books {
		if tick != nil {
			select {
			case <-tick:
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		select {
		case jobs <- b:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	return nil
}

// cutText shortens s to at most max bytes, at a space when possible.
func cutText(s string, max int) string {
	if len(s) <= max {
		return s
	}

	if i := strings.LastIndexByte(s[:max], ' '); i > 0 {
		max = i
	}

	return strings.ToValidUTF8(s[:max], "")
}

// openJSONL opens the JSONL file at path for appending, creating it if needed, and decodes its lines.
func openJSONL[T any](path string) (*os.File, []T, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return nil, nil, err
	}

	var out []T
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for line := 1; sc.Scan(); line++ {
		var v T
		if err := json.Unmarshal(sc.Bytes(), &v); err != nil {
			f.Close()
			return nil, nil, fmt.Errorf("%s:%d: %w", path, line, err)
		}

		out = append(out, v)
	}

	if err := sc.Err(); err != nil {
		f.Close()
		return nil, nil, err
	}

	return f, out, nil
}
