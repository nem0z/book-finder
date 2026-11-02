// Package woocommerce lists the products of a WooCommerce store through its public Store API, as books.
package woocommerce

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"slices"
	"strconv"
	"strings"

	"github.com/nem0z/book-finder/internal/shops"
)

// pageSize is the most products the Store API returns per request.
const pageSize = 100

// notGenres are store categories that say how a book is sold, not what it is about.
var notGenres = []string{"new books", "book bundles", "pre-orders", "preorders", "gift cards", "uncategorised", "uncategorized"}

// Catalog lists a WooCommerce store's products.
type Catalog struct {
	shop   shops.Shop
	client *shops.Client
}

// product is a product of the Store API (wp-json/wc/store/v1/products).
type product struct {
	Name             string `json:"name"`
	Permalink        string `json:"permalink"`
	SKU              string `json:"sku"`
	ShortDescription string `json:"short_description"`
	Description      string `json:"description"`
	IsInStock        bool   `json:"is_in_stock"`
	Images           []struct {
		Src string `json:"src"`
	} `json:"images"`
	Categories []struct {
		Name string `json:"name"`
	} `json:"categories"`
}

// New returns the catalog of shop, whose requests client sends.
func New(shop shops.Shop, client *shops.Client) *Catalog {
	shop.URL = strings.TrimRight(shop.URL, "/")
	return &Catalog{shop: shop, client: client}
}

// List pages through every product by increasing ID, so products added during a run land on later pages.
func (c *Catalog) List(ctx context.Context, page func(items []shops.Item) error) error {
	for n := 1; ; n++ {
		products, pages, err := c.fetch(ctx, n)
		if err != nil {
			return err
		}

		items := make([]shops.Item, len(products))
		for i, p := range products {
			items[i].Book, items[i].Err = parseProduct(p)
		}

		if err := page(items); err != nil {
			return err
		}

		if n >= pages || len(products) == 0 {
			return nil
		}
	}
}

// fetch gets page n of the products and the number of pages (X-WP-TotalPages).
func (c *Catalog) fetch(ctx context.Context, n int) ([]product, int, error) {
	url := fmt.Sprintf("%s/wp-json/wc/store/v1/products?per_page=%d&page=%d&orderby=id&order=asc", c.shop.URL, pageSize, n)
	var products []product
	resp, err := c.client.Do(ctx, shops.Request{Method: "GET", URL: url}, func(r shops.Response) error {
		products = nil
		if err := json.Unmarshal(r.Body, &products); err != nil {
			return fmt.Errorf("bad response: %w", err)
		}

		return nil
	})
	if err != nil {
		return nil, 0, fmt.Errorf("woocommerce: %w", err)
	}

	pages, err := strconv.Atoi(resp.Header.Get("X-WP-TotalPages"))
	if err != nil {
		return nil, 0, fmt.Errorf("woocommerce: bad X-WP-TotalPages %q", resp.Header.Get("X-WP-TotalPages"))
	}

	return products, pages, nil
}

// parseProduct turns a product into a Book. It returns shops.ErrNotBook or shops.ErrNoTitle for a product to skip.
// The author is only in the short description, "Title | ISBN | Author".
func parseProduct(p product) (shops.Book, error) {
	isbn := strings.TrimSpace(p.SKU)
	if !shops.ValidISBN13(isbn) {
		return shops.Book{}, shops.ErrNotBook
	}

	title := strings.TrimSpace(html.UnescapeString(p.Name))
	if title == "" || title == isbn {
		return shops.Book{}, shops.ErrNoTitle
	}

	b := shops.Book{
		ISBN13:      isbn,
		Title:       title,
		Description: shops.HTMLText(p.Description),
		URL:         p.Permalink,
		InStock:     p.IsInStock,
	}
	if parts := strings.Split(shops.HTMLText(p.ShortDescription), "|"); len(parts) >= 3 {
		b.Authors = shops.AuthorNames(parts[len(parts)-1])
	}

	for _, cat := range p.Categories {
		g := strings.TrimSpace(html.UnescapeString(cat.Name))
		if g != "" && !slices.Contains(notGenres, strings.ToLower(g)) {
			b.Genres = append(b.Genres, g)
		}
	}

	if len(p.Images) > 0 {
		b.CoverURL = p.Images[0].Src
	}

	return b, nil
}
