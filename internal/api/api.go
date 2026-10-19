// Package api serves the book search over HTTP.
package api

import (
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

// Searcher runs a search. *search.Engine implements it.
type Searcher interface {
	Search(ctx context.Context, query string, f search.Filters, limit int) ([]search.Result, error)
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
	URL         string   `json:"url"` // the work's Open Library page
}

type searchResponse struct {
	Query   string `json:"query"`
	Results []Book `json:"results"`
}

type errorResponse struct {
	Error string `json:"error"`
}

type handler struct {
	searcher Searcher
	log      *slog.Logger
}

// NewHandler returns the API's routes: GET /search?q=<description of the book>.
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

	results, err := h.searcher.Search(r.Context(), query, search.Filters{}, ResultLimit)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	books := make([]Book, len(results))
	for i, res := range results {
		books[i] = toBook(res)
	}

	writeJSON(w, http.StatusOK, searchResponse{Query: query, Results: books})
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
		Description: r.Description, Tags: r.Tags, URL: openLibraryURL + r.ID,
	}

	if r.CoverID > 0 {
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

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck // nothing to do if the client is gone
}
