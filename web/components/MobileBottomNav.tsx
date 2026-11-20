"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";

// Front-end mobile bottom navigation (<lg only). The front has three
// primary destinations: the home page, search (an empty query doubles
// as "latest indexed"), and the trending block on the home page.
// Admin pages keep their own shell navigation, so this renders
// nothing under /admin.
//
// The bar is fixed; a same-height spacer in the flow keeps page
// content (including the footers) from sliding under it. Both honour
// the iPhone home-indicator inset via env(safe-area-inset-bottom).
// Solid surface, no backdrop-blur — a blurred fixed bar re-samples
// the content scrolling under it every frame.

const ITEMS = [
  { href: "/", label: "首页", icon: IconHome, match: (p: string) => p === "/" },
  {
    href: "/search",
    label: "搜索",
    icon: IconSearch,
    match: (p: string) => p === "/search",
  },
  {
    href: "/#trending",
    label: "热榜",
    icon: IconFlame,
    // An anchor into the home page; it never owns a route, so it
    // takes no active state.
    match: () => false,
  },
] as const;

export default function MobileBottomNav() {
  const pathname = usePathname() ?? "";
  if (pathname.startsWith("/admin")) return null;

  return (
    <>
      <div
        aria-hidden
        className="h-[calc(3.5rem+env(safe-area-inset-bottom))] lg:hidden"
      />
      <nav
        aria-label="主导航"
        className="fixed inset-x-0 bottom-0 z-40 border-t border-line bg-surface pb-[env(safe-area-inset-bottom)] lg:hidden"
      >
        <div className="mx-auto flex max-w-3xl items-stretch justify-around">
          {ITEMS.map((item) => {
            const active = item.match(pathname);
            return (
              <Link
                key={item.label}
                href={item.href}
                prefetch={false}
                aria-current={active ? "page" : undefined}
                className={`flex min-h-14 flex-1 flex-col items-center justify-center gap-0.5 text-[11px] transition-colors ${
                  active
                    ? "font-medium text-accent-ink"
                    : "text-mute hover:text-accent-ink"
                }`}
              >
                <item.icon active={active} />
                {item.label}
              </Link>
            );
          })}
        </div>
      </nav>
    </>
  );
}

function iconProps(active: boolean) {
  return {
    viewBox: "0 0 24 24",
    fill: "none",
    stroke: "currentColor",
    strokeWidth: active ? 2.4 : 2,
    strokeLinecap: "round",
    strokeLinejoin: "round",
    className: "h-5 w-5",
    "aria-hidden": true,
  } as const;
}

function IconHome({ active }: { active: boolean }) {
  return (
    <svg {...iconProps(active)}>
      <path d="m3 11 9-8 9 8" />
      <path d="M5 9.5V21h14V9.5" />
      <path d="M10 21v-6h4v6" />
    </svg>
  );
}

function IconSearch({ active }: { active: boolean }) {
  return (
    <svg {...iconProps(active)}>
      <circle cx="11" cy="11" r="7" />
      <path d="m20 20-3.8-3.8" />
    </svg>
  );
}

function IconFlame({ active }: { active: boolean }) {
  return (
    <svg {...iconProps(active)}>
      <path d="M8.5 14.5A2.5 2.5 0 0 0 11 12c0-1.38-.5-2-1-3-1.072-2.143-.224-4.054 2-6 .5 2.5 2 4.9 4 6.5 2 1.6 3 3.5 3 5.5a7 7 0 1 1-14 0c0-1.153.433-2.294 1-3a2.5 2.5 0 0 0 2.5 2.5z" />
    </svg>
  );
}
