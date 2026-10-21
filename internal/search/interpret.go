package search

import "context"

// Interpretation is what a search query asks for: hard filters, and a description of the book to embed
// in place of the query.
type Interpretation struct {
	Filters     Filters
	Description string
}

// Interpreter reads a search query. *understand.Interpreter implements it with an LLM.
type Interpreter interface {
	Interpret(ctx context.Context, query string) (Interpretation, error)
}
