package understand

import (
	"fmt"
	"io"
	"time"

	"github.com/nem0z/book-finder/internal/llm"
)

// FromConfig returns the interpreter that asks the model c describes, and how long to wait for it
// (timeout, e.g. "5s"). It returns nil when c names no provider: the search then embeds queries as they
// are. Assign it to search.Engine.Interpreter only when non-nil.
func FromConfig(c llm.Config, timeout string) (*Interpreter, time.Duration, error) {
	if c.Provider == "" {
		return nil, 0, nil
	}

	d, err := time.ParseDuration(timeout)
	if err != nil || d <= 0 {
		return nil, 0, fmt.Errorf("%w: LLM timeout %q, want a positive duration such as 5s", llm.ErrConfig, timeout)
	}

	client, err := llm.New(c)
	if err != nil {
		return nil, 0, err
	}

	return New(client), d, nil
}

// Close frees what the model client holds, such as the Copilot runtime. A nil Interpreter is fine.
func (in *Interpreter) Close() error {
	if in == nil {
		return nil
	}

	if c, ok := in.llm.(io.Closer); ok {
		return c.Close()
	}

	return nil
}
