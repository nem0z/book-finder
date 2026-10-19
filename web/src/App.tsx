import { useRef, useState, type FormEvent } from "react";
import { searchBooks, type Book } from "./api";
import BookCard from "./BookCard";

type State =
  | { status: "idle" }
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "done"; books: Book[] };

export default function App() {
  const [query, setQuery] = useState("");
  const [state, setState] = useState<State>({ status: "idle" });
  const pending = useRef<AbortController | null>(null);

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    const q = query.trim();
    if (!q) return;

    // A new search replaces the one in flight.
    pending.current?.abort();
    const ctrl = new AbortController();
    pending.current = ctrl;
    setState({ status: "loading" });

    try {
      setState({ status: "done", books: await searchBooks(q, ctrl.signal) });
    } catch (err) {
      if (ctrl.signal.aborted) return;
      setState({ status: "error", message: err instanceof Error ? err.message : String(err) });
    }
  }

  return (
    <main>
      <h1>Book Finder</h1>
      <form className="search" onSubmit={onSubmit}>
        <textarea
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && !e.shiftKey) onSubmit(e);
          }}
          placeholder="Describe the book you're looking for: story, themes, mood, writing style…"
          rows={3}
          maxLength={1000}
        />
        <button type="submit" disabled={!query.trim() || state.status === "loading"}>
          {state.status === "loading" ? "Searching…" : "Search"}
        </button>
      </form>

      {state.status === "error" && <p className="error">{state.message}</p>}
      {state.status === "done" && state.books.length === 0 && <p className="empty">No books found.</p>}
      {state.status === "done" && (
        <ol className="results">
          {state.books.map((b) => (
            <li key={b.id}>
              <BookCard book={b} />
            </li>
          ))}
        </ol>
      )}
    </main>
  );
}
