"use client";

import type { SearchSort } from "@/lib/search-ui";

/** "隐藏疑似死种" toggle for the search page. Like SortSelect it
 *  navigates (document navigation, so a Cloudflare challenge can
 *  complete) to the same search with hide_dead flipped and the page
 *  reset. Only torrents whose scrape history derives to
 *  likely_dead are hidden — never-scraped results stay, because
 *  unknown is not dead. */
export default function HideDeadToggle({
  q,
  category,
  sort,
  hideDead,
}: {
  q: string;
  category: string;
  sort: SearchSort;
  hideDead: boolean;
}) {
  function onChange(e: React.ChangeEvent<HTMLInputElement>) {
    const params = new URLSearchParams();
    if (q) params.set("q", q);
    if (category) params.set("category", category);
    if (sort !== "relevance") params.set("sort", sort);
    if (e.target.checked) params.set("hide_dead", "1");
    const qs = params.toString();
    window.location.assign(qs ? `/search?${qs}` : "/search");
  }

  return (
    <label className="inline-flex min-h-11 cursor-pointer items-center gap-2 text-sm text-mute select-none">
      <input
        type="checkbox"
        checked={hideDead}
        onChange={onChange}
        className="h-4 w-4 cursor-pointer accent-accent"
      />
      隐藏疑似死种
    </label>
  );
}
