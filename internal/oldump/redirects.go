package oldump

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Paths that start a full Open Library key, e.g. "/works/OL45804W".
const (
	WorksPath    = "/works/"
	AuthorsPath  = "/authors/"
	EditionsPath = "/books/"
)

const (
	idPrefix = "OL"

	// Redirect chains longer than this are treated as loops.
	maxRedirectHops = 10
)

var (
	ErrInvalidKey   = errors.New("invalid Open Library key")
	ErrKindMismatch = errors.New("redirect between different kinds of record")
	ErrRedirectLoop = errors.New("redirect loop")
)

// kind is the type of record a key points to. The number in an ID is unique within a kind only:
// OL123W and OL123A are different records.
type kind uint8

const (
	work kind = iota
	author
	edition
)

// kinds gives each kind's path and the letter that ends its IDs (OL45804W is a work).
var kinds = [...]struct {
	path   string
	letter byte
}{
	work:    {WorksPath, 'W'},
	author:  {AuthorsPath, 'A'},
	edition: {EditionsPath, 'M'},
}

// Redirects maps merged Open Library records to the records that replaced them, by ID number, with
// one map per kind ("/works/OL45804W" is 45804 in the works map). Create it with NewRedirects.
// See docs/redirects.md.
type Redirects struct {
	works, authors, editions map[uint32]uint32
}

func NewRedirects() *Redirects {
	return &Redirects{works: map[uint32]uint32{}, authors: map[uint32]uint32{}, editions: map[uint32]uint32{}}
}

// Len returns the number of redirects stored.
func (r *Redirects) Len() int {
	return len(r.works) + len(r.authors) + len(r.editions)
}

// mapFor returns the map holding redirects of kind k.
func (r *Redirects) mapFor(k kind) map[uint32]uint32 {
	switch k {
	case work:
		return r.works
	case author:
		return r.authors
	default:
		return r.editions
	}
}

// Add registers a redirect. It returns an error, and records nothing, when either key is invalid or
// the two keys are of different kinds (a work redirected to an edition).
func (r *Redirects) Add(from, to string) error {
	fromKind, fromNum, err := parseKey(from)
	if err != nil {
		return fmt.Errorf("redirect source: %w", err)
	}

	toKind, toNum, err := parseKey(to)
	if err != nil {
		return fmt.Errorf("redirect target: %w", err)
	}

	if fromKind != toKind {
		return fmt.Errorf("%w: %q -> %q", ErrKindMismatch, from, to)
	}

	r.mapFor(fromKind)[fromNum] = toNum

	return nil
}

// Resolve returns the current full key for a key found in any record: it fixes the key, then
// follows redirects. It returns an error when the key is invalid or the redirects loop.
func (r *Redirects) Resolve(key string) (string, error) {
	k, n, err := parseKey(key)
	if err != nil {
		return "", err
	}

	m := r.mapFor(k)
	next, ok := m[n]
	for hops := 0; ok && next != n; hops++ {
		if hops == maxRedirectHops {
			return "", fmt.Errorf("%w: %q still redirected after %d hops", ErrRedirectLoop, key, maxRedirectHops)
		}

		n = next
		next, ok = m[n]
	}

	return kinds[k].path + idPrefix + strconv.FormatUint(uint64(n), 10) + string(kinds[k].letter), nil
}

// parseKey returns the kind and ID number of a work, author or edition key, or an error saying why
// the key is invalid. Surrounding whitespace is trimmed and a missing path is accepted, the kind
// coming from the ID's last letter ("OL4791619A" is an author). A key whose path and letter disagree
// ("/authors/OL1W") is invalid.
func parseKey(key string) (kind, uint32, error) {
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return 0, 0, fmt.Errorf("%w: empty", ErrInvalidKey)
	}

	k, ok := kindForLetter(trimmed[len(trimmed)-1])
	if !ok {
		return 0, 0, fmt.Errorf("%w %q: does not end with W, A or M", ErrInvalidKey, key)
	}

	id := trimmed
	if trimmed[0] == '/' {
		if id, ok = strings.CutPrefix(trimmed, kinds[k].path); !ok {
			return 0, 0, fmt.Errorf("%w %q: path does not match the letter %c", ErrInvalidKey, key, kinds[k].letter)
		}
	}

	n, err := idNumber(id)
	if err != nil {
		return 0, 0, fmt.Errorf("%w %q: %w", ErrInvalidKey, key, err)
	}

	return k, n, nil
}

func kindForLetter(c byte) (kind, bool) {
	for k, info := range kinds {
		if info.letter == c {
			return kind(k), true
		}
	}

	return 0, false
}

// idNumber returns the number in "OL<digits><letter>". Leading zeros are rejected so that two
// spellings cannot share a number; none appear in the dumps.
func idNumber(id string) (uint32, error) {
	digits, ok := strings.CutPrefix(id, idPrefix)
	if !ok || len(digits) < 2 {
		return 0, fmt.Errorf("not OL<digits><letter>")
	}

	digits = digits[:len(digits)-1]
	if digits[0] == '0' || digits[0] == '+' {
		return 0, fmt.Errorf("number %q has a leading zero or sign", digits)
	}

	n, err := strconv.ParseUint(digits, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("number %q is not a 32-bit number", digits)
	}

	return uint32(n), nil
}
