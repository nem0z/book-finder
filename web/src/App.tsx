import { useRef, useState, type FormEvent } from "react";
import { searchBooks, type Book, type Interpretation } from "./api";
import BookCard from "./BookCard";
import Understood from "./Understood";

type State =
  | { status: "idle" }
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "done"; books: Book[]; interpretation?: Interpretation };

export default function App() {
  const [query, setQuery] = useState("");
  const [inShop, setInShop] = useState(false);
  const [state, setState] = useState<State>({ status: "idle" });
  const pending = useRef<AbortController | null>(null);

  function onSubmit(e: FormEvent) {
    e.preventDefault();
    search(inShop);
  }

  // Changing where to search re-runs the current search.
  function onShopChange(checked: boolean) {
    setInShop(checked);
    if (state.status !== "idle") search(checked);
  }

  async function search(shop: boolean) {
    const q = query.trim();
    if (!q) return;

    // A new search replaces the one in flight.
    pending.current?.abort();
    const ctrl = new AbortController();
    pending.current = ctrl;
    setState({ status: "loading" });

    try {
      setState({ status: "done", ...(await searchBooks(q, shop, ctrl.signal)) });
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
      <label className="shop-toggle">
        <input type="checkbox" checked={inShop} onChange={(e) => onShopChange(e.target.checked)} />
        In a Dublin bookshop
      </label>

      {state.status === "error" && <p className="error">{state.message}</p>}
      {state.status === "done" && state.interpretation && <Understood interpretation={state.interpretation} />}
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
