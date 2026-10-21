package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	requestTimeout = time.Minute
	maxErrorBody   = 1024

	// maxRetries is how many times a rate-limited request (429) is sent again.
	maxRetries = 5
	// defaultRetryAfter is the wait after a 429 without a Retry-After header.
	defaultRetryAfter = time.Second
	maxRetryAfter     = time.Minute
)

// OpenAI calls an OpenAI-compatible chat completions API with structured outputs. NewAzure and NewOllama
// return one.
type OpenAI struct {
	url    string
	model  string
	header http.Header
	client *http.Client
	sleep  func(ctx context.Context, d time.Duration) error
}

// ChatRequest is the body of a chat completions request, as built by Request.
type ChatRequest struct {
	Model          string         `json:"model"`
	Messages       []Message      `json:"messages"`
	ResponseFormat ResponseFormat `json:"response_format"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ResponseFormat struct {
	Type       string     `json:"type"`
	JSONSchema JSONSchema `json:"json_schema"`
}

type JSONSchema struct {
	Name   string          `json:"name"`
	Strict bool            `json:"strict"`
	Schema json.RawMessage `json:"schema"`
}

type chatResponse struct {
	Choices []chatChoice `json:"choices"`
}

type chatChoice struct {
	Message struct {
		Content string `json:"content"`
		Refusal string `json:"refusal"`
	} `json:"message"`
}

// NewAzure returns a client for the deployment of an Azure OpenAI resource, through its v1 API at url
// (https://<resource>.openai.azure.com/openai/v1).
func NewAzure(url, deployment, apiKey string) *OpenAI {
	return newOpenAI(strings.TrimRight(url, "/"), deployment, http.Header{"Api-Key": {apiKey}})
}

// NewOllama returns a client for a model served by Ollama at url (e.g. http://localhost:11434).
func NewOllama(url, model string) *OpenAI {
	return newOpenAI(strings.TrimRight(url, "/")+"/v1", model, nil)
}

func newOpenAI(url, model string, header http.Header) *OpenAI {
	return &OpenAI{url: url, model: model, header: header, client: &http.Client{Timeout: requestTimeout}, sleep: sleep}
}

// Request returns the chat completions request that asks model for an answer matching schema, in strict
// mode: the schema must list every property as required and forbid others.
func Request(model, system, user, schemaName string, schema json.RawMessage) ChatRequest {
	return ChatRequest{
		Model:    model,
		Messages: []Message{{Role: "system", Content: system}, {Role: "user", Content: user}},
		ResponseFormat: ResponseFormat{
			Type:       "json_schema",
			JSONSchema: JSONSchema{Name: schemaName, Strict: true, Schema: schema},
		},
	}
}

// JSON implements Client. Rate-limited requests are sent again after the wait the API asks for, while ctx
// allows.
func (o *OpenAI) JSON(ctx context.Context, system, user, schemaName string, schema json.RawMessage, out any) error {
	var resp chatResponse
	if err := o.post(ctx, Request(o.model, system, user, schemaName, schema), &resp); err != nil {
		return err
	}

	if len(resp.Choices) == 0 {
		return fmt.Errorf("%w: no choices", ErrBadResponse)
	}

	msg := resp.Choices[0].Message
	if msg.Refusal != "" {
		return fmt.Errorf("%w: refused: %s", ErrBadResponse, msg.Refusal)
	}

	if err := json.Unmarshal([]byte(msg.Content), out); err != nil {
		return fmt.Errorf("%w: %w", ErrBadResponse, err)
	}

	return nil
}

func (o *OpenAI) post(ctx context.Context, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}

	for attempt := 0; ; attempt++ {
		wait, err := o.send(ctx, body, out)
		if wait == 0 || attempt == maxRetries {
			return err
		}

		if serr := o.sleep(ctx, wait); serr != nil {
			return fmt.Errorf("%w (not retried: %w)", err, serr)
		}
	}
}

// send posts body once. On a 429 it returns the error and how long to wait before trying again.
func (o *OpenAI) send(ctx context.Context, body []byte, out any) (time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.url+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}

	for k, v := range o.header {
		req.Header[k] = v
	}

	req.Header.Set("Content-Type", "application/json")
	resp, err := o.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("LLM at %s: %w", o.url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		err := fmt.Errorf("%w: %s: %s", ErrBadResponse, resp.Status, strings.TrimSpace(string(msg)))
		if resp.StatusCode == http.StatusTooManyRequests {
			return retryAfter(resp.Header), err
		}

		return 0, err
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return 0, fmt.Errorf("%w: %w", ErrBadResponse, err)
	}

	return 0, nil
}

// retryAfter reads the wait a 429 asks for: retry-after-ms (Azure) or Retry-After in seconds.
func retryAfter(h http.Header) time.Duration {
	d := defaultRetryAfter
	if ms, err := strconv.Atoi(h.Get("Retry-After-Ms")); err == nil && ms > 0 {
		d = time.Duration(ms) * time.Millisecond
	} else if s, err := strconv.Atoi(h.Get("Retry-After")); err == nil && s > 0 {
		d = time.Duration(s) * time.Second
	}

	return min(d, maxRetryAfter)
}

// sleep waits d, unless ctx ends first or would end before d.
func sleep(ctx context.Context, d time.Duration) error {
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < d {
		return context.DeadlineExceeded
	}

	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
