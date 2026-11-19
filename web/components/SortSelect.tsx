"use client";

import { SORTS, type SearchSort } from "@/lib/search-ui";

/** Sort dropdown for the search page. Changing it navigates (document
 *  navigation, like SearchBox — a router fetch cannot display a
 *  Cloudflare challenge) to the same search with the new sort and the
 *  page reset. */
export default function SortSelect({
  q,
  category,
  sort,
}: {
  q: string;
  category: string;
  sort: SearchSort;
}) {
  function onChange(e: React.ChangeEvent<HTMLSelectElement>) {
    const next = e.target.value as SearchSort;
    const params = new URLSearchParams();
    if (q) params.set("q", q);
    if (category) params.set("category", category);
    if (next !== "relevance") params.set("sort", next);
    const qs = params.toString();
    window.location.assign(qs ? `/search?${qs}` : "/search");
  }

  return (
    <label className="inline-flex items-center gap-2 text-sm text-mute">
      <span className="hidden sm:inline">排序</span>
      <select
        value={sort}
        onChange={onChange}
        aria-label="结果排序"
        className="min-h-11 rounded-lg border border-input bg-surface px-3 text-sm text-ink-2 outline-none focus:border-accent"
      >
        {SORTS.map((s) => (
          <option key={s.key} value={s.key}>
            {s.label}
          </option>
        ))}
      </select>
    </label>
  );
}
