"use client";

// Mobile/tablet settings tab strip, shaped after Polaris's settings
// nav (web/src/pages/Settings.tsx + .snav in its styles.css): a
// horizontally scrollable row of icon + label pills that bleeds to
// the screen edges instead of sitting inside a card, the active tab
// is a filled soft-teal pill, and the row scrolls the active pill to
// center whenever the group changes. Two adaptations from Polaris:
// the tabs stay real route Links (Polaris switches hash state
// client-side; dhtsearch groups are separate pages), and the desktop
// rail in SettingsNav.tsx is untouched.
//
// This replaces a sticky strip rendered as a bordered overflow-x-auto
// card, which failed on touch devices in two compounding ways:
//   1. Reaching most groups meant swipe-then-tap, and the first tap
//      after a swipe is consumed stopping the strip's momentum
//      scroll — the tap never becomes a click (reproduced with
//      trusted touch events: fling, then tap -> zero clicks). The
//      36px targets made drift-eaten taps likelier still.
//   2. Every page rendered the strip at scrollLeft 0 with no
//      scroll-into-view, so for any group past the first screenful
//      the active pill was off-screen — and for auth it was clipped
//      mid-glyph by the card's right border. After a successful tap
//      the strip looked like it had jumped back to the first tab.
// The row is also no longer sticky: at top-2 it slid underneath the
// shell's sticky top bar (z-30) and was covered by it when scrolled.
// Polaris's mobile row is static too.

import Link from "next/link";
import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { settingsGroupHref } from "@/lib/settings-groups";
import type { SettingsNavEntry } from "./SettingsNav";

// Layout effect on the client (scroll before paint, so the row never
// flashes its unscrolled state), plain effect during SSR.
const useIsoLayoutEffect =
  typeof window !== "undefined" ? useLayoutEffect : useEffect;

/* Group glyphs: 24px stroke icons in the same idiom as the admin
 * shell's nav icons. The settings groups had no iconography of their
 * own (the index cards are text-only), so these are drawn for the
 * strip; unknown future groups fall back to the gear. */
const GROUP_ICON_PATHS: Record<string, React.ReactNode> = {
  __overview: <path d="m15 18-6-6 6-6" />,
  service: (
    <>
      <ellipse cx="12" cy="5" rx="9" ry="3" />
      <path d="M3 5v14a9 3 0 0 0 18 0V5" />
      <path d="M3 12a9 3 0 0 0 18 0" />
    </>
  ),
  update: (
    <>
      <path d="M21 12a9 9 0 1 1-2.64-6.36" />
      <path d="M21 3v6h-6" />
    </>
  ),
  auth: (
    <>
      <circle cx="8" cy="15" r="4.5" />
      <path d="m11.2 11.8 8.3-8.3" />
      <path d="M17 5l3 3" />
      <path d="M14 8l2.5 2.5" />
    </>
  ),
  appearance: (
    <>
      <circle cx="12" cy="12" r="9" />
      <path d="M12 3a9 9 0 0 1 0 18z" fill="currentColor" stroke="none" />
    </>
  ),
  crawler: (
    <>
      <ellipse cx="12" cy="13" rx="5" ry="6" />
      <path d="M12 7V5" />
      <path d="M7 11 3.5 9M7 15l-4 .5M7.5 18.5 4.5 21M17 11l3.5-2M17 15l4 .5M16.5 18.5 19.5 21" />
    </>
  ),
  metadata: (
    <>
      <path d="M12 3v10" />
      <path d="m8 9 4 4 4-4" />
      <path d="M4 17v2a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-2" />
    </>
  ),
  scrape: (
    <>
      <path d="M3 6h13" />
      <path d="M3 12h13" />
      <path d="M3 18h13" />
      <path d="m19 14v7" />
      <path d="m16 18 3 3 3-3" />
    </>
  ),
  filter: <path d="M4 5h16l-6.2 7.2V19l-3.6 2v-8.8L4 5z" />,
  moderation: (
    <>
      <path d="M12 3l7 2.8V11c0 4.6-3 7.9-7 9.2C8 18.9 5 15.6 5 11V5.8L12 3z" />
      <path d="m9.3 11.8 2 2 3.6-3.8" />
    </>
  ),
  trending: (
    <>
      <path d="m3 17 6-6 4 4 7-7" />
      <path d="M14 8h6v6" />
    </>
  ),
  feeds: (
    <>
      <path d="M4 11a9 9 0 0 1 9 9" />
      <path d="M4 4a16 16 0 0 1 16 16" />
      <circle cx="5" cy="19" r="1.2" fill="currentColor" stroke="none" />
    </>
  ),
  ratelimit: (
    <>
      <path d="M4.5 19a9 9 0 1 1 15 0" />
      <path d="m12 14 4.5-4.5" />
      <circle cx="12" cy="14" r="1.2" fill="currentColor" stroke="none" />
    </>
  ),
  publicapi: (
    <>
      <path d="m8 7-5 5 5 5" />
      <path d="m16 7 5 5-5 5" />
    </>
  ),
  __fallback: (
    <>
      <path d="M4 6h8M16 6h4M4 12h3M11 12h9M4 18h10M18 18h2" />
      <circle cx="14" cy="6" r="2" />
      <circle cx="9" cy="12" r="2" />
      <circle cx="16" cy="18" r="2" />
    </>
  ),
};

function GroupIcon({ groupKey }: { groupKey: string }) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="h-[18px] w-[18px] shrink-0"
      aria-hidden
    >
      {GROUP_ICON_PATHS[groupKey] ?? GROUP_ICON_PATHS.__fallback}
    </svg>
  );
}

function pillClass(active: boolean) {
  return `inline-flex min-h-11 shrink-0 items-center gap-1.5 rounded-full px-3.5 text-sm whitespace-nowrap transition-colors ${
    active
      ? "bg-accent-soft font-medium text-accent-ink"
      : "text-mute hover:bg-soft hover:text-accent-ink active:bg-soft"
  }`;
}

export default function SettingsNavChips({
  groups,
  active,
}: {
  groups: SettingsNavEntry[];
  active: string;
}) {
  const navRef = useRef<HTMLElement>(null);
  const [more, setMore] = useState(false);

  // Polaris's overflow affordance: a right-edge fade while more
  // pills are scrolled out of view on that side.
  const updateMore = useCallback(() => {
    const nav = navRef.current;
    setMore(
      !!nav && nav.scrollLeft + nav.clientWidth < nav.scrollWidth - 2
    );
  }, []);

  // Center the active pill whenever the group changes (Polaris's
  // scroll-on-tab-change effect), before paint so the row never
  // flashes its scrolled-to-zero state.
  useIsoLayoutEffect(() => {
    const nav = navRef.current;
    const el = nav?.querySelector<HTMLElement>('[aria-current="page"]');
    if (nav && el && nav.scrollWidth > nav.clientWidth) {
      nav.scrollTo({
        left:
          nav.scrollLeft +
          el.getBoundingClientRect().left -
          nav.getBoundingClientRect().left -
          (nav.clientWidth - el.clientWidth) / 2,
      });
    }
    updateMore();
  }, [active, updateMore]);

  useEffect(() => {
    const nav = navRef.current;
    if (!nav) return;
    const ro = new ResizeObserver(updateMore);
    ro.observe(nav);
    updateMore();
    return () => ro.disconnect();
  }, [updateMore]);

  return (
    <div className="relative -mx-4 sm:-mx-6 lg:hidden">
      <nav
        ref={navRef}
        aria-label="设置分组"
        onScroll={updateMore}
        className="flex gap-1.5 overflow-x-auto px-4 py-1 [scrollbar-width:none] sm:px-6 [&::-webkit-scrollbar]:hidden"
      >
        <Link href="/admin/settings" className={pillClass(false)}>
          <GroupIcon groupKey="__overview" />
          总览
        </Link>
        {groups.map((g) => (
          <Link
            key={g.key}
            href={settingsGroupHref(g.key)}
            aria-current={active === g.key ? "page" : undefined}
            className={pillClass(active === g.key)}
          >
            <GroupIcon groupKey={g.key} />
            {g.label}
          </Link>
        ))}
      </nav>
      {more && (
        <span
          aria-hidden
          className="pointer-events-none absolute inset-y-0 right-0 w-12 bg-gradient-to-l from-canvas via-canvas/70 to-transparent"
        />
      )}
    </div>
  );
}
