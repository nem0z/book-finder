package bench

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nem0z/book-finder/internal/enrich"
	"github.com/nem0z/book-finder/internal/llm"
)

const queryKeywordsSchemaName = "query_keywords"

// QueryKeywords turns search queries into keywords with an LLM (enrich.QueryPrompt), in the shape of a
// book's keywords. Answers are appended to a JSONL cache file, so each query is sent once across runs.
type QueryKeywords struct {
	client llm.Client
	model  string

	mu    sync.Mutex
	cache map[string]queryKeywordsEntry
	file  *os.File
}

// queryKeywordsEntry is a line of the cache file. Took is how long the LLM took to answer.
type queryKeywordsEntry struct {
	Key      string          `json:"key"`
	Query    string          `json:"query"`
	Keywords enrich.Keywords `json:"keywords"`
	Took     time.Duration   `json:"took_ns"`
}

// OpenQueryKeywords returns a QueryKeywords asking client (model is its model, part of the cache key) and
// caching answers in the file at path, created if needed.
func OpenQueryKeywords(client llm.Client, model, path string) (*QueryKeywords, error) {
	f, entries, err := openJSONL[queryKeywordsEntry](path)
	if err != nil {
		return nil, err
	}

	q := &QueryKeywords{client: client, model: model, cache: map[string]queryKeywordsEntry{}, file: f}
	for _, e := range entries {
		q.cache[e.Key] = e
	}

	return q, nil
}

func (q *QueryKeywords) Close() error { return q.file.Close() }

// Get returns query's keywords as one text (enrich.Keywords.Text), and how long the LLM took to answer,
// asking it when the answer is not cached.
func (q *QueryKeywords) Get(ctx context.Context, query string) (string, time.Duration, error) {
	h := sha256.Sum256([]byte(strings.Join([]string{q.model, enrich.QueryPrompt, query}, "\x00")))
	key := hex.EncodeToString(h[:16])

	q.mu.Lock()
	e, ok := q.cache[key]
	q.mu.Unlock()
	if ok {
		return e.Keywords.Normalize().Text(), e.Took, nil
	}

	var k enrich.Keywords
	start := time.Now()
	if err := q.client.JSON(ctx, enrich.QueryPrompt, query, queryKeywordsSchemaName, enrich.Schema, &k); err != nil {
		return "", 0, fmt.Errorf("query keywords: %w", err)
	}

	e = queryKeywordsEntry{Key: key, Query: query, Keywords: k, Took: time.Since(start)}
	if strings.TrimSpace(e.Keywords.Normalize().Text()) == "" {
		return "", 0, fmt.Errorf("query keywords: %w: no keywords", llm.ErrBadResponse)
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	line, _ := json.Marshal(e)
	if _, err := q.file.Write(append(line, '\n')); err != nil {
		return "", 0, err
	}

	q.cache[key] = e

	return e.Keywords.Normalize().Text(), e.Took, nil
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
