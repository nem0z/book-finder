import { useState } from "react";
import type { Book } from "./api";

// Descriptions longer than this are cut, with a "See more" button.
const PREVIEW_CHARS = 400;

// Display names of the bookshops in works.source.
const SHOPS: Record<string, string> = { chapters: "Chapters Bookstore" };

export default function BookCard({ book }: { book: Book }) {
  const [expanded, setExpanded] = useState(false);
  const long = book.description.length > PREVIEW_CHARS;
  const text = long && !expanded ? preview(book.description) : book.description;

  return (
    <article className="book">
      {book.cover_url ? (
        <img className="cover" src={book.cover_url} alt={`Cover of ${book.title}`} loading="lazy" />
      ) : (
        <div className="cover placeholder" aria-hidden="true" />
      )}

      <div className="details">
        <h2>
          <a href={book.url} target="_blank" rel="noreferrer">
            {book.title}
          </a>
        </h2>
        <p className="meta">
          {[book.authors.join(", "), book.year, book.pages && `${book.pages} pages`].filter(Boolean).join(" · ")}
        </p>
        {SHOPS[book.source] && (
          <p className="shop">
            At{" "}
            <a href={book.url} target="_blank" rel="noreferrer">
              {SHOPS[book.source]}
            </a>
            , Dublin
          </p>
        )}

        <p className="description">{text}</p>
        {long && (
          <button className="more" onClick={() => setExpanded(!expanded)}>
            {expanded ? "See less" : "See more"}
          </button>
        )}

        {book.tags.length > 0 && (
          <ul className="tags">
            {book.tags.map((t) => (
              <li key={t}>{t}</li>
            ))}
          </ul>
        )}
      </div>
    </article>
  );
}

// preview cuts text at a word boundary before PREVIEW_CHARS.
function preview(text: string): string {
  const cut = text.lastIndexOf(" ", PREVIEW_CHARS);
  return text.slice(0, cut > 0 ? cut : PREVIEW_CHARS) + "…";
}
