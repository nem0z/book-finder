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
  url: string;
};

type SearchResponse = { query: string; results: Book[] };
type ErrorResponse = { error: string };

export async function searchBooks(query: string, signal?: AbortSignal): Promise<Book[]> {
  const res = await fetch(`/search?q=${encodeURIComponent(query)}`, { signal });
  if (!res.ok) {
    const body = (await res.json().catch(() => null)) as ErrorResponse | null;
    throw new Error(body?.error ?? `search failed (${res.status})`);
  }

  return ((await res.json()) as SearchResponse).results;
}
