"use client";

import { useState } from "react";
import type { SearchResult } from "@/lib/api";
import { copyText } from "@/lib/clipboard";
import ResultCard from "./ResultCard";

export default function ResultList({
  results,
  trackers = [],
  query = "",
}: {
  results: SearchResult[];
  trackers?: string[];
  /** The search query, forwarded to cards for keyword highlighting. */
  query?: string;
}) {
  const [selected, setSelected] = useState<ReadonlySet<string>>(new Set());
  const [copied, setCopied] = useState(false);

  // Results can repeat an info_hash across pages but not within one, so the
  // hash is a safe selection key for this list.
  function toggle(infoHash: string) {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(infoHash)) {
        next.delete(infoHash);
      } else {
        next.add(infoHash);
      }
      return next;
    });
    setCopied(false);
  }

  const allSelected = selected.size === results.length;

  function toggleAll() {
    setSelected(allSelected ? new Set() : new Set(results.map((r) => r.info_hash)));
    setCopied(false);
  }

  function clearSelection() {
    setSelected(new Set());
    setCopied(false);
  }

  async function copySelected() {
    const magnets = results
      .filter((r) => selected.has(r.info_hash))
      .map((r) => r.magnet);
    if (magnets.length === 0) return;
    await copyText(magnets.join("\n"));
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  }

  return (
    <>
      <div className="mt-3 space-y-3">
        {results.map((r) => (
          <ResultCard
            key={r.info_hash}
            result={r}
            trackers={trackers}
            selected={selected.has(r.info_hash)}
            onToggleSelect={() => toggle(r.info_hash)}
            query={query}
          />
        ))}
      </div>

      {selected.size > 0 && (
        // Sticky, not fixed: the bar rides the viewport bottom while the list
        // scrolls, but settles into the flow at the end of the list, so it can
        // never cover the last card, the pagination, or the footer.
        <div className="sticky bottom-4 z-10 mt-3">
          {/* Solid surface, no backdrop-blur: a blurred sticky bar
              forces the browser to re-sample and blur the content
              scrolling under it every frame — a classic mobile scroll
              jank source — for a 5% translucency nobody can see. */}
          <div className="flex w-full items-center justify-between gap-2 rounded-xl border border-input bg-surface px-4 py-3 shadow-bar">
            <span className="shrink-0 text-sm text-ink-2">
              已选 <span className="font-medium text-accent-ink">{selected.size}</span> 条
            </span>
            <div className="flex items-center gap-2">
              {/* Phones get shorter labels and taller (easier-to-tap) buttons. */}
              <button
                onClick={toggleAll}
                className="inline-flex min-h-11 items-center rounded-lg border border-input bg-soft px-3 text-xs font-medium text-ink-2 transition-colors hover:border-accent hover:text-accent-ink"
              >
                <span className="sm:hidden">{allSelected ? "取消" : "全选"}</span>
                <span className="hidden sm:inline">
                  {allSelected ? "取消全选" : "全选本页"}
                </span>
              </button>
              <button
                onClick={copySelected}
                className={`inline-flex min-h-11 items-center rounded-lg border px-3 text-xs font-medium transition-colors ${
                  copied
                    ? "border-accent bg-accent-soft text-accent-ink"
                    : "border-accent bg-accent-soft text-accent-ink hover:bg-accent-soft"
                }`}
              >
                {copied ? (
                  "✓ 已复制"
                ) : (
                  <>
                    <span className="sm:hidden">复制链接</span>
                    <span className="hidden sm:inline">复制磁力链接</span>
                  </>
                )}
              </button>
              <button
                onClick={clearSelection}
                title="取消选择"
                aria-label="取消选择"
                className="flex h-11 w-11 shrink-0 items-center justify-center rounded-lg border border-input bg-soft text-mute transition-colors hover:border-faint hover:text-ink"
              >
                <svg
                  viewBox="0 0 24 24"
                  fill="none"
                  stroke="currentColor"
                  strokeWidth="2"
                  strokeLinecap="round"
                  className="h-4 w-4"
                  aria-hidden
                >
                  <path d="M18 6 6 18M6 6l12 12" />
                </svg>
              </button>
            </div>
          </div>
        </div>
      )}
    </>
  );
}
