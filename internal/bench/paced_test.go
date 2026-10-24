package bench

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/nem0z/book-finder/internal/rerank"
)

// lengthScorer scores a text by its length; it records the size of each call and fails the calls listed
// in limited with a 429.
type lengthScorer struct {
	calls   []int
	limited map[int]bool
}

func (l *lengthScorer) Scores(_ context.Context, _ string, texts []string) ([]float64, error) {
	l.calls = append(l.calls, len(texts))
	time.Sleep(2 * time.Millisecond)
	if l.limited[len(l.calls)] {
		return nil, fmt.Errorf("%w: 429 Too Many Requests: slow down", rerank.ErrBadResponse)
	}

	out := make([]float64, len(texts))
	for i, t := range texts {
		out[i] = float64(len(t))
	}

	return out, nil
}

func TestPacedReranker(t *testing.T) {
	defer func(d time.Duration) { pacedFirstWait = d }(pacedFirstWait)
	pacedFirstWait = time.Millisecond

	ctx := context.Background()
	inner := &lengthScorer{limited: map[int]bool{2: true}}
	p := &PacedReranker{RR: inner, Chunk: 2}

	scores, err := p.Scores(ctx, "q", []string{"a", "bb", "ccc"})
	if err != nil || !slices.Equal(scores, []float64{1, 2, 3}) {
		t.Fatalf("scores %v, %v", scores, err)
	}

	// Calls: 2 texts, then 1 rate-limited and asked again.
	if !slices.Equal(inner.calls, []int{2, 1, 1}) {
		t.Errorf("calls: %v", inner.calls)
	}

	// Cached: only the new text is scored, and another query is scored apart.
	if scores, err := p.Scores(ctx, "q", []string{"ccc", "dddd"}); err != nil || !slices.Equal(scores, []float64{3, 4}) || inner.calls[len(inner.calls)-1] != 1 {
		t.Errorf("cached: %v, %v, calls %v", scores, err, inner.calls)
	}

	if _, err := p.Scores(ctx, "other", []string{"a"}); err != nil || len(inner.calls) != 5 {
		t.Errorf("other query: %v, calls %v", err, inner.calls)
	}

	if p.Spent() <= 0 {
		t.Errorf("spent: %v", p.Spent())
	}

	// Another error is not retried.
	failing := &PacedReranker{RR: &lengthScorer{limited: map[int]bool{1: true, 2: true, 3: true, 4: true, 5: true, 6: true}}}
	if _, err := failing.Scores(ctx, "q", []string{"a"}); err == nil {
		t.Error("expected the 429 error after the retries")
	}
}

func TestPacedRerankerCaps(t *testing.T) {
	p := &PacedReranker{PerMinute: 1}
	p.calls = []pacedCall{{at: time.Now().Add(-pacedWindow + 20*time.Millisecond), tokens: 1}}

	start := time.Now()
	if err := p.wait(context.Background(), 1); err != nil || time.Since(start) < 10*time.Millisecond {
		t.Errorf("wait: %v after %v", err, time.Since(start))
	}

	p = &PacedReranker{TokensPerMinute: 100}
	p.calls = []pacedCall{{at: time.Now(), tokens: 90}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := p.wait(ctx, 20); err == nil {
		t.Error("a call over the token cap should wait")
	}
}
