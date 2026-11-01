// Types and client for the Go API (internal/api).

export type Book = {
  id: string;
  title: string;
  authors: string[];
  year?: number;
  pages?: number;
  description: string;
  tags: string[];
  cover_url?: string;
  url: string; // the bookshop's product page, else the Open Library page
  source: string; // "openlibrary" or a bookshop ("chapters")
};

export type Filters = {
  min_year?: number;
  max_year?: number;
  min_pages?: number;
  max_pages?: number;
  original_language?: string;
  in_shop?: boolean;
};

// What an LLM understood of the query: the keywords searched for instead, and the filters applied.
export type Interpretation = { keywords: string; filters: Filters };

export type SearchResult = { books: Book[]; interpretation?: Interpretation };

type SearchResponse = { query: string; interpretation?: Interpretation; results: Book[] };
type ErrorResponse = { error: string };

// searchBooks searches the whole catalogue, or only the books a Dublin bookshop lists when inShop is set.
export async function searchBooks(query: string, inShop: boolean, signal?: AbortSignal): Promise<SearchResult> {
  const res = await fetch(`/search?q=${encodeURIComponent(query)}${inShop ? "&shop=1" : ""}`, { signal });
  if (!res.ok) {
    const body = (await res.json().catch(() => null)) as ErrorResponse | null;
    throw new Error(body?.error ?? `search failed (${res.status})`);
  }

  const body = (await res.json()) as SearchResponse;
  return { books: body.results, interpretation: body.interpretation };
}
