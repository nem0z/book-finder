import type { Filters, Interpretation } from "./api";

// Shows what the search understood of the query: the keywords it searched for and the filters it applied.
export default function Understood({ interpretation }: { interpretation: Interpretation }) {
  const filters = filterLabels(interpretation.filters);

  return (
    <section className="understood" aria-label="How the search read your query">
      <p>
        <span className="label">Searched for</span> {interpretation.keywords}
      </p>
      {filters.length > 0 && (
        <ul className="filters">
          {filters.map((f) => (
            <li key={f}>{f}</li>
          ))}
        </ul>
      )}
    </section>
  );
}

export function filterLabels(f: Filters): string[] {
  const labels: string[] = [];
  if (f.min_year && f.max_year) {
    labels.push(f.min_year === f.max_year ? `Published in ${f.min_year}` : `Published ${f.min_year}–${f.max_year}`);
  } else if (f.min_year) labels.push(`Published ${f.min_year} or later`);
  else if (f.max_year) labels.push(`Published ${f.max_year} or earlier`);

  if (f.min_pages && f.max_pages) labels.push(`${f.min_pages}–${f.max_pages} pages`);
  else if (f.min_pages) labels.push(`At least ${f.min_pages} pages`);
  else if (f.max_pages) labels.push(`Up to ${f.max_pages} pages`);

  if (f.original_language) labels.push(`Original language: ${f.original_language}`);
  if (f.in_shop) labels.push("In a Dublin bookshop");

  return labels;
}
