package bench

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/nem0z/book-finder/internal/enrich"
	"github.com/nem0z/book-finder/internal/llm"
	"github.com/nem0z/book-finder/internal/search"
	"github.com/nem0z/book-finder/internal/understand"
)

const (
	// rewriteVersion is part of the cache key: bump it when understand's prompt changes.
	rewriteVersion = "1"

	queryKeywordsSchemaName = "query_keywords"
)

// Rewrite is what an LLM makes of a query: the search's interpretation (a description of the wanted book and
// filters, from understand.Interpreter) and the query as keywords in the shape of a book's (enrich.QueryPrompt).
type Rewrite struct {
	Key         string         `json:"key"`
	Query       string         `json:"query"`
	Description string         `json:"description"`
	Filters     search.Filters `json:"filters"`
	Keywords    string         `json:"keywords"`
}

// Rewriter rewrites queries with an LLM. Rewrites are appended to a JSONL cache file, so each query is sent
// once across runs, and every run searches with the same rewrites.
type Rewriter struct {
	client llm.Client
	interp search.Interpreter
	model  string

	mu    sync.Mutex
	cache map[string]Rewrite
	file  *os.File
}

// OpenRewriter returns a Rewriter asking client (model is its model, part of the cache key) and caching
// rewrites in the file at path, created if needed.
func OpenRewriter(client llm.Client, model, path string) (*Rewriter, error) {
	f, rewrites, err := openJSONL[Rewrite](path)
	if err != nil {
		return nil, err
	}

	r := &Rewriter{client: client, interp: understand.New(client), model: model, cache: map[string]Rewrite{}, file: f}
	for _, rw := range rewrites {
		r.cache[rw.Key] = rw
	}

	return r, nil
}

func (r *Rewriter) Close() error { return r.file.Close() }

// Get returns query's rewrite, asking the LLM when it is not cached.
func (r *Rewriter) Get(ctx context.Context, query string) (Rewrite, error) {
	h := sha256.Sum256([]byte(strings.Join([]string{r.model, rewriteVersion, enrich.QueryPrompt, query}, "\x00")))
	key := hex.EncodeToString(h[:16])

	r.mu.Lock()
	rw, ok := r.cache[key]
	r.mu.Unlock()
	if ok {
		return rw, nil
	}

	in, err := r.interp.Interpret(ctx, query)
	if err != nil {
		return Rewrite{}, fmt.Errorf("interpret: %w", err)
	}

	var k enrich.Keywords
	if err := r.client.JSON(ctx, enrich.QueryPrompt, query, queryKeywordsSchemaName, enrich.Schema, &k); err != nil {
		return Rewrite{}, fmt.Errorf("query keywords: %w", err)
	}

	rw = Rewrite{Key: key, Query: query, Description: in.Description, Filters: in.Filters, Keywords: k.Normalize().Text()}
	if strings.TrimSpace(rw.Keywords) == "" {
		return Rewrite{}, fmt.Errorf("query keywords: %w: no keywords", llm.ErrBadResponse)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	line, _ := json.Marshal(rw)
	if _, err := r.file.Write(append(line, '\n')); err != nil {
		return Rewrite{}, err
	}

	r.cache[key] = rw

	return rw, nil
}
