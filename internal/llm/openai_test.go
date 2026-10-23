package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var testSchema = json.RawMessage(`{"type":"object","properties":{"a":{"type":"integer"}},"required":["a"],"additionalProperties":false}`)

type answer struct {
	A int `json:"a"`
}

func TestOpenAI(t *testing.T) {
	var got ChatRequest
	var header http.Header
	status, reply := http.StatusOK, chatReply(`{"a":7}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/openai/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}

		header = r.Header
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}

		w.WriteHeader(status)
		w.Write([]byte(reply)) //nolint:errcheck
	}))
	defer srv.Close()

	c := NewAzure(srv.URL+"/openai/v1/", "dep", "key")
	var a answer
	if err := c.JSON(context.Background(), "sys", "user", "s", testSchema, &a); err != nil || a.A != 7 {
		t.Fatalf("got %+v, %v", a, err)
	}

	want := Request("dep", "sys", "user", "s", testSchema)
	if header.Get("Api-Key") != "key" || got.Model != want.Model || len(got.Messages) != 2 || got.Messages[0] != want.Messages[0] ||
		got.Messages[1] != want.Messages[1] || got.ResponseFormat.Type != "json_schema" || !got.ResponseFormat.JSONSchema.Strict ||
		got.ResponseFormat.JSONSchema.Name != "s" || string(got.ResponseFormat.JSONSchema.Schema) != string(testSchema) {
		t.Errorf("request: %v %+v", header, got)
	}

	for _, bad := range []string{`{"choices":[]}`, chatReply(`not json`), `{"choices":[{"message":{"content":"","refusal":"no"}}]}`, `not json`} {
		reply = bad
		if err := c.JSON(context.Background(), "s", "u", "s", testSchema, &a); !errors.Is(err, ErrBadResponse) {
			t.Errorf("%s: got %v", bad, err)
		}
	}

	status, reply = http.StatusUnauthorized, `{"error":{"message":"invalid key"}}`
	if err := c.JSON(context.Background(), "s", "u", "s", testSchema, &a); !errors.Is(err, ErrBadResponse) || !strings.Contains(err.Error(), "invalid key") {
		t.Errorf("401: %v", err)
	}
}

func TestOllama(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Api-Key") != "" || r.Header.Get("Authorization") != "" {
			t.Errorf("request: %s %v", r.URL.Path, r.Header)
		}

		w.Write([]byte(chatReply(`{"a":1}`))) //nolint:errcheck
	}))
	defer srv.Close()

	var a answer
	if err := NewOllama(srv.URL+"/", "m").JSON(context.Background(), "s", "u", "s", testSchema, &a); err != nil || a.A != 1 {
		t.Errorf("got %+v, %v", a, err)
	}
}

func TestRetry(t *testing.T) {
	calls := 0
	limited := 2
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls <= limited {
			w.Header().Set("Retry-After-Ms", "1500")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}

		w.Write([]byte(chatReply(`{"a":3}`))) //nolint:errcheck
	}))
	defer srv.Close()

	c := NewOllama(srv.URL, "m")
	var waits []time.Duration
	c.sleep = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}

	var a answer
	if err := c.JSON(context.Background(), "s", "u", "s", testSchema, &a); err != nil || a.A != 3 || calls != 3 ||
		len(waits) != 2 || waits[0] != 1500*time.Millisecond {
		t.Errorf("got %+v, %v after %d calls, waits %v", a, err, calls, waits)
	}

	calls, limited = 0, 100
	if err := c.JSON(context.Background(), "s", "u", "s", testSchema, &a); !errors.Is(err, ErrBadResponse) || calls != maxRetries+1 {
		t.Errorf("always limited: %v after %d calls", err, calls)
	}

	calls = 0
	c.sleep = sleep
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.JSON(ctx, "s", "u", "s", testSchema, &a); !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
		t.Errorf("wait past the deadline: %v after %d calls", err, calls)
	}
}

func TestRetryAfter(t *testing.T) {
	for _, tt := range []struct {
		h    http.Header
		want time.Duration
	}{
		{http.Header{}, defaultRetryAfter},
		{http.Header{"Retry-After": {"3"}}, 3 * time.Second},
		{http.Header{"Retry-After": {"3"}, "Retry-After-Ms": {"200"}}, 200 * time.Millisecond},
		{http.Header{"Retry-After": {"Wed, 21 Oct 2026 07:28:00 GMT"}}, defaultRetryAfter},
		{http.Header{"Retry-After": {"3600"}}, maxRetryAfter},
	} {
		if got := retryAfter(tt.h); got != tt.want {
			t.Errorf("%v: got %s", tt.h, got)
		}
	}
}

func TestNew(t *testing.T) {
	for _, bad := range []Config{
		{Provider: ProviderAzure, URL: "u", APIKey: "k"},
		{Provider: ProviderAzure, URL: "u", Model: "m"},
		{Provider: ProviderOllama, Model: "m"},
		{Provider: "openai", URL: "u", Model: "m"},
	} {
		if _, err := New(bad); !errors.Is(err, ErrConfig) {
			t.Errorf("%+v: %v", bad, err)
		}
	}

	if c, err := New(Config{Provider: ProviderAzure, URL: "https://r/openai/v1", Model: "d", APIKey: "k"}); err != nil || c.(*OpenAI).url != "https://r/openai/v1" {
		t.Errorf("azure: %+v, %v", c, err)
	}

	if c, err := New(Config{Provider: ProviderOllama, URL: "http://o:11434", Model: "m"}); err != nil || c.(*OpenAI).url != "http://o:11434/v1" {
		t.Errorf("ollama: %+v, %v", c, err)
	}
}

func chatReply(content string) string {
	b, _ := json.Marshal(content)
	return `{"choices":[{"index":0,"message":{"role":"assistant","content":` + string(b) + `}}]}`
}
