package rerank

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestVoyage(t *testing.T) {
	var got voyageRequest
	var auth string
	status := http.StatusOK
	reply := `{"object":"list","data":[{"index":1,"relevance_score":0.9},{"index":0,"relevance_score":0.1}],"model":"m","usage":{"total_tokens":8}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rerank" {
			http.NotFound(w, r)
			return
		}

		auth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}

		w.WriteHeader(status)
		w.Write([]byte(reply)) //nolint:errcheck
	}))
	defer srv.Close()

	v := NewVoyage("key", "m")
	v.baseURL = srv.URL
	scores, err := v.Scores(context.Background(), "q", []string{"a", "b"})
	if err != nil || !slices.Equal(scores, []float64{0.1, 0.9}) {
		t.Fatalf("scores %v, %v", scores, err)
	}

	if auth != "Bearer key" || got.Query != "q" || !slices.Equal(got.Documents, []string{"a", "b"}) || got.Model != "m" || !got.Truncation {
		t.Errorf("request: %q %+v", auth, got)
	}

	for _, bad := range []string{`{"data":[{"index":0,"relevance_score":1}]}`, `{"data":[{"index":0,"relevance_score":1},{"index":0,"relevance_score":2}]}`, `not json`} {
		reply = bad
		if _, err := v.Scores(context.Background(), "q", []string{"a", "b"}); !errors.Is(err, ErrBadResponse) {
			t.Errorf("%s: got %v", bad, err)
		}
	}

	status, reply = http.StatusUnauthorized, `{"detail":"Provided API key is invalid."}`
	if _, err := v.Scores(context.Background(), "q", []string{"a"}); !errors.Is(err, ErrBadResponse) || !strings.Contains(err.Error(), "invalid") {
		t.Errorf("401: %v", err)
	}
}
