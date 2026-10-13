package oldump

import "strings"

// Paths that start a full Open Library key, e.g. "/works/OL45804W".
const (
	WorksPath    = "/works/"
	AuthorsPath  = "/authors/"
	EditionsPath = "/books/"
)

// Record kinds by the letter that ends an Open Library ID: OL45804W is a work.
const (
	workLetter    = 'W'
	authorLetter  = 'A'
	editionLetter = 'M'

	idPrefix = "OL"
)

// CanonicalKey returns key in its full form, "/<kind>/OL<digits><letter>", and whether it is a
// valid work, author or edition ID.
//
// IDs without their path ("OL45804W", seen in about 7k redirect targets) get the path that matches
// their final letter. Anything else is invalid: other record types ("/about/team", "/books/ia:..."),
// a letter that does not match the path ("/authors/OL1W") or a missing letter ("/authors/OL1").
func CanonicalKey(key string) (string, bool) {
	key = strings.TrimSpace(key)
	if len(key) > 0 && key[0] != '/' {
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

// pathForLetter returns the path for an ID's final letter.
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
		if len(key) > len(p) && key[:len(p)] == p {
			return p, key[len(p):], true
		}
	}

	return "", "", false
}

// validID reports whether id is "OL", at least one digit, then a letter.
func validID(id string) bool {
	if len(id) < len(idPrefix)+2 || id[:len(idPrefix)] != idPrefix {
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
