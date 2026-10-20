package understand

import (
	"errors"
	"fmt"
	"time"

	"github.com/nem0z/book-finder/internal/search"
)

var ErrNoModel = errors.New("an LLM URL needs a model (LLM_MODEL)")

// FromConfig returns the interpreter the commands' LLM settings describe, and how long to wait for it.
// An empty url turns it off: the interpreter is nil and the search embeds queries as they are.
func FromConfig(url, model, apiKey, timeout string) (search.Interpreter, time.Duration, error) {
	if url == "" {
		return nil, 0, nil
	}

	if model == "" {
		return nil, 0, ErrNoModel
	}

	d, err := time.ParseDuration(timeout)
	if err != nil || d <= 0 {
		return nil, 0, fmt.Errorf("LLM timeout %q: want a positive duration such as 5s", timeout)
	}

	return New(url, model, apiKey), d, nil
}
