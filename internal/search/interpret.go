package search

import "context"

// Interpretation is what a search query asks for: hard filters, and the text retrieval embeds in place of
// the query (understand: the keywords of the wanted book).
type Interpretation struct {
	Filters Filters
	Text    string
}

// Interpreter reads a search query. *understand.Interpreter implements it with an LLM.
type Interpreter interface {
	Interpret(ctx context.Context, query string) (Interpretation, error)
}
