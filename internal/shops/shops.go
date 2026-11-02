// Package shops reads the catalogues of bookshops' online stores and turns their products into books, to be
// stored as works of the shop's source (see docs/shops.md). What is common to every shop lives here: the
// registry of shops, the book model, the paced HTTP client and text helpers.
// Each e-commerce platform (package shopify) only builds its requests and parses its products.
package shops

import (
	"context"
	"errors"
	"fmt"
)

// Platforms the shops run on; each has a package that lists a store's products.
const (
	Shopify Platform = "shopify"
)

var (
	// ErrNotBook: the product has no valid ISBN-13 (DVDs, games, gifts, bundles).
	ErrNotBook = errors.New("not a book: no ISBN-13")
	// ErrNoTitle: nothing usable is left of the product title once cleaned (e.g. only an ISBN).
	ErrNoTitle = errors.New("no title")
	// ErrUnknownShop is a shop ID that is not in All.
	ErrUnknownShop = errors.New("unknown shop")

	// All are the shops whose books can be loaded. Adding a shop on a known platform is one entry; its ID is the
	// works source of its books, and part of their IDs ("<id>:<isbn13>"): lowercase letters and digits only.
	All = []Shop{
		{ID: "chapters", Name: "Chapters Bookstore", URL: "https://chaptersbookstore.com", Platform: Shopify},
	}
)

// Platform is the e-commerce software of a shop's online store.
type Platform string

// Shop is a bookshop with an online store.
type Shop struct {
	ID       string // works source, e.g. "chapters"
	Name     string // shown to readers
	URL      string // the store, e.g. https://chaptersbookstore.com
	Platform Platform
}

// Book is a book a shop lists, cleaned up from its product.
type Book struct {
	Source      string // the shop's ID
	ISBN13      string
	Title       string
	Authors     []string // "First Last"
	Year        int      // edition year; 0 when unknown
	Format      string   // e.g. "paperback"; empty when unknown
	Genres      []string // the shop's categories
	Description string   // plain text, paragraphs separated by a blank line
	URL         string   // product page
	CoverURL    string
	InStock     bool
}

// Item is a product as a platform parsed it: a Book, or the reason it is not one (ErrNotBook, ErrNoTitle).
type Item struct {
	Book Book
	Err  error
}

// Catalog lists the products of a shop's store, page by page, until the last page or the first error of page.
type Catalog interface {
	List(ctx context.Context, page func(items []Item) error) error
}

// Reader reads a shop's books through its catalog. Client must be the one the catalog sends its requests with.
type Reader struct {
	Shop    Shop
	Catalog Catalog
	Client  *Client
}

// Stats counts what Walk did.
type Stats struct {
	Requests, Retries int
	Products          int // seen
	Books             int // handed to the callback
	NotBooks          int // skipped: no ISBN-13
	Untitled          int // skipped: no title once cleaned
	Duplicates        int // skipped: ISBN-13 of a book already handed over
}

// Find returns the shop with this ID.
func Find(id string) (Shop, error) {
	for _, s := range All {
		if s.ID == id {
			return s, nil
		}
	}

	return Shop{}, fmt.Errorf("%w: %q", ErrUnknownShop, id)
}

// ID returns the book's works ID, "<source>:<isbn13>".
func (b Book) ID() string {
	return b.Source + ":" + b.ISBN13
}

// Walk reads every page of the shop's catalog and hands each page's books to fn, with the counts so far. Products
// that are not books, have no title or repeat an ISBN-13 are skipped and counted. It stops at the first error of
// fn or of the catalog.
func (r Reader) Walk(ctx context.Context, fn func(books []Book, s Stats) error) (Stats, error) {
	var s Stats
	seen := make(map[string]bool)
	err := r.Catalog.List(ctx, func(items []Item) error {
		books := make([]Book, 0, len(items))
		for _, it := range items {
			s.Products++
			switch {
			case errors.Is(it.Err, ErrNotBook):
				s.NotBooks++
			case errors.Is(it.Err, ErrNoTitle):
				s.Untitled++
			case it.Err != nil:
				return it.Err
			case seen[it.Book.ISBN13]:
				s.Duplicates++
			default:
				seen[it.Book.ISBN13] = true
				it.Book.Source = r.Shop.ID
				books = append(books, it.Book)
				s.Books++
			}
		}

		s.Requests, s.Retries = r.Client.Counts()

		return fn(books, s)
	})
	s.Requests, s.Retries = r.Client.Counts()

	return s, err
}
