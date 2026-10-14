package oldump

import (
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
// one map per kind ("/works/OL45804W" is 45804 in the work map). See docs/redirects.md.
type Redirects map[kind]map[uint32]uint32

// Add registers a redirect. It returns false, and records nothing, when either key is invalid or the
// two keys are of different kinds (a work redirected to an edition).
func (r Redirects) Add(from, to string) bool {
	fromKind, fromNum, _, okFrom := parseKey(from)
	toKind, toNum, _, okTo := parseKey(to)
	if !okFrom || !okTo || fromKind != toKind {
		return false
	}

	m := r[fromKind]
	if m == nil {
		m = map[uint32]uint32{}
		r[fromKind] = m
	}

	m[fromNum] = toNum

	return true
}

// Resolve returns the current full key for a key found in any record: it fixes the key, then
// follows redirects. It returns "" when key is not a valid work, author or edition key.
func (r Redirects) Resolve(key string) string {
	k, n, full, ok := parseKey(key)
	if !ok {
		return ""
	}

	m := r[k]
	start := n
	for range maxRedirectHops {
		next, ok := m[n]
		if !ok || next == n {
			break
		}

		n = next
	}

	if n == start {
		return full
	}

	return kinds[k].path + idPrefix + strconv.FormatUint(uint64(n), 10) + string(kinds[k].letter)
}

// parseKey returns key's kind, ID number and full form ("/<path>/OL<digits><letter>"), and whether
// it is a valid work, author or edition key. Surrounding whitespace is trimmed and a missing path is
// added from the ID's last letter ("OL4791619A" -> "/authors/OL4791619A"). A key whose path and
// letter disagree ("/authors/OL1W") is invalid.
func parseKey(key string) (k kind, n uint32, full string, ok bool) {
	key = strings.TrimSpace(key)
	if key == "" {
		return 0, 0, "", false
	}

	k, ok = kindForLetter(key[len(key)-1])
	if !ok {
		return 0, 0, "", false
	}

	id := key
	if key[0] == '/' {
		if id, ok = strings.CutPrefix(key, kinds[k].path); !ok {
			return 0, 0, "", false
		}
	} else {
		key = kinds[k].path + key
	}

	n, ok = idNumber(id)

	return k, n, key, ok
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
func idNumber(id string) (uint32, bool) {
	digits, ok := strings.CutPrefix(id, idPrefix)
	if !ok || len(digits) < 2 {
		return 0, false
	}

	digits = digits[:len(digits)-1]
	if digits[0] == '0' || digits[0] == '+' {
		return 0, false
	}

	n, err := strconv.ParseUint(digits, 10, 32)

	return uint32(n), err == nil
}
