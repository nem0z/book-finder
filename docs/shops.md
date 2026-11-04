# Bookshop catalogues

Besides Open Library, the catalogue holds every book listed by Dublin bookshops with an online store: one `works`
row per book and shop, with `source` = the shop's ID and id `<source>:<isbn13>`. Books that are also in Open Library,
or at several shops, are separate rows; search brings them together by ISBN-13 (see [Searching](#searching)).

| Shop | ID | Platform | Products |
|---|---|---|---|
| [Chapters Bookstore](https://chaptersbookstore.com) | `chapters` | Shopify | about 281,000 |
| [Books Upstairs](https://booksupstairs.ie) | `booksupstairs` | WooCommerce | about 26,000 |

## Code

`internal/shops` holds what every shop shares: the registry of shops (`shops.All`: ID, name, store URL, platform),
the `Book` model, the paced HTTP client, text helpers (ISBN-13 check, HTML to text, author names), the walk that
counts and skips products (not a book, no title, ISBN-13 already read) and the staging of a shop's books. Each
platform has a package that only builds its requests and parses its products into books: `internal/shops/shopify`
and `internal/shops/woocommerce`. `cmd/shops` picks the package from the shop's platform.

**Adding a shop** on a known platform is one entry in `shops.All`; its ID (lowercase letters and digits) becomes the
works source, so no migration is needed. Other Dublin shops checked on 2026-11-02 run on the same platforms: The
Gutter Bookshop, An Siopa Leabhar and Ulysses Rare Books on WooCommerce, The Secret Book & Record Store on Shopify.
Check the shop's rules and how its products carry the ISBN and author before adding it.

The client identifies itself (`User-Agent: book-finder/1.0 (+https://github.com/nem0z/book-finder)`), starts a
request at most every 2 s, and tries again after a 429, a 5xx, a network error, a body cut short or an error the
platform marks as worth retrying (Shopify's `THROTTLED`), waiting what `Retry-After` asks or 30 s doubled each
time (at most 5 min, 6 tries). Other errors stop that shop's run.

## Refreshing

Run by hand, locally, in this order:

```sh
go run ./cmd/shops -shop chapters -max-pages 1   # optional trial: reads 1 page, changes nothing
go run ./cmd/shops                                # every shop, one after the other (-shop <id> for one)
LLM_PROVIDER=copilot LLM_MODEL=claude-haiku-5.5 go run ./cmd/index -dry-run   # how many works to extract
LLM_PROVIDER=copilot LLM_MODEL=claude-haiku-5.5 go run ./cmd/index
./deploy/azure/restore-db.ps1     # only to publish the database (see docs/azure.md)
```

`cmd/shops` applies the migrations (`-db`, env `DATABASE_URL`), then reads each shop into its own unlogged
`stage_works_<id>` table, logging every 10 pages. Only once every page is read does it replace the shop's works,
in one transaction: it deletes the shop's rows and inserts the staged books, then runs `ANALYZE works`. A failed or
interrupted run (Ctrl+C) drops the stage and leaves that shop's works untouched, and the next shop is still loaded;
with `-max-pages` it stops after that many pages (or at the end of a shorter catalogue) and changes nothing. A run
holds a lock on its shop (a Postgres advisory lock) from staging to replacing, so a second run on the same shop
stops at once. A run that staged fewer than half as many books as the shop has refuses to replace them, in case
the catalogue answer was cut short; `-allow-shrink` overrides that. Open Library rows, other shops' rows and the
indexes are left alone, and `work_keywords` has no foreign key, so `cmd/index` then only extracts new or edited
books.

A shop row gets `search_tsv` like an Open Library work (title and authors A, genres B, description C), the
description with `description_source = 'edition'`, and `in_stock`. Its `first_publish_year` is the edition year
when the shop gives one, and it has no original language, page count, subjects or ratings: filters on language or
length exclude it, and `cmd/index` reaches it last (most-rated first).

## Searching

Once `cmd/index` gave them keyword vectors, shop rows are searched like Open Library works. "In a Dublin bookshop"
on the web page (`shop=1` on `GET /search`, `-shop` for `cmd/search`) keeps only shop rows
(`source <> 'openlibrary'`), filtered while walking the HNSW index like the other filters. Shop rows with the same
ISBN-13 (the book at several shops) come back once, the nearest, so a search can return a few less candidates than
asked. Every result lists the shops selling its ISBN-13 (`shops`: ID, name from `shops.All`, product page, in
stock), Open Library results included; the web card links to each. A shop result shows the shop's cover. Year
filters use the edition year, and page or language filters (typed, or read from the query by the LLM) exclude shop
rows, so with the toggle on they return nothing.

## Shopify: Chapters Bookstore

`/products.json` stops at 25,000 products, so the store is read through the tokenless Storefront GraphQL API:
`POST <store>/api/2026-07/graphql.json`, `products(first: 250, sortKey: ID, query: "id:>N")` with N the numeric ID
of the previous page's last product, about 1,130 requests for Chapters. The API's cursor paging also stops at
25,000 products; paging on the ID makes each request a fresh query, so the limit does not apply. The walk fails if
the last ID does not parse or does not increase. Chapters answered 429 after about 15 quick requests and recovered
within 90 s; a full pass takes about 40 minutes.

| Book field  | From the product                                                                                   |
|-------------|----------------------------------------------------------------------------------------------------|
| ISBN-13     | the SKU of the new variant, else of a second-hand one (`XG`/`XV`/`XE` + the ISBN without `97`); products without a valid 978/979 ISBN-13 (DVDs, gifts) are skipped |
| title       | the title without its `[yyyy] format` suffix, shop codes (`Tpb W2`, `Z90`) and the author before or after a colon; capitals are title-cased; products left with no title (only an ISBN) are skipped |
| year, format | the `[yyyy] format` suffix (edition year, e.g. `paperback`)                                       |
| authors     | the vendor, `Last, First` (several joined by `&`), as `First Last`; `Unknown` is none              |
| genres      | the product type in sentence case, without `BARGAIN` (`FICTION PAPERBACK` -> `Fiction paperback`)  |
| description | `descriptionHtml` as plain text, paragraphs separated by blank lines                               |
| URL, cover, in stock | `onlineStoreUrl` (else `/products/<handle>`), `featuredImage.url`, `availableForSale`      |

Chapters' terms of service forbid scraping. Its catalogue is read for private use only, by hand, at a slow pace.

## WooCommerce: Books Upstairs

The public Store API needs no key: `GET <store>/wp-json/wc/store/v1/products?per_page=100&page=N&orderby=id&order=asc`,
until the last page announced by `X-WP-TotalPages`. An empty page before it, or an answer that is not a list of
products, stops the run with an error rather than passing for the end of the catalogue. Ordering by ID puts products
added during a run on later pages, so none is missed or read twice. Books Upstairs has 260 pages; a full pass takes
about 10 minutes.

| Book field  | From the product                                                                                   |
|-------------|----------------------------------------------------------------------------------------------------|
| ISBN-13     | `sku`; products without a valid 978/979 ISBN-13 (bundles, gift cards) are skipped                  |
| title       | `name`, HTML entities decoded; products without a name are skipped                                |
| authors     | the last part of `short_description` (`Title \| ISBN \| Author`), several joined by `&` or `;`     |
| genres      | `categories`, without those about how a book is sold (`New Books`, `Book Bundles`, ...)            |
| description | `description` as plain text                                                                        |
| URL, cover, in stock | `permalink`, `images[0].src`, `is_in_stock`                                               |

Books Upstairs gives no year or format. Its robots.txt allows the API, and the site has no terms of use.

## Hodges Figgis

[Hodges Figgis](https://www.hodgesfiggis.ie/) (Waterstones) is not included: Cloudflare blocks all automated
access to the site.
