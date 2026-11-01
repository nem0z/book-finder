// Package api serves the book search over HTTP.
package api

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/nem0z/book-finder/internal/search"
)

const (
	// ResultLimit is how many books a search returns.
	ResultLimit = 10

	maxQueryBytes = 1000

	coverURL       = "https://covers.openlibrary.org/b/id/%d-M.jpg"
	openLibraryURL = "https://openlibrary.org/works/"
)

// Searcher runs a search and says what it understood of the query. *search.Engine implements it.
type Searcher interface {
	SearchInterpreted(ctx context.Context, query string, f search.Filters, limit int) ([]search.Result, *search.Interpretation, error)
}

// Book is one search result as returned by the API.
type Book struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Authors     []string `json:"authors"`
	Year        int      `json:"year,omitempty"`
	Pages       int      `json:"pages,omitempty"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	CoverURL    string   `json:"cover_url,omitempty"`
	URL         string   `json:"url"`    // the bookshop's product page, else the work's Open Library page
	Source      string   `json:"source"` // openlibrary or a bookshop (chapters)
}

type searchResponse struct {
	Query string `json:"query"`
	// Interpretation is what an LLM understood of the query; absent when the query was searched as typed.
	Interpretation *Interpretation `json:"interpretation,omitempty"`
	Results        []Book          `json:"results"`
}

// Interpretation is what the search embedded in place of the query, the keywords of the wanted book, and
// the filters it applied.
type Interpretation struct {
	Keywords string  `json:"keywords"`
	Filters  Filters `json:"filters"`
}

// Filters are the bounds a search applied; absent ones were not set.
type Filters struct {
	MinYear          int    `json:"min_year,omitempty"`
	MaxYear          int    `json:"max_year,omitempty"`
	MinPages         int    `json:"min_pages,omitempty"`
	MaxPages         int    `json:"max_pages,omitempty"`
	OriginalLanguage string `json:"original_language,omitempty"`
	InShop           bool   `json:"in_shop,omitempty"`
}

type errorResponse struct {
	Error string `json:"error"`
}

type handler struct {
	searcher Searcher
	log      *slog.Logger
}

// NewHandler returns the API's routes: GET /search?q=<description of the book>, with shop=1 for only the books a
// bookshop lists.
func NewHandler(s Searcher, log *slog.Logger) http.Handler {
	h := &handler{searcher: s, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /search", h.search)

	return mux
}

func (h *handler) search(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	switch {
	case query == "":
		writeJSON(w, http.StatusBadRequest, errorResponse{"missing query: use /search?q=..."})
		return
	case len(query) > maxQueryBytes:
		writeJSON(w, http.StatusBadRequest, errorResponse{"query too long: at most " + strconv.Itoa(maxQueryBytes) + " bytes"})
		return
	}

	inShop, err := strconv.ParseBool(cmp.Or(r.URL.Query().Get("shop"), "false"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{"shop must be 1 or 0"})
		return
	}

	results, in, err := h.searcher.SearchInterpreted(r.Context(), query, search.Filters{InShop: inShop}, ResultLimit)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	books := make([]Book, len(results))
	for i, res := range results {
		books[i] = toBook(res)
	}

	writeJSON(w, http.StatusOK, searchResponse{Query: query, Interpretation: toInterpretation(in), Results: books})
}

// fail logs a search error and answers without its details, which can include internal addresses.
func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, context.Canceled) {
		return // the client went away
	}

	h.log.Error("search failed", "query", r.URL.Query().Get("q"), "err", err)
	writeJSON(w, http.StatusInternalServerError, errorResponse{"search failed"})
}

func toBook(r search.Result) Book {
	b := Book{
		ID: r.ID, Title: r.Title, Authors: r.Authors, Year: r.Year, Pages: r.Pages,
		Description: r.Description, Tags: r.Tags, URL: cmp.Or(r.SourceURL, openLibraryURL+r.ID),
		Source: cmp.Or(r.Source, "openlibrary"), CoverURL: r.CoverURL,
	}

	if b.CoverURL == "" && r.CoverID > 0 {
		b.CoverURL = fmt.Sprintf(coverURL, r.CoverID)
	}

	if b.Authors == nil {
		b.Authors = []string{}
	}

	if b.Tags == nil {
		b.Tags = []string{}
	}

	return b
}

func toInterpretation(in *search.Interpretation) *Interpretation {
	if in == nil {
		return nil
	}

	f := in.Filters

	return &Interpretation{
		Keywords: in.Text,
		Filters: Filters{MinYear: f.MinYear, MaxYear: f.MaxYear, MinPages: f.MinPages, MaxPages: f.MaxPages,
			OriginalLanguage: f.OriginalLanguage, InShop: f.InShop},
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck // nothing to do if the client is gone
}
