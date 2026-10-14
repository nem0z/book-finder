package oldump

import "strings"

// Paths that start a full Open Library key, e.g. "/works/OL45804W".
const (
	WorksPath    = "/works/"
	AuthorsPath  = "/authors/"
	EditionsPath = "/books/"
)

const (
	// Record kinds by the letter that ends an Open Library ID: OL45804W is a work.
	workLetter    = 'W'
	authorLetter  = 'A'
	editionLetter = 'M'

	idPrefix = "OL"

	// Redirect chains longer than this are treated as loops.
	maxRedirectHops = 10
)

// Redirects maps merged Open Library keys to the keys that replaced them. Keys are stored in full
// form ("/works/OL45804W"). See docs/redirects.md.
type Redirects map[string]string

// Add registers a redirect. It returns false, and records nothing, when either key is invalid or the
// two keys are of different kinds (a work redirected to an edition).
func (r Redirects) Add(from, to string) bool {
	from, okFrom := fixKey(from)
	to, okTo := fixKey(to)
	if !okFrom || !okTo || kind(from) != kind(to) {
		return false
	}

	r[from] = to

	return true
}

// Resolve returns the current full key for a key found in any record: it fixes the key, then
// follows redirects. It returns "" when key is not a valid work, author or edition key.
func (r Redirects) Resolve(key string) string {
	key, ok := fixKey(key)
	if !ok {
		return ""
	}

	for range maxRedirectHops {
		next, ok := r[key]
		if !ok || next == key {
			return key
		}

		key = next
	}

	return key
}

// fixKey returns key in full form, "/<path>/OL<digits><letter>", and whether it is a valid work,
// author or edition key. Surrounding whitespace is trimmed and a missing path is added from the
// ID's last letter ("OL4791619A" -> "/authors/OL4791619A").
func fixKey(key string) (string, bool) {
	key = strings.TrimSpace(key)
	if key != "" && key[0] != '/' {
		path, ok := pathForLetter(key)
		if !ok {
			return "", false
		}

		key = path + key
	}

	path, id, ok := splitKey(key)
	if !ok || !validID(id) {
		return "", false
	}

	if want, _ := pathForLetter(id); path != want {
		return "", false
	}

	return key, true
}

// pathForLetter returns the path for an ID's last letter.
func pathForLetter(id string) (string, bool) {
	if id == "" {
		return "", false
	}

	switch id[len(id)-1] {
	case workLetter:
		return WorksPath, true
	case authorLetter:
		return AuthorsPath, true
	case editionLetter:
		return EditionsPath, true
	default:
		return "", false
	}
}

// splitKey splits "/works/OL1W" into "/works/" and "OL1W".
func splitKey(key string) (path, id string, ok bool) {
	for _, p := range []string{WorksPath, AuthorsPath, EditionsPath} {
		if id, found := strings.CutPrefix(key, p); found {
			return p, id, true
		}
	}

	return "", "", false
}

// validID reports whether id is "OL", at least one digit, then a letter.
func validID(id string) bool {
	if len(id) < len(idPrefix)+2 || !strings.HasPrefix(id, idPrefix) {
		return false
	}

	for _, c := range id[len(idPrefix) : len(id)-1] {
		if c < '0' || c > '9' {
			return false
		}
	}

	_, ok := pathForLetter(id)

	return ok
}

// kind returns the path of a full key, e.g. "/works/".
func kind(key string) string {
	return key[:strings.LastIndexByte(key, '/')+1]
}
