package bench

import (
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
	// JudgePromptVersion is part of the cache key: changing the prompt means bumping it.
	JudgePromptVersion = "1"

	// MaxRating is the best rating JudgePrompt gives.
	MaxRating = 3

	// JudgePrompt asks for a graded relevance rating. The judge sees the book as a reader would (title,
	// authors, year, description), never the keywords or the vector a retrieval used.
	JudgePrompt = `You rate how well a book matches a reader's search for a book to read. You see the search and one book from a catalogue of fiction: its title, authors, year of first publication and description.

Rate from 0 to 3:
3: strong match: the book is what the search describes, or fits all its main elements (story, setting and period, themes, mood, kind of book).
2: reasonable suggestion: it fits the central element or most main elements, with some differences.
1: weak match: it shares a minor element (the same place, genre or a theme) but misses what the search is mainly about.
0: not relevant.

A book that contradicts an explicit requirement of the search (a period, a place, a length, an age group) gets 1 at most. Judge from the information given; do not reward a book for being famous.
Return the rating and a reason of at most 20 words.`

	judgeSchemaName       = "relevance"
	judgeDescriptionBytes = 2000
	defaultJudgeWorkers   = 4

	// minJudgeFailures is how many failed ratings are tolerated before checking whether most fail, which
	// means the LLM settings are wrong rather than a few odd answers.
	minJudgeFailures = 10
)

var (
	ErrTooManyFailures = errors.New("most ratings fail; check the LLM settings")

	judgeSchema = json.RawMessage(`{
	"type": "object",
	"properties": {"rating": {"type": "integer"}, "reason": {"type": "string"}},
	"required": ["rating", "reason"],
	"additionalProperties": false
}`)
)

// KnownItemRubric rates how well a book matches a search describing it (JudgePrompt).
var KnownItemRubric = Rubric{Version: JudgePromptVersion, Prompt: JudgePrompt, SchemaName: judgeSchemaName, Schema: judgeSchema}

// Rubric is what a judge asks: the prompt, the answer's schema and the names of the dimensions it scores
// besides the overall rating. Version is part of the cache key, so each rubric has its own ratings.
type Rubric struct {
	Version    string
	Prompt     string
	SchemaName string
	Schema     json.RawMessage
	// Dimensions are scored 0 to MaxDimension, or NotApplicable when the search doesn't ask for them.
	Dimensions []string
}

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

// Judge rates how relevant books are to queries with an LLM. Ratings are appended to a JSONL cache file,
// so each (query, book) pair is rated once, across runs.
type Judge struct {
	client llm.Client
	model  string
	rubric Rubric
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

// OpenJudge returns a judge asking client with KnownItemRubric. See OpenJudgeRubric.
func OpenJudge(client llm.Client, model, path string) (*Judge, error) {
	return OpenJudgeRubric(client, model, path, KnownItemRubric)
}

// OpenJudgeRubric returns a judge asking client (model is its model, part of the cache key) with rubric, and
// caching ratings in the file at path, created if needed.
func OpenJudgeRubric(client llm.Client, model, path string, rubric Rubric) (*Judge, error) {
	f, judgments, err := openJSONL[Judgment](path)
	if err != nil {
		return nil, err
	}

	j := &Judge{client: client, model: model, rubric: rubric, cache: map[string]Judgment{}, file: f}
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

// Rating returns the cached rating of book for query, if there is one.
func (j *Judge) Rating(query string, book search.Result) (int, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()

	jd, ok := j.cache[j.key(query, book)]

	return jd.Rating, ok
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
		if _, ok := j.Rating(query, b); !ok {
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

	err := j.client.JSON(ctx, j.rubric.Prompt, judgeMessage(query, b), j.rubric.SchemaName, j.rubric.Schema, &a)
	if err == nil {
		err = j.rubric.check(a.Rating, a.Dimensions)
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

// key identifies a rating: the model, prompt, query and what the judge reads of the book.
func (j *Judge) key(query string, b search.Result) string {
	h := sha256.Sum256([]byte(strings.Join([]string{j.model, j.rubric.Version, query, b.ID, judgeMessage(query, b)}, "\x00")))
	return hex.EncodeToString(h[:16])
}

// check returns an error when the rating or a dimension score is out of range, or a dimension is missing.
func (r Rubric) check(rating int, dims map[string]int) error {
	if rating < 0 || rating > MaxRating {
		return fmt.Errorf("rating %d out of range", rating)
	}

	for _, d := range r.Dimensions {
		if s, ok := dims[d]; !ok || s < NotApplicable || s > MaxDimension {
			return fmt.Errorf("dimension %s: %d (present: %v) out of range", d, s, ok)
		}
	}

	return nil
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
