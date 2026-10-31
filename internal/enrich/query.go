package enrich

// QueryPrompt asks for a search query's keywords in the shape SystemPrompt gives a book's, so that both
// can be compared: the keywords of the book the reader is looking for. The answer follows Schema.
const QueryPrompt = `You index searches for a keyword search engine over novels. A reader describes the book they want; return the keywords the index of a matching book would have:
- keywords: 15 to 30 short lowercase terms: places, period, themes, tropes, genre and subgenre, mood and tone, type of protagonist, notable objects, creatures, jobs and events. Add common synonyms and other word forms ("holland", "netherlands", "dutch"; "insect", "bug", "vermin"). No words about the reader or the search itself ("looking for", "recommend", "book").
- setting_places: up to 3 places where the story happens, most specific first, as "City, Country" when known.
- era_from, era_to: the first and last years in which the story takes place (negative for BC), 0 when unknown.
- era_label: the period in a few words ("2020s", "Victorian era", "Middle Ages", "far future", "invented world"), empty when unknown.
- themes: up to 6 themes, a few words each.
- protagonist: the main character's type in a few words, empty when unclear.
Use only what the search says or clearly implies: leave a field empty rather than guess, and do not name a specific book.`
