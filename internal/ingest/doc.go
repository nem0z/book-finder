// Package ingest loads English-language fiction from the Open Library dumps into Postgres.
//
// It is a full rebuild: final tables are truncated, dumps are streamed into unlogged staging
// tables with COPY, then the final tables are filled in a few set-based statements and the
// secondary indexes are rebuilt.
//
// Passes: redirects -> works (fiction candidates) -> editions (English filter, most complete English
// edition, original language) -> authors -> ratings -> reading log -> finalize. Editions are not stored.
package ingest
