package embed

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const (
	// ~1,000 tokens: fits EmbeddingGemma's 2,048-token window with room for title, authors and topics.
	maxDescriptionChars = 4000
	maxTopics           = 15
)

// Book holds the work fields that go into its embedding.
type Book struct {
	ID          string
	Title       string
	Subtitle    string
	Authors     []string
	Genres      []string
	Subjects    []string
	Description string
}

// Document builds the text embedded for a book. The body lists authors and topics (useful when the
// description is short), then the description, which carries plot, mood and themes.
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
		s.WriteString("\n" + truncate(d, maxDescriptionChars))
	}
	return Doc{Title: title, Text: strings.TrimSpace(s.String())}
}

func topics(genres, subjects []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range append(append([]string{}, genres...), subjects...) {
		t = strings.TrimSpace(t)
		k := strings.ToLower(t)
		// Generic labels carry no signal in an all-fiction catalogue.
		if k == "" || k == "fiction" || k == "fiction, general" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, t)
		if len(out) == maxTopics {
			break
		}
	}
	return out
}

// truncate cuts s to at most max bytes, preferring a word boundary.
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

// ContentHash identifies the model and text an embedding was computed from, so unchanged works
// are not re-embedded after a reload.
func ContentHash(model string, d Doc) string {
	h := sha256.Sum256([]byte(model + "\x00" + d.Title + "\x00" + d.Text))
	return hex.EncodeToString(h[:16])
}
