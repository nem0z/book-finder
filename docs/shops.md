# Bookshop catalogues

Besides Open Library, the catalogue holds every book listed by [Chapters Bookstore](https://chaptersbookstore.com)
(Dublin), one `works` row per book with `source = 'chapters'` and id `chapters:<isbn13>`. Books that are also in
Open Library are not merged. `internal/shops` reads the shop and `cmd/shops` writes the rows.

## Refreshing

Run by hand, locally, in this order:

```sh
go run ./cmd/shops -max-pages 1   # optional trial: reads 1 page (250 products), changes nothing
go run ./cmd/shops                # about 45 minutes
LLM_PROVIDER=copilot LLM_MODEL=claude-haiku-5.5 go run ./cmd/index -dry-run   # how many works to extract
LLM_PROVIDER=copilot LLM_MODEL=claude-haiku-5.5 go run ./cmd/index
./deploy/azure/restore-db.ps1     # only to publish the database (see docs/azure.md)
```

`cmd/shops` applies the migrations (`-db`, env `DATABASE_URL`), then reads the whole shop into an unlogged
`stage_shop_works` table, logging every 10 pages. Only once every page is read does it replace the shop's works,
in one transaction: it deletes the `chapters` rows and inserts the staged books, then runs `ANALYZE works`. A
failed or interrupted run (Ctrl+C) drops the stage and leaves the works untouched; with `-max-pages` it stops after
that many pages and changes nothing. Open Library rows and the indexes are left alone, and `work_keywords` has no
foreign key, so `cmd/index` then only extracts new or edited books.

A shop row gets `search_tsv` like an Open Library work (title and authors A, genre B, description C) and the
description with `description_source = 'edition'`. Its `first_publish_year` is the edition year, and it has no
original language, page count, subjects or ratings: filters on language or length exclude it, and `cmd/index`
reaches it last (most-rated first).

## Source

Chapters runs on Shopify (`chapters-ie.myshopify.com`, about 281,000 products). `/products.json` stops at
25,000 products, so the shop is read through the tokenless Storefront GraphQL API instead:
`POST https://chaptersbookstore.com/api/2026-07/graphql.json`, `products(first: 250, sortKey: ID, query: "id:>N")`
with N the numeric ID of the previous page's last product, about 1,130 requests for the whole catalogue. The API's
cursor paging (`after: $cursor`) also stops at 25,000 products; paging on the ID makes each request a fresh
query, so the limit does not apply. The walk fails if the last ID does not parse or does not increase.

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
