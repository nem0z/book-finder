// Package shopify lists the products of a Shopify store through the tokenless Storefront GraphQL API, as books.
package shopify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/nem0z/book-finder/internal/shops"
)

const (
	APIVersion = "2026-07"

	// pageSize is the most products the API returns per request.
	pageSize = 250

	// productsQuery pages on the product ID ("id:>N") rather than with cursors, which stop at 25,000 products.
	productsQuery = `query($first: Int!, $query: String) {
  products(first: $first, sortKey: ID, query: $query) {
    pageInfo { hasNextPage }
    nodes {
      id handle title vendor productType descriptionHtml onlineStoreUrl availableForSale
      featuredImage { url }
      variants(first: 4) { nodes { sku } }
    }
  }
}`
)

// Catalog lists a Shopify store's products.
type Catalog struct {
	shop   shops.Shop
	client *shops.Client
}

type graphQLRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

// graphQLResponse is an answer to productsQuery. Its fields are pointers so that a missing or null part, which
// must not pass for the end of the catalogue, can be told apart from an empty one.
type graphQLResponse struct {
	Data *struct {
		Products *struct {
			PageInfo *struct {
				HasNextPage *bool `json:"hasNextPage"`
			} `json:"pageInfo"`
			Nodes []product `json:"nodes"`
		} `json:"products"`
	} `json:"data"`
	Errors []struct {
		Message    string `json:"message"`
		Extensions struct {
			Code string `json:"code"`
		} `json:"extensions"`
	} `json:"errors"`
}

// New returns the catalog of shop, whose requests client sends.
func New(shop shops.Shop, client *shops.Client) *Catalog {
	shop.URL = strings.TrimRight(shop.URL, "/")
	return &Catalog{shop: shop, client: client}
}

// List pages through every product by increasing ID.
func (c *Catalog) List(ctx context.Context, page func(items []shops.Item) error) error {
	var after uint64
	for {
		resp, err := c.fetch(ctx, after)
		if err != nil {
			return err
		}

		nodes := resp.Data.Products.Nodes
		items := make([]shops.Item, len(nodes))
		for i, p := range nodes {
			items[i].Book, items[i].Err = parseProduct(p, c.shop.URL)
		}

		if err := page(items); err != nil {
			return err
		}

		if !*resp.Data.Products.PageInfo.HasNextPage || len(nodes) == 0 {
			return nil
		}

		last, err := productID(nodes[len(nodes)-1].ID)
		if err != nil {
			return err
		}

		if last <= after {
			return fmt.Errorf("shopify: product IDs do not increase: %d after %d", last, after)
		}

		after = last
	}
}

// fetch gets the page of products whose ID is above after (0: the first page).
func (c *Catalog) fetch(ctx context.Context, after uint64) (*graphQLResponse, error) {
	vars := map[string]any{"first": pageSize, "query": nil}
	if after > 0 {
		vars["query"] = fmt.Sprintf("id:>%d", after)
	}

	body, err := json.Marshal(graphQLRequest{Query: productsQuery, Variables: vars})
	if err != nil {
		return nil, err
	}

	var out graphQLResponse
	req := shops.Request{Method: "POST", URL: c.shop.URL + "/api/" + APIVersion + "/graphql.json", Body: body}
	_, err = c.client.Do(ctx, req, func(r shops.Response) error {
		out = graphQLResponse{}
		if err := json.Unmarshal(r.Body, &out); err != nil {
			return fmt.Errorf("bad response: %w", err)
		}

		return graphQLError(out)
	})
	if err != nil {
		return nil, fmt.Errorf("shopify: %w", err)
	}

	return &out, nil
}

// graphQLError returns the errors of an answer, to try again when the store throttled the request.
func graphQLError(out graphQLResponse) error {
	if len(out.Errors) > 0 {
		msgs := make([]string, len(out.Errors))
		throttled := false
		for i, e := range out.Errors {
			msgs[i] = e.Message
			throttled = throttled || e.Extensions.Code == "THROTTLED"
		}

		err := fmt.Errorf("GraphQL: %s", strings.Join(msgs, "; "))
		if throttled {
			return shops.Retry(err)
		}

		return err
	}

	switch d := out.Data; {
	case d == nil:
		return errors.New("bad response: no data")
	case d.Products == nil || d.Products.Nodes == nil:
		return errors.New("bad response: no products")
	case d.Products.PageInfo == nil || d.Products.PageInfo.HasNextPage == nil:
		return errors.New("bad response: no pageInfo.hasNextPage")
	}

	return nil
}

// productID returns the number of a product's ID ("gid://shopify/Product/7248452387001").
func productID(gid string) (uint64, error) {
	n, err := strconv.ParseUint(gid[strings.LastIndex(gid, "/")+1:], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("shopify: bad product ID %q", gid)
	}

	return n, nil
}
