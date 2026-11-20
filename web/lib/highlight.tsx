import type { ReactNode } from "react";

// Keyword highlighting for result names. The backend matches a query
// as whitespace-separated keywords, each a case-insensitive substring
// of the name / alias (store.keywordWhere: strings.Fields + LIKE), so
// the highlight uses exactly that tokenisation: every occurrence of
// every keyword is marked. Longer keywords come first in the pattern
// so an overlapping longer match wins over a shorter one.
//
// The mark is deliberately quiet — the theme's soft accent wash plus
// semibold, no saturated yellow — and it is a plain inline element, so
// it composes with the card's wrap-anywhere / line-clamp name layout
// without changing line breaks.
export function highlightText(text: string, query: string): ReactNode {
  if (!text) return text;
  const keywords = Array.from(
    new Set(
      query
        .split(/\s+/)
        .map((k) => k.trim())
        .filter(Boolean)
    )
  ).sort((a, b) => b.length - a.length);
  if (keywords.length === 0) return text;
  const pattern = keywords
    .map((k) => k.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"))
    .join("|");
  const parts = text.split(new RegExp(`(${pattern})`, "gi"));
  if (parts.length === 1) return text;
  return parts.map((part, i) =>
    i % 2 === 1 ? (
      <mark key={i} className="rounded-sm bg-accent-soft font-semibold text-inherit">
        {part}
      </mark>
    ) : (
      part
    )
  );
}
