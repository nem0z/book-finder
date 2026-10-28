package bench

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nem0z/book-finder/internal/rerank"
	"github.com/nem0z/book-finder/internal/search"
)

const (
	pacedRetries = 5
	pacedMaxWait = time.Minute
	pacedWindow  = time.Minute
	// bytesPerToken estimates tokens from text length, for the token rate cap.
	bytesPerToken = 4
)

// pacedFirstWait is the wait after a first 429, doubled after each further one, up to pacedMaxWait.
var pacedFirstWait = 10 * time.Second

// PacedReranker wraps a re-ranker behind a rate-limited API, such as Voyage AI. Each text's score depends
// only on the query and that text, so it:
//   - remembers scores, so that a benchmark run again, or re-ranking the same works in another order or
//     depth, scores each text once;
//   - sends at most Chunk texts per call, when set;
//   - keeps under PerMinute calls and TokensPerMinute estimated tokens in any minute, when set;
//   - waits and tries again when a call is rate-limited (429), 10 s then twice as long each time, up to a
//     minute.
//
// Time spent waiting is not counted in Spent, which the benchmark reports as the re-rank time.
type PacedReranker struct {
	RR                         search.Reranker
	Chunk                      int // 0: no limit
	PerMinute, TokensPerMinute int // 0: no cap

	mu     sync.Mutex
	scores map[string]float64 // by scoreKey
	model  string
	file   *os.File
	calls  []pacedCall // in the last minute
	spent  time.Duration
}

// scoreLine is a line of the score cache file.
type scoreLine struct {
	Key   string  `json:"key"`
	Score float64 `json:"score"`
}

type pacedCall struct {
	at     time.Time
	tokens int
}

// OpenCache loads the scores of model cached in the file at path, creating it if needed, and appends the
// new scores to it, so that a later run with the same model, queries and texts makes no call.
func (p *PacedReranker) OpenCache(path, model string) error {
	f, lines, err := openJSONL[scoreLine](path)
	if err != nil {
		return err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	p.scores, p.model, p.file = map[string]float64{}, model, f
	for _, l := range lines {
		p.scores[l.Key] = l.Score
	}

	return nil
}

// Close closes the cache file, if one is open.
func (p *PacedReranker) Close() error {
	if p.file == nil {
		return nil
	}

	return p.file.Close()
}

// scoreKey identifies the score of text for query by the model.
func (p *PacedReranker) scoreKey(query, text string) string {
	h := sha256.Sum256([]byte(p.model + "\x00" + query + "\x00" + text))
	return hex.EncodeToString(h[:16])
}

// Scores implements search.Reranker.
func (p *PacedReranker) Scores(ctx context.Context, query string, texts []string) ([]float64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.scores == nil {
		p.scores = map[string]float64{}
	}

	var missing []int
	for i, t := range texts {
		if _, ok := p.scores[p.scoreKey(query, t)]; !ok {
			missing = append(missing, i)
		}
	}

	for len(missing) > 0 {
		n := len(missing)
		if p.Chunk > 0 {
			n = min(n, p.Chunk)
		}

		chunk := make([]string, n)
		for i, ti := range missing[:n] {
			chunk[i] = texts[ti]
		}

		scores, err := p.call(ctx, query, chunk)
		if err != nil {
			return nil, err
		}

		for i, ti := range missing[:n] {
			key := p.scoreKey(query, texts[ti])
			p.scores[key] = scores[i]
			if p.file != nil {
				line, _ := json.Marshal(scoreLine{Key: key, Score: scores[i]})
				if _, err := p.file.Write(append(line, '\n')); err != nil {
					return nil, err
				}
			}
		}

		missing = missing[n:]
	}

	out := make([]float64, len(texts))
	for i, t := range texts {
		out[i] = p.scores[p.scoreKey(query, t)]
	}

	return out, nil
}

// Spent returns the time spent in calls to the re-ranker, without waits.
func (p *PacedReranker) Spent() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.spent
}

// call scores texts, waiting for the rate caps first and after a 429.
func (p *PacedReranker) call(ctx context.Context, query string, texts []string) ([]float64, error) {
	tokens := estimateTokens(query, texts)

	for attempt := 0; ; attempt++ {
		if err := p.wait(ctx, tokens); err != nil {
			return nil, err
		}

		start := time.Now()
		p.calls = append(p.calls, pacedCall{at: start, tokens: tokens})
		scores, err := p.RR.Scores(ctx, query, texts)
		if err == nil {
			p.spent += time.Since(start)
			return scores, nil
		}

		if attempt == pacedRetries || !errors.Is(err, rerank.ErrBadResponse) || !strings.Contains(err.Error(), "429") {
			return nil, err
		}

		if err := sleep(ctx, min(pacedFirstWait<<attempt, pacedMaxWait)); err != nil {
			return nil, err
		}
	}
}

// estimateTokens estimates the tokens a re-ranker API bills for scoring texts: the query's for each text,
// plus the texts'.
func estimateTokens(query string, texts []string) int {
	tokens := len(query) / bytesPerToken * len(texts)
	for _, t := range texts {
		tokens += len(t) / bytesPerToken
	}

	return tokens
}

// wait sleeps until a call of tokens fits the caps over the last minute.
func (p *PacedReranker) wait(ctx context.Context, tokens int) error {
	for {
		now := time.Now()
		for len(p.calls) > 0 && now.Sub(p.calls[0].at) >= pacedWindow {
			p.calls = p.calls[1:]
		}

		used := 0
		for _, c := range p.calls {
			used += c.tokens
		}

		callsOK := p.PerMinute <= 0 || len(p.calls) < p.PerMinute
		tokensOK := p.TokensPerMinute <= 0 || len(p.calls) == 0 || used+tokens <= p.TokensPerMinute
		if callsOK && tokensOK {
			return nil
		}

		if err := sleep(ctx, p.calls[0].at.Add(pacedWindow).Sub(now)); err != nil {
			return err
		}
	}
}

// sleep waits d, or until ctx ends.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
