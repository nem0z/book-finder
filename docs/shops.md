# Bookshop catalogues

Besides Open Library, the catalogue holds every book listed by [Chapters Bookstore](https://chaptersbookstore.com)
(Dublin), one `works` row per book with `source = 'chapters'` and id `chapters:<isbn13>`. Books that are also in
Open Library are not merged. `internal/shops` reads the shop; a later loader writes the rows.

## Source

Chapters runs on Shopify (`chapters-ie.myshopify.com`, about 281,000 products). `/products.json` stops at
25,000 products, so the shop is read through the tokenless Storefront GraphQL API instead:
`POST https://chaptersbookstore.com/api/2026-07/graphql.json`, `products(first: 250, after: $cursor)`, about
1,130 requests for the whole catalogue.

The shop rate-limits: it answered 429 after about 15 quick requests and recovered within 90 s. The client
identifies itself (`User-Agent: book-finder/1.0 (+https://github.com/nem0z/book-finder)`), starts a request at most
every 2 s, and tries again after a 429, a 5xx, a network error or a `THROTTLED` GraphQL error, waiting what
`Retry-After` asks or 30 s doubled each time (at most 5 min, 6 tries). Other errors stop the run. A full pass
takes about 45 minutes.

## What is read

| Book field  | From the product                                                                                   |
|-------------|----------------------------------------------------------------------------------------------------|
| ISBN-13     | the SKU of the new variant, else of a second-hand one (`XG`/`XV`/`XE` + the ISBN without `97`); products without a valid 978/979 ISBN-13 (DVDs, gifts) are skipped |
| title       | the title without its `[yyyy] format` suffix, shop codes (`Tpb W2`, `Z90`) and the author before or after a colon; capitals are title-cased; products left with no title (only an ISBN) are skipped |
| year, format | the `[yyyy] format` suffix (edition year, e.g. `paperback`)                                       |
| authors     | the vendor, `Last, First` (several joined by `&`), as `First Last`; `Unknown` is none              |
| genres      | the product type in sentence case, without `BARGAIN` (`FICTION PAPERBACK` -> `Fiction paperback`)  |
| description | `descriptionHtml` as plain text, paragraphs separated by blank lines                               |
| URL, cover  | `onlineStoreUrl` (else `/products/<handle>`), `featuredImage.url` (Shopify CDN)                    |

A product whose ISBN-13 was already read is skipped. On a sample page, 239 of 250 products were kept and 31 of
those had a description under 300 characters, which `cmd/index` skips.

## Terms of use

Chapters' terms of service forbid scraping. The catalogue is read for private use only, by hand, at a slow
pace.

## Hodges Figgis

[Hodges Figgis](https://www.hodgesfiggis.ie/) (Waterstones) is not included: Cloudflare blocks all automated
access to the site.
