package understand

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestManagedIdentity(t *testing.T) {
	calls := 0
	status, reply := http.StatusOK, `{"access_token":"t1","expires_on":"1800000000","resource":"https://cognitiveservices.azure.com","token_type":"Bearer"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		q := r.URL.Query()
		if r.Header.Get("X-IDENTITY-HEADER") != "secret" || q.Get("resource") != cognitiveServicesResource ||
			q.Get("api-version") != identityAPIVersion || q.Get("client_id") != "id" {
			t.Errorf("request: %s %v", r.URL, r.Header)
		}

		w.WriteHeader(status)
		w.Write([]byte(reply)) //nolint:errcheck
	}))
	defer srv.Close()

	t.Setenv("IDENTITY_ENDPOINT", srv.URL)
	t.Setenv("IDENTITY_HEADER", "secret")
	t.Setenv("AZURE_CLIENT_ID", "id")
	m := managedIdentityFromEnv()
	now := time.Unix(1800000000, 0).Add(-time.Hour)
	m.now = func() time.Time { return now }

	ctx := context.Background()
	if tok, err := m.Token(ctx); tok != "t1" || err != nil {
		t.Fatalf("got %q, %v", tok, err)
	}

	if tok, _ := m.Token(ctx); tok != "t1" || calls != 1 {
		t.Errorf("not cached: %q after %d calls", tok, calls)
	}

	now = now.Add(time.Hour - time.Minute)
	reply = `{"access_token":"t2","expires_on":1800003600}`
	if tok, _ := m.Token(ctx); tok != "t2" || calls != 2 {
		t.Errorf("not renewed before expiry: %q after %d calls", tok, calls)
	}

	m.token = ""
	for _, bad := range []string{`{"access_token":"","expires_on":"1"}`, `{"access_token":"t","expires_on":"soon"}`, `not json`} {
		reply = bad
		if _, err := m.Token(ctx); !errors.Is(err, ErrBadResponse) {
			t.Errorf("%s: got %v", bad, err)
		}
	}

	status, reply = http.StatusBadRequest, `{"error":"no identity"}`
	if _, err := m.Token(ctx); !errors.Is(err, ErrBadResponse) {
		t.Errorf("400: %v", err)
	}
}

func TestManagedIdentityFromEnv(t *testing.T) {
	t.Setenv("IDENTITY_ENDPOINT", "")
	t.Setenv("IDENTITY_HEADER", "")
	if m := managedIdentityFromEnv(); m != nil {
		t.Errorf("outside Container Apps: %+v", m)
	}
}

func TestInterpretManagedIdentity(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.Write([]byte(`{"access_token":"tok","expires_on":"1800000000"}`)) //nolint:errcheck
			return
		}

		auth = r.Header.Get("Authorization")
		w.Write([]byte(chatReply(`{"description":"d","min_year":0,"max_year":0,"min_pages":0,"max_pages":0}`))) //nolint:errcheck
	}))
	defer srv.Close()

	t.Setenv("IDENTITY_ENDPOINT", srv.URL+"/token")
	t.Setenv("IDENTITY_HEADER", "secret")
	it, _, err := FromConfig(srv.URL, "m", "", "5s")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := it.Interpret(context.Background(), "q"); err != nil || auth != "Bearer tok" {
		t.Errorf("auth %q, %v", auth, err)
	}
}
