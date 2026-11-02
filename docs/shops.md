# Bookshop catalogues

Besides Open Library, the catalogue holds every book listed by Dublin bookshops with an online store: one `works`
row per book and shop, with `source` = the shop's ID and id `<source>:<isbn13>`. Books that are also in Open Library,
or at several shops, are separate rows.

| Shop | ID | Platform | Products |
|---|---|---|---|
| [Chapters Bookstore](https://chaptersbookstore.com) | `chapters` | Shopify | about 281,000 |

## Code

`internal/shops` holds what every shop shares: the registry of shops (`shops.All`: ID, name, store URL, platform),
the `Book` model, the paced HTTP client, text helpers (ISBN-13 check, HTML to text, author names) and the walk that
counts and skips products (not a book, no title, ISBN-13 already read). Each
platform has a package that only builds its requests and parses its products into books: `internal/shops/shopify`.

**Adding a shop** on a known platform is one entry in `shops.All`; its ID (lowercase letters and digits) becomes the
works source, so no migration is needed. Check the shop's rules and how its products carry the ISBN and author before adding it.

The client identifies itself (`User-Agent: book-finder/1.0 (+https://github.com/nem0z/book-finder)`), starts a
request at most every 2 s, and tries again after a 429, a 5xx, a network error, a body cut short or an error the
platform marks as worth retrying (Shopify's `THROTTLED`), waiting what `Retry-After` asks or 30 s doubled each
time (at most 5 min, 6 tries). Other errors stop that shop's run.

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

## Hodges Figgis

[Hodges Figgis](https://www.hodgesfiggis.ie/) (Waterstones) is not included: Cloudflare blocks all automated
access to the site.
