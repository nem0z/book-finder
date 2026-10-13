package ingest

// Record type column that starts a dump line, tab included, so passes can skip other lines
// before splitting them.
var (
	typeRedirect = []byte("/type/redirect\t")
	typeWork     = []byte("/type/work\t")
)
