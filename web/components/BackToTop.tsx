"use client";

import { useEffect, useState } from "react";

// Back-to-top for long pages (search results, admin overview,
// settings). Appears after roughly one viewport of scrolling and
// smooth-scrolls back. Fixed bottom-right; the caller picks the
// bottom offset — front pages must clear the mobile bottom nav,
// admin pages sit closer to the corner.
export default function BackToTop({
  bottomClass = "bottom-[calc(4.5rem+env(safe-area-inset-bottom))] lg:bottom-6",
}: {
  bottomClass?: string;
}) {
  const [visible, setVisible] = useState(false);

  useEffect(() => {
    let ticking = false;
    function onScroll() {
      if (ticking) return;
      ticking = true;
      requestAnimationFrame(() => {
        setVisible(window.scrollY > window.innerHeight);
        ticking = false;
      });
    }
    onScroll();
    window.addEventListener("scroll", onScroll, { passive: true });
    return () => window.removeEventListener("scroll", onScroll);
  }, []);

  if (!visible) return null;

  return (
    <button
      type="button"
      onClick={() => window.scrollTo({ top: 0, behavior: "smooth" })}
      aria-label="回到顶部"
      title="回到顶部"
      className={`fixed right-4 z-40 flex h-11 w-11 items-center justify-center rounded-full border border-input bg-surface text-ink-2 shadow-bar transition-colors hover:border-accent hover:text-accent-ink lg:right-6 ${bottomClass}`}
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
  );
}
