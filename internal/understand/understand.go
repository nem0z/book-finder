// Package understand reads a book search query with an LLM: it pulls out the hard filters the search
// supports and rewrites the rest as a short description of the wanted book, which embeds closer to the
// catalogue's descriptions than a conversational query does. It calls an OpenAI-compatible chat
// completions API with structured outputs: Azure OpenAI, OpenAI, or Ollama's /v1 endpoint.
package understand

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/nem0z/book-finder/internal/search"
)

const (
	// maxPages is the longest length accepted as a filter; larger values are taken as mistakes.
	maxPages = 10_000

	requestTimeout = time.Minute
	maxErrorBody   = 1024

	// systemPrompt takes the current year. Language is not extracted as a filter: most translated works
	// have original_language "eng" in the catalogue, so a language filter would hide right answers.
	systemPrompt = `You turn a book search query into a search request for a catalogue of fiction in English.

Return:
- description: the book the user wants, as a short back-cover blurb (one to three sentences): plot, characters, setting (place and time), themes, mood, genre. Write it in English, in the third person. Keep every detail the user gives about the book's content, including the setting, even when it also became a filter. Leave out conversational words ("I'm looking for", "a book that", "please", "recommend") and anything about the reader rather than the book. Do not add titles, authors or details the user did not imply.
- min_year, max_year: bounds on the year the book was first published, 0 when not set. Set them only from what the user says about when the book was written or published ("published after 2015", "a novel from the 60s"), or from a story set in a recent period the user dates ("set in 2020s Berlin", "modern Amsterdam (2020+)"): a book about that period was published then or later, so set min_year only. A story set in the past says nothing about when it was written: "set in Victorian London" sets no year. Words like "modern" or "recent" without a date set no year.
- min_pages, max_pages: bounds on the length in pages, 0 when not set. Only from an explicit number ("under 200 pages", "800+ pages"). For vague lengths ("short", "long") set nothing and keep the word in the description.

The current year is %d.`
)

var (
	ErrBadResponse = errors.New("unexpected LLM response")

	// datePattern finds a year (1925, 2020+), a decade (1960s, '60s) or a century (19th).
	datePattern = regexp.MustCompile(`(?i)\b\d{4}\b|\d0s\b|\b\d{1,2}(st|nd|rd|th)\b`)

	// queryJSONSchema is the structured output the model must return. Strict mode needs every property
	// required and no others, hence 0 rather than a missing field for "no bound".
	queryJSONSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"description": {"type": "string"},
		"min_year": {"type": "integer"},
		"max_year": {"type": "integer"},
		"min_pages": {"type": "integer"},
		"max_pages": {"type": "integer"}
	},
	"required": ["description", "min_year", "max_year", "min_pages", "max_pages"],
	"additionalProperties": false
}`)
)

// Client interprets queries with a chat model.
type Client struct {
	baseURL string
	model   string
	apiKey  string
	client  *http.Client
	now     func() time.Time
}

type chatRequest struct {
	Model          string         `json:"model"`
	Messages       []chatMessage  `json:"messages"`
	ResponseFormat responseFormat `json:"response_format"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseFormat struct {
	Type       string     `json:"type"`
	JSONSchema jsonSchema `json:"json_schema"`
}

type jsonSchema struct {
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

// queryAnswer is the model's answer, as described by queryJSONSchema.
type queryAnswer struct {
	Description string `json:"description"`
	MinYear     int    `json:"min_year"`
	MaxYear     int    `json:"max_year"`
	MinPages    int    `json:"min_pages"`
	MaxPages    int    `json:"max_pages"`
}

// New returns a client for the chat completions API at baseURL (e.g.
// https://<resource>.openai.azure.com/openai/v1, or http://localhost:11434/v1 for Ollama). model is the
// model or, on Azure, the deployment name. apiKey is sent as a bearer token when set.
func New(baseURL, model, apiKey string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		apiKey:  apiKey,
		client:  &http.Client{Timeout: requestTimeout},
		now:     time.Now,
	}
}

// Interpret returns the filters query states and a description of the book it asks for. Values the model
// returns out of range are dropped; an empty description falls back to the query.
func (c *Client) Interpret(ctx context.Context, query string) (search.Interpretation, error) {
	req := chatRequest{
		Model: c.model,
		Messages: []chatMessage{
			{Role: "system", Content: fmt.Sprintf(systemPrompt, c.now().Year())},
			{Role: "user", Content: query},
		},
		ResponseFormat: responseFormat{
			Type:       "json_schema",
			JSONSchema: jsonSchema{Name: "book_query", Strict: true, Schema: queryJSONSchema},
		},
	}

	var resp chatResponse
	if err := c.post(ctx, "/chat/completions", req, &resp); err != nil {
		return search.Interpretation{}, err
	}

	if len(resp.Choices) == 0 {
		return search.Interpretation{}, fmt.Errorf("%w: no choices", ErrBadResponse)
	}

	msg := resp.Choices[0].Message
	if msg.Refusal != "" {
		return search.Interpretation{}, fmt.Errorf("%w: refused: %s", ErrBadResponse, msg.Refusal)
	}

	var a queryAnswer
	if err := json.Unmarshal([]byte(msg.Content), &a); err != nil {
		return search.Interpretation{}, fmt.Errorf("%w: %w", ErrBadResponse, err)
	}

	return a.interpretation(query, c.now().Year()), nil
}

// interpretation checks the answer. A filter hides books for good, so it needs grounds in the query: year
// bounds a year, decade or century ("2020+", "the 60s", "19th century"), page bounds the word "page". Small
// models invent bounds otherwise. Out-of-range or contradictory bounds are dropped too.
func (a queryAnswer) interpretation(query string, year int) search.Interpretation {
	var f search.Filters
	if datePattern.MatchString(query) {
		f.MinYear, f.MaxYear = bounds(a.MinYear, a.MaxYear, year+1)
	}

	if strings.Contains(strings.ToLower(query), "page") {
		f.MinPages, f.MaxPages = bounds(a.MinPages, a.MaxPages, maxPages)
	}

	desc := strings.TrimSpace(a.Description)
	if desc == "" {
		desc = query
	}

	return search.Interpretation{Filters: f, Description: desc}
}

// bounds keeps lo and hi when they are in [1, limit]. Bounds that contradict each other are both dropped.
func bounds(lo, hi, limit int) (int, int) {
	if lo < 1 || lo > limit {
		lo = 0
	}

	if hi < 1 || hi > limit {
		hi = 0
	}

	if lo > 0 && hi > 0 && lo > hi {
		return 0, 0
	}

	return lo, hi
}

func (c *Client) post(ctx context.Context, path string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("LLM at %s: %w", c.baseURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return fmt.Errorf("%w: %s: %s", ErrBadResponse, resp.Status, strings.TrimSpace(string(msg)))
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%w: %w", ErrBadResponse, err)
	}

	return nil
}
