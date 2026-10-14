package embed

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const (
	// About 1,000 tokens: fits EmbeddingGemma's 2,048-token window with room for the rest.
	maxDescriptionBytes = 4000
	maxTopics           = 15
	hashBytes           = 16
)

// genericTopics carry no signal in an all-fiction catalogue.
var genericTopics = map[string]bool{"fiction": true, "fiction, general": true}

// Book holds the work fields that go into its vector.
type Book struct {
	Title       string
	Subtitle    string
	Authors     []string
	Genres      []string
	Subjects    []string
	Description string
}

// Document builds the text embedded for a book: authors and topics first (they help when the
// description is short), then the description, which carries the plot, mood and themes.
func Document(b Book) Doc {
	title := b.Title
	if b.Subtitle != "" {
		title += ": " + b.Subtitle
	}

	var s strings.Builder
	if len(b.Authors) > 0 {
		s.WriteString("By " + strings.Join(b.Authors, ", ") + "\n")
	}

	if t := topics(b.Genres, b.Subjects); len(t) > 0 {
		s.WriteString("Topics: " + strings.Join(t, "; ") + "\n")
	}

	if d := strings.TrimSpace(b.Description); d != "" {
		s.WriteString("\n" + truncate(d, maxDescriptionBytes))
	}

	return Doc{Title: title, Text: strings.TrimSpace(s.String())}
}

// topics returns genres then subjects, without duplicates (any case) or generic labels.
func topics(genres, subjects []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, list := range [][]string{genres, subjects} {
		for _, t := range list {
			t = strings.TrimSpace(t)
			k := strings.ToLower(t)
			if k == "" || genericTopics[k] || seen[k] {
				continue
			}

			seen[k] = true
			out = append(out, t)
			if len(out) == maxTopics {
				return out
			}
		}
	}

	return out
}

// truncate cuts s to at most max bytes, at a word boundary when there is one in the second half.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}

	cut := strings.LastIndexAny(s[:max], " \n")
	if cut < max/2 {
		cut = max
	}

	return strings.ToValidUTF8(s[:cut], "")
}

// ContentHash identifies the model and text a vector was computed from, so that unchanged works are
// not embedded again after a reload.
func ContentHash(model string, d Doc) string {
	h := sha256.Sum256([]byte(model + "\x00" + d.Title + "\x00" + d.Text))
	return hex.EncodeToString(h[:hashBytes])
}
