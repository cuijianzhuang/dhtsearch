"use client";

import { FormEvent, useState } from "react";

interface SearchBoxProps {
  initialQuery?: string;
  large?: boolean;
}

export default function SearchBox({ initialQuery = "", large = false }: SearchBoxProps) {
  const [q, setQ] = useState(initialQuery);

  function onSubmit(e: FormEvent) {
    e.preventDefault();
    const query = q.trim();
    // Cloudflare may return an interstitial Managed Challenge for /search.
    // A client-side router fetch cannot display that HTML challenge, so make
    // search a document navigation and let Cloudflare complete its normal
    // challenge -> cf_clearance -> page flow.
    window.location.assign(
      query ? `/search?q=${encodeURIComponent(query)}` : "/search",
    );
  }

  return (
    <form onSubmit={onSubmit} className="w-full">
      <div className="flex w-full items-center gap-2">
        <input
          type="text"
          value={q}
          onChange={(e) => setQ(e.target.value)}
          placeholder="搜索磁力链接 / 资源名称…"
          autoFocus={large}
          // min-w-0 lets the input shrink past its intrinsic size; without it
          // the row overflows narrow viewports and pushes the submit button
          // off-screen.
          className={`min-w-0 flex-1 rounded-lg border border-input bg-surface text-ink placeholder-faint outline-none focus:border-accent focus:ring-4 focus:ring-accent/15 ${
            large ? "h-12 px-4 text-lg" : "h-11 px-4 text-sm"
          }`}
        />
        <button
          type="submit"
          className={`inline-flex shrink-0 items-center justify-center rounded-lg bg-accent font-medium text-on-accent transition-colors hover:bg-accent-hover ${
            large ? "h-12 px-6 text-lg" : "h-11 px-4 text-sm"
          }`}
        >
          搜索
        </button>
      </div>
    </form>
  );
}
