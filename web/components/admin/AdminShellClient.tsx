"use client";

// Admin chrome layout: content column + RIGHT sidebar.
//
// Desktop (lg+): the sidebar is sticky and can be collapsed to an
// icon rail. The collapsed choice persists in localStorage; a boot
// script in AdminShell copies it onto <html data-admin-nav> before
// first paint, and globals.css keys the rail width / label visibility
// off that attribute (inside the lg media query only), so there is
// no flash and no hydration state to mismatch. The toggle therefore
// needs no React state — it flips the attribute and the store.
//
// Mobile (<lg): the same sidebar becomes a right-side drawer —
// fixed, translated off-canvas until the top bar's menu button opens
// it, with a backdrop, Escape-to-close, body scroll lock, and
// close-on-navigation. The collapsed attribute never applies here,
// so the drawer always shows full labels.

import Link from "next/link";
import { useEffect, useState } from "react";
import AppearanceSwitcher from "@/components/AppearanceSwitcher";
import SiteLogo from "@/components/SiteLogo";
import LogoutButton from "./LogoutButton";
import { ADMIN_NAV_STORAGE_KEY, type Appearance } from "@/lib/theme";

const TABS = [
  { key: "overview", href: "/admin", label: "概览", icon: IconOverview },
  { key: "torrents", href: "/admin/torrents", label: "种子管理", icon: IconTorrents },
  { key: "review", href: "/admin/review", label: "未审核队列", icon: IconReview },
  { key: "blocked", href: "/admin/blocked", label: "黑名单", icon: IconBlocked },
  { key: "llm", href: "/admin/llm", label: "LLM 审核", icon: IconLlm },
  { key: "reports", href: "/admin/reports", label: "举报队列", icon: IconReports },
  { key: "users", href: "/admin/users", label: "账号", icon: IconUsers },
  { key: "settings", href: "/admin/settings", label: "设置", icon: IconSettings },
  { key: "api-tokens", href: "/admin/api-tokens", label: "API 令牌", icon: IconKey },
] as const;

export type AdminTabKey = (typeof TABS)[number]["key"];

function toggleCollapsed() {
  const el = document.documentElement;
  const next =
    el.dataset.adminNav === "collapsed" ? "expanded" : "collapsed";
  el.dataset.adminNav = next;
  try {
    localStorage.setItem(ADMIN_NAV_STORAGE_KEY, next);
  } catch {
    // Storage unavailable — the attribute still applies for this page.
  }
}

export default function AdminShellClient({
  active,
  siteName,
  defaultAppearance,
  children,
}: {
  active: AdminTabKey;
  siteName: string;
  defaultAppearance: Appearance;
  children: React.ReactNode;
}) {
  const [drawerOpen, setDrawerOpen] = useState(false);

  // Close the drawer when navigating: the active tab changes on every
  // navigation, so adjust the state during render (the React-docs
  // pattern for prop-driven resets) — the drawer is already closed by
  // the time the new page paints.
  const [prevActive, setPrevActive] = useState(active);
  if (prevActive !== active) {
    setPrevActive(active);
    setDrawerOpen(false);
  }
  useEffect(() => {
    if (!drawerOpen) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setDrawerOpen(false);
    };
    const mq = window.matchMedia("(min-width: 1024px)");
    const onMq = (e: MediaQueryListEvent) => {
      if (e.matches) setDrawerOpen(false);
    };
    document.addEventListener("keydown", onKey);
    mq.addEventListener("change", onMq);
    document.body.style.overflow = "hidden";
    return () => {
      document.removeEventListener("keydown", onKey);
      mq.removeEventListener("change", onMq);
      document.body.style.overflow = "";
    };
  }, [drawerOpen]);

  return (
    <div className="flex flex-1 flex-col">
      {/* Mobile top bar: brand + appearance + the drawer trigger.
          Desktop has no top bar — the sidebar carries the brand. */}
      <div className="sticky top-0 z-30 flex items-center gap-2 border-b border-line bg-canvas px-4 py-1.5 lg:hidden">
        <Link
          href="/admin"
          className="inline-flex min-h-11 items-center text-lg font-bold tracking-tight"
        >
          <SiteLogo name={siteName} />
          <span className="ml-2 rounded-md bg-accent-soft px-2 py-1 text-xs font-medium text-accent-ink">
            管理端
          </span>
        </Link>
        <div className="ml-auto flex items-center gap-1.5">
          <AppearanceSwitcher defaultAppearance={defaultAppearance} />
          <button
            type="button"
            onClick={() => setDrawerOpen(true)}
            aria-label="打开导航"
            aria-expanded={drawerOpen}
            className="inline-flex min-h-11 min-w-11 items-center justify-center rounded-lg border border-input bg-soft text-ink-2 transition-colors hover:border-accent hover:text-accent-ink"
          >
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" className="h-5 w-5" aria-hidden>
              <path d="M4 6h16M4 12h16M4 18h16" />
            </svg>
          </button>
        </div>
      </div>

      <div className="flex flex-1 flex-row items-stretch">
        {/* Content column. min-w-0 so wide inner content (tables,
            truncating names) can never push the sidebar off or widen
            the page. */}
        <div className="flex min-w-0 flex-1 flex-col px-4 py-6 sm:px-6">
          {children}
          <footer className="mt-auto pt-10 text-center text-xs text-faint">
            {siteName} 管理端 · 数据来自后端 /api/admin
          </footer>
        </div>

        {drawerOpen && (
          <button
            type="button"
            aria-label="关闭导航"
            onClick={() => setDrawerOpen(false)}
            className="fixed inset-0 z-30 bg-black/40 lg:hidden"
          />
        )}

        <aside
          aria-label="管理端导航"
          className={`admin-sidebar fixed inset-y-0 right-0 z-40 flex w-72 max-w-[85vw] flex-col border-l border-line bg-surface transition-transform duration-200 ${
            drawerOpen ? "translate-x-0" : "translate-x-full max-lg:invisible"
          } lg:sticky lg:top-0 lg:z-auto lg:h-dvh lg:max-w-none lg:translate-x-0 lg:transition-[width]`}
        >
          <div className="flex items-center gap-2 px-3 pt-3 pb-1">
            <Link
              href="/admin"
              onClick={() => setDrawerOpen(false)}
              className="admin-nav-text inline-flex min-h-11 items-center text-lg font-bold tracking-tight"
            >
              <SiteLogo name={siteName} />
              <span className="ml-2 rounded-md bg-accent-soft px-2 py-1 text-xs font-medium text-accent-ink">
                管理端
              </span>
            </Link>
            <button
              type="button"
              onClick={toggleCollapsed}
              aria-label="收起或展开侧栏"
              title="收起或展开侧栏"
              className="ml-auto hidden min-h-11 min-w-11 items-center justify-center rounded-lg text-mute transition-colors hover:bg-soft hover:text-accent-ink lg:inline-flex"
            >
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" className="h-5 w-5" aria-hidden>
                <rect x="3" y="3" width="18" height="18" rx="2" />
                <path d="M15 3v18" />
              </svg>
            </button>
            <button
              type="button"
              onClick={() => setDrawerOpen(false)}
              aria-label="关闭导航"
              className="ml-auto inline-flex min-h-11 min-w-11 items-center justify-center rounded-lg text-mute transition-colors hover:bg-soft hover:text-ink lg:hidden"
            >
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" className="h-5 w-5" aria-hidden>
                <path d="M18 6 6 18M6 6l12 12" />
              </svg>
            </button>
          </div>

          <nav className="flex-1 space-y-1 overflow-y-auto px-3 py-3" aria-label="管理端导航">
            {TABS.map((tab) => (
              <Link
                key={tab.key}
                href={tab.href}
                onClick={() => setDrawerOpen(false)}
                aria-current={active === tab.key ? "page" : undefined}
                title={tab.label}
                className={`admin-nav-item flex min-h-11 items-center gap-3 rounded-lg px-3 text-sm transition-colors ${
                  active === tab.key
                    ? "bg-accent-soft font-medium text-accent-ink"
                    : "text-ink-2 hover:bg-soft hover:text-accent-ink"
                }`}
              >
                <tab.icon />
                <span className="admin-nav-text whitespace-nowrap">{tab.label}</span>
              </Link>
            ))}
            <Link
              href="/"
              onClick={() => setDrawerOpen(false)}
              title="返回前台"
              className="admin-nav-item flex min-h-11 items-center gap-3 rounded-lg px-3 text-sm text-mute transition-colors hover:bg-soft hover:text-accent-ink"
            >
              <IconHome />
              <span className="admin-nav-text whitespace-nowrap">返回前台</span>
            </Link>
          </nav>

          <div className="space-y-2 border-t border-line-2 px-3 py-3">
            <div className="admin-nav-appear flex items-center justify-between gap-2">
              <span className="admin-nav-text text-xs text-faint">外观</span>
              <AppearanceSwitcher defaultAppearance={defaultAppearance} />
            </div>
            <div className="admin-nav-appear">
              <LogoutButton />
            </div>
            <div className="admin-nav-icononly">
              <LogoutButton compact />
            </div>
          </div>
        </aside>
      </div>
    </div>
  );
}

/* --- Nav icons: 24px stroke glyphs (lucide-style paths), one per tab. --- */

function iconProps() {
  return {
    viewBox: "0 0 24 24",
    fill: "none",
    stroke: "currentColor",
    strokeWidth: 2,
    strokeLinecap: "round",
    strokeLinejoin: "round",
    className: "h-5 w-5 shrink-0",
    "aria-hidden": true,
  } as const;
}

function IconOverview() {
  return (
    <svg {...iconProps()}>
      <rect x="3" y="3" width="7" height="9" rx="1" />
      <rect x="14" y="3" width="7" height="5" rx="1" />
      <rect x="14" y="12" width="7" height="9" rx="1" />
      <rect x="3" y="16" width="7" height="5" rx="1" />
    </svg>
  );
}

function IconTorrents() {
  return (
    <svg {...iconProps()}>
      <ellipse cx="12" cy="5" rx="9" ry="3" />
      <path d="M3 5v14a9 3 0 0 0 18 0V5" />
      <path d="M3 12a9 3 0 0 0 18 0" />
    </svg>
  );
}

function IconReview() {
  return (
    <svg {...iconProps()}>
      <circle cx="12" cy="12" r="9" />
      <path d="M12 7v5l3 2" />
    </svg>
  );
}

function IconBlocked() {
  return (
    <svg {...iconProps()}>
      <circle cx="12" cy="12" r="9" />
      <path d="m5.5 5.5 13 13" />
    </svg>
  );
}

function IconLlm() {
  return (
    <svg {...iconProps()}>
      <rect x="5" y="5" width="14" height="14" rx="2" />
      <rect x="10" y="10" width="4" height="4" />
      <path d="M9 2v3M15 2v3M9 19v3M15 19v3M2 9h3M2 15h3M19 9h3M19 15h3" />
    </svg>
  );
}

function IconReports() {
  return (
    <svg {...iconProps()}>
      <path d="M4 22V4c4-2 8 2 12 0v10c-4 2-8-2-12 0" />
    </svg>
  );
}

function IconUsers() {
  return (
    <svg {...iconProps()}>
      <circle cx="9" cy="8" r="3.5" />
      <path d="M2.5 20c.8-3.2 3.4-5 6.5-5s5.7 1.8 6.5 5" />
      <path d="M16 5.2a3.5 3.5 0 0 1 0 5.6M17.8 15.4c1.9.7 3.2 2.2 3.7 4.6" />
    </svg>
  );
}

function IconSettings() {
  return (
    <svg {...iconProps()}>
      <path d="M4 6h8M16 6h4M4 12h3M11 12h9M4 18h10M18 18h2" />
      <circle cx="14" cy="6" r="2" />
      <circle cx="9" cy="12" r="2" />
      <circle cx="16" cy="18" r="2" />
    </svg>
  );
}

function IconKey() {
  return (
    <svg {...iconProps()}>
      <circle cx="8" cy="15" r="4.5" />
      <path d="m11.2 11.8 8.3-8.3M17 5l3 3M14 8l2.5 2.5" />
    </svg>
  );
}

function IconHome() {
  return (
    <svg {...iconProps()}>
      <path d="m3 11 9-8 9 8" />
      <path d="M5 9.5V21h14V9.5" />
      <path d="M10 21v-6h4v6" />
    </svg>
  );
}
