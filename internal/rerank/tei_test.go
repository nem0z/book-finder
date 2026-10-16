package rerank

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

func TestTEI(t *testing.T) {
	var got rerankRequest
	reply := `[{"index":1,"score":0.9},{"index":0,"score":0.1}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rerank" {
			http.NotFound(w, r)
			return
		}

		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}

		w.Write([]byte(reply)) //nolint:errcheck
	}))
	defer srv.Close()

	tei := NewTEI(srv.URL + "/")
	scores, err := tei.Scores(context.Background(), "q", []string{"a", "b"})
	if err != nil || !slices.Equal(scores, []float64{0.1, 0.9}) {
		t.Fatalf("scores %v, %v", scores, err)
	}

	if got.Query != "q" || !slices.Equal(got.Texts, []string{"a", "b"}) || !got.Truncate {
		t.Errorf("request: %+v", got)
	}

	for _, bad := range []string{`[{"index":0,"score":1}]`, `[{"index":0,"score":1},{"index":0,"score":2}]`,
		`[{"index":0,"score":1},{"index":5,"score":2}]`, `not json`} {
		reply = bad
		if _, err := tei.Scores(context.Background(), "q", []string{"a", "b"}); !errors.Is(err, ErrBadResponse) {
			t.Errorf("%s: got %v", bad, err)
		}
	}

	if _, err := NewTEI(srv.URL+"/missing").Scores(context.Background(), "q", []string{"a"}); !errors.Is(err, ErrBadResponse) {
		t.Errorf("404: %v", err)
	}
}
