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

The number is unique within a type only (`OL123W` and `OL123A` are different records), so the full
ID is used as the map key.

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

## Rules

Implemented in `oldump.CanonicalKey` and `internal/ingest/redirects.go`.

1. **Canonical form.** Every ID is converted to `/<path>/OL<digits><letter>` before use. An ID without
   its path gets the path matching its last letter (`OL4791619A` → `/authors/OL4791619A`).
2. **Invalid IDs are skipped**: other record types (`/about/team`), Internet Archive identifiers
   (`/books/ia:...`), a letter that does not match the path (`/authors/OL24320672W`), a missing letter
   (`/authors/OL123`).
3. **Only work and author redirects are kept.** Editions are not stored, so their redirects are never
   followed. A redirect whose target is a different type (work → edition) is skipped.
4. **Resolving.** `resolve(id)` converts any ID found in a record (work author lists, edition work
   references, ratings, reading log) to its canonical form, then follows redirects up to 10 hops (longer
   chains are loops). A path-less ID in a record therefore resolves exactly like the full one, without
   storing both forms in the map. It returns `""` for invalid IDs, which callers skip.

Result on the 2026-09-30 dump: 1,702,518 redirects kept, 4,576 targets fixed, 116,003 skipped.

## Storage: in memory, rebuilt at each load

| | |
|---|---|
| Read + decompress + build the map | ~4.5 s |
| Memory (map of full IDs, works and authors only) | ~154 MB |

The map is rebuilt from the redirects dump at the start of every load and is not stored in Postgres:

- it is cheap to rebuild (~5 s) compared with the rest of the load;
- each monthly dump adds new merges, so a stored table would need refreshing anyway;
- only the loader needs it; nothing else translates old IDs today.

**Considered and rejected:** keying the map by the numeric part of the ID (`OL45804W` → `45804`, one
map per type). It would use ~28 MB instead of ~154 MB, but a lookup was slower in a benchmark
(~180 ns vs ~83 ns, because the number must be parsed first), and 154 MB is acceptable.

## Known gap

The editions pass finds an edition's work ID by searching the raw line for `"/works/OL` before decoding
(to skip most of the editions file cheaply). An edition whose work reference has no path would be
missed by that search. To check when the editions pass is built, using the editions dump.
