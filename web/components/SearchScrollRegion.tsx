"use client";

import { useEffect, useRef, useState, type ReactNode } from "react";

// The search page's results scroller — the only thing that scrolls
// on /search. The page is an app shell (see the body:has rule in
// globals.css): the header, the notice bar, the filter/sort controls
// and the category chips are pinned, and results, pagination and the
// page's footer line live in here. Mirrors the admin shell's shape
// (a 100dvh column with an internal overflow-y-auto region).
export default function SearchScrollRegion({
  resetKey,
  children,
}: {
  /** Identity of the current result set (query, page, category, sort,
   *  dead filter). A new result set starts at the top of the list —
   *  never at the previous set's scroll offset. Document navigations
   *  (search submit, sort, pagination) load a fresh document already
   *  at the top; this covers the soft navigations (category chips,
   *  empty-state links), where the region's DOM node survives the
   *  re-render and would otherwise keep its stale offset. */
  resetKey: string;
  children: ReactNode;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const [showTop, setShowTop] = useState(false);

  useEffect(() => {
    ref.current?.scrollTo({ top: 0 });
  }, [resetKey]);

  // Back-to-top for the list. The window itself never scrolls on
  // this page, so BackToTop (which watches window.scrollY) could
  // never fire here; this is the same affordance bound to the
  // region instead — same visuals, same offsets (clearing the
  // mobile bottom nav), appearing after roughly one region-height
  // of scrolling, rAF-throttled like the original.
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    let ticking = false;
    function onScroll() {
      if (ticking) return;
      ticking = true;
      requestAnimationFrame(() => {
        const node = ref.current;
        if (node) setShowTop(node.scrollTop > node.clientHeight);
        ticking = false;
      });
    }
    onScroll();
    el.addEventListener("scroll", onScroll, { passive: true });
    return () => el.removeEventListener("scroll", onScroll);
  }, []);

  return (
    <>
      <div
        ref={ref}
        data-search-scroll-region
        className="scroll-thin min-h-0 flex-1 overflow-y-auto overscroll-contain pb-6"
      >
        {/* min-h-full flex column so the page footer line keeps its
            mt-auto behaviour: resting at the region's bottom when
            the results are short, trailing the pagination when
            they are long. */}
        <div className="flex min-h-full flex-col">{children}</div>
      </div>
      {showTop && (
        <button
          type="button"
          onClick={() =>
            ref.current?.scrollTo({ top: 0, behavior: "smooth" })
          }
          aria-label="回到顶部"
          title="回到顶部"
          className="fixed right-4 bottom-[calc(4.5rem+env(safe-area-inset-bottom))] z-40 flex h-11 w-11 items-center justify-center rounded-full border border-input bg-surface text-ink-2 shadow-bar transition-colors hover:border-accent hover:text-accent-ink lg:right-6 lg:bottom-6"
        >
          <svg
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            strokeWidth="2"
            strokeLinecap="round"
            strokeLinejoin="round"
            className="h-5 w-5"
            aria-hidden
          >
            <path d="M12 19V5" />
            <path d="m5 12 7-7 7 7" />
          </svg>
        </button>
      )}
    </>
  );
}
