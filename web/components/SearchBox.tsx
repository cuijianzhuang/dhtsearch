"use client";

import { FormEvent, useEffect, useRef, useState } from "react";

interface SearchBoxProps {
  initialQuery?: string;
  large?: boolean;
}

// Recent searches live only in this browser (localStorage), most
// recent first, deduplicated, capped at 10. They are a convenience
// layer over the plain document navigation below — nothing is sent
// anywhere until the visitor actually searches.
const HISTORY_KEY = "dhtsearch-search-history";
const HISTORY_MAX = 10;

function loadHistory(): string[] {
  try {
    const raw = localStorage.getItem(HISTORY_KEY);
    if (!raw) return [];
    const parsed = JSON.parse(raw) as unknown;
    if (!Array.isArray(parsed)) return [];
    return parsed.filter((x): x is string => typeof x === "string").slice(0, HISTORY_MAX);
  } catch {
    return [];
  }
}

function saveHistory(list: string[]) {
  try {
    localStorage.setItem(HISTORY_KEY, JSON.stringify(list.slice(0, HISTORY_MAX)));
  } catch {
    // Storage full/blocked — history is best-effort.
  }
}

function rememberQuery(query: string) {
  if (!query) return;
  const next = [query, ...loadHistory().filter((h) => h !== query)];
  saveHistory(next);
}

export default function SearchBox({ initialQuery = "", large = false }: SearchBoxProps) {
  const [q, setQ] = useState(initialQuery);
  const [history, setHistory] = useState<string[]>([]);
  const [open, setOpen] = useState(false);
  const inputRef = useRef<HTMLInputElement>(null);
  const boxRef = useRef<HTMLDivElement>(null);

  // Refresh the list every time the box gains focus: another tab (or
  // the search page's own box) may have recorded searches since.
  function refreshHistory() {
    setHistory(loadHistory());
  }

  // Close the dropdown on any pointer press outside the box.
  useEffect(() => {
    if (!open) return;
    function onDown(e: PointerEvent) {
      if (boxRef.current && !boxRef.current.contains(e.target as Node)) {
        setOpen(false);
      }
    }
    document.addEventListener("pointerdown", onDown);
    return () => document.removeEventListener("pointerdown", onDown);
  }, [open ]);

  function go(query: string) {
    rememberQuery(query);
    // Cloudflare may return an interstitial Managed Challenge for /search.
    // A client-side router fetch cannot display that HTML challenge, so make
    // search a document navigation and let Cloudflare complete its normal
    // challenge -> cf_clearance -> page flow.
    window.location.assign(
      query ? `/search?q=${encodeURIComponent(query)}` : "/search",
    );
  }

  function onSubmit(e: FormEvent) {
    e.preventDefault();
    go(q.trim());
  }

  function removeOne(item: string) {
    const next = history.filter((h) => h !== item);
    setHistory(next);
    saveHistory(next);
    inputRef.current?.focus();
  }

  function clearAll() {
    setHistory([]);
    saveHistory([]);
    setOpen(false);
    inputRef.current?.focus();
  }

  const trimmed = q.trim();
  const suggestions =
    open && history.length > 0
      ? history.filter(
          (h) => trimmed === "" || h.toLowerCase().includes(trimmed.toLowerCase())
        )
      : [];
  const showDropdown = open && suggestions.length > 0;

  return (
    <div ref={boxRef} className="relative w-full">
      <form onSubmit={onSubmit} className="w-full">
        <div className="flex w-full items-center gap-2">
          <input
            ref={inputRef}
            type="text"
            value={q}
            onChange={(e) => {
              setQ(e.target.value);
              setOpen(true);
            }}
            onFocus={() => {
              refreshHistory();
              setOpen(true);
            }}
            onKeyDown={(e) => {
              if (e.key === "Escape") setOpen(false);
            }}
            placeholder="搜索磁力链接 / 资源名称…"
            autoFocus={large}
            autoComplete="off"
            role="combobox"
            aria-expanded={showDropdown}
            aria-controls="search-history-listbox"
            aria-label="搜索"
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

      {showDropdown && (
        <div className="absolute inset-x-0 top-full z-30 mt-2 overflow-hidden rounded-xl border border-line bg-surface shadow-bar">
          <ul id="search-history-listbox" role="listbox" aria-label="最近搜索">
            {suggestions.map((item) => (
              <li key={item} className="flex items-center">
                <button
                  type="button"
                  onClick={() => go(item)}
                  className="flex min-h-11 min-w-0 flex-1 items-center gap-2 px-3 text-left text-sm text-ink-2 transition-colors hover:bg-soft hover:text-accent-ink"
                >
                  <svg
                    viewBox="0 0 24 24"
                    fill="none"
                    stroke="currentColor"
                    strokeWidth="2"
                    strokeLinecap="round"
                    strokeLinejoin="round"
                    className="h-4 w-4 shrink-0 text-faint"
                    aria-hidden
                  >
                    <circle cx="12" cy="12" r="9" />
                    <path d="M12 7v5l3 2" />
                  </svg>
                  <span className="min-w-0 flex-1 truncate">{item}</span>
                </button>
                <button
                  type="button"
                  onClick={() => removeOne(item)}
                  aria-label={`删除搜索历史「${item}」`}
                  title="删除这条"
                  className="flex h-11 w-11 shrink-0 items-center justify-center text-faint transition-colors hover:text-ink"
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
              </li>
            ))}
          </ul>
          <div className="flex items-center justify-between border-t border-line-2 px-3 py-1">
            <span className="text-xs text-faint">最近搜索（仅保存在本机）</span>
            <button
              type="button"
              onClick={clearAll}
              className="inline-flex min-h-9 items-center px-2 text-xs text-mute transition-colors hover:text-accent-ink"
            >
              清空历史
            </button>
          </div>
        </div>
      )}
    </div>
  );
}
