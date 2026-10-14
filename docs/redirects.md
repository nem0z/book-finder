# Redirects (merged records)

When Open Library librarians merge duplicate records, one record survives and the others become
**redirects**: small records saying "this ID now lives at another ID". Other records (editions,
ratings, reading-log entries, a work's author list) are not always updated after a merge, so they can
still point to an old ID. The loader resolves every work and author ID it follows through these
redirects, so data attached to an old ID still reaches the surviving record.

## ID format

`/works/OL45804W` = path (`/works/`) + `OL` + a number + a letter for the record type:

| Letter | Type | Path |
|---|---|---|
| `W` | work | `/works/` |
| `A` | author | `/authors/` |
| `M` | edition | `/books/` |

The number is unique within a type only (`OL123W` and `OL123A` are different records), so redirects are
stored by number in one map per type. In the dump the numbers have no leading zeros and the largest is
58.9M, so a 32-bit number holds them without losing anything.

## What the redirects dump contains

Measured on `ol_dump_redirects_2026-09-30.txt.gz` (71.6 MB):

| | Count |
|---|---|
| Lines (all `/type/redirect`) | 1,818,521 |
| Work redirects | 1,150,009 |
| Author redirects | 552,522 |
| Edition redirects | 115,947 |
| Other (`/about/team`, `/books/ia:...`, `/olcc...`, ...) | 43 |
| Targets without their path (e.g. `OL4791619A`) | 6,942 (2,329 W, 2,247 A, 2,366 M) |
| Targets whose path does not match the source's type | ~13 |
| Redirects needing more than one hop | 163,308 (longest chain: 9) |
| Loops | 26 |

Path-less IDs only appear as **targets**, never as sources, and their letter always matches the
source's type. The full form (with the path) is the correct one.

## The `oldump.Redirects` type

One map per type (work, author, edition), from old ID number to new ID number
(`/works/OL45804W` is 45804 in the work map), with two methods:

- **`Add(from, to)`** fixes both keys and stores the redirect. It returns false, and stores nothing, when
  either key is invalid or the two keys are of different kinds (work → edition).
- **`Resolve(key)`** fixes the key, then follows redirects up to 10 hops (longer chains are loops). It
  returns `""` for an invalid key, which callers skip. Every key read from a record (a work's authors, an
  edition's work, ratings, reading log) goes through `Resolve` before use.

Fixing a key (private helper):

1. Surrounding whitespace is trimmed.
2. A key without its path gets the path matching its last letter (`OL4791619A` → `/authors/OL4791619A`).
   A path-less key in a record therefore resolves exactly like the full one, without storing both forms.
3. Anything that is not `/<path>/OL<digits><letter>` with a letter matching the path is invalid: site pages
   (`/about/team`), Internet Archive identifiers (`/books/ia:...`), a mismatched letter
   (`/authors/OL24320672W`), a missing letter (`/authors/OL123`).

Result on the 2026-09-30 dump: 1,818,431 redirects kept (4,576 targets needed their path added), 90 skipped:

| Skipped because | Count | Example |
|---|---|---|
| Source is not a book record (site pages) | 88 | `/about/people` → `/about/team` |
| Target of another type | 1 | `/books/OL23693907M` → `/authors/OL6120853A` |
| Target is not an ID | 1 | `/books/OL7246278M` → `Diplomatarium Islandicum, Bd. 1` |

One author redirect has a stray tab before its target (`"\t/authors/OL3281458A"`); trimming recovers it.
Edition redirects (115,912) are kept too: nothing follows them yet, and they cost about 2 MB.

## Storage: in memory, rebuilt at each load

| | |
|---|---|
| Read + decompress the dump | ~5 s |
| Build the map | ~0.4 s |
| Memory | ~29 MB |
| `Resolve`, key not redirected / redirected | ~156 ns / ~361 ns |

The map is rebuilt from the redirects dump at the start of every load and is not stored in Postgres:

- it is cheap to rebuild (~6 s) compared with the rest of the load;
- each monthly dump adds new merges, so a stored table would need refreshing anyway;
- only the loader needs it; nothing else translates old IDs today.

**Numbers rather than full keys:** a map keyed by the full text key used ~190 MB. With the same key
fixing, it was also slightly slower: ~175 ns and ~400 ns for the two cases above.

## Checked: edition work references

The editions pass finds an edition's work key by searching the raw line for `"/works/OL` before decoding.
An edition whose work reference had no path would be missed; the 2026-09-30 editions dump has none.
