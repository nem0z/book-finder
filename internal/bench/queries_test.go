package bench

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckAnswer(t *testing.T) {
	dims := map[string]int{}
	for _, d := range Dimensions {
		dims[d] = NotApplicable
	}

	if err := checkAnswer(2, dims); err != nil {
		t.Errorf("valid: %v", err)
	}

	if checkAnswer(4, dims) == nil {
		t.Error("rating out of range")
	}

	dims["plot"] = 3
	if checkAnswer(2, dims) == nil {
		t.Error("dimension out of range")
	}

	delete(dims, "plot")
	if checkAnswer(2, dims) == nil {
		t.Error("missing dimension")
	}
}

func TestLoadQueries(t *testing.T) {
	qs, err := LoadQueries("")
	if err != nil {
		t.Fatal(err)
	}

	cats, lengths, flagged := map[string]int{}, map[string]int{}, 0
	for _, q := range qs {
		cats[q.Category]++
		lengths[q.Length]++
		if q.Flag != "" {
			flagged++
		}

		if strings.TrimSpace(q.Query) == "" {
			t.Errorf("query %+v", q)
		}
	}

	if len(qs) != 70 || len(cats) != 7 || lengths["S"] != 14 || lengths["M"] != 21 || lengths["L"] != 21 || lengths["XL"] != 14 || flagged != 2 {
		t.Errorf("%d queries, categories %v, lengths %v, %d flagged", len(qs), cats, lengths, flagged)
	}

	for c, n := range cats {
		if n != 10 {
			t.Errorf("%s: %d queries", c, n)
		}
	}

	path := filepath.Join(t.TempDir(), "q.json")
	if err := os.WriteFile(path, []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadQueries(path); !errors.Is(err, ErrNoQueries) {
		t.Errorf("empty file: %v", err)
	}
}
