"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";

// Shared public footer, rendered once from the root layout after the
// page content. The public pages each carry their own small in-main
// footer line (home: index stats; search: name + tagline), but none
// of them linked anywhere — in particular there was no way to reach
// the admin panel except by typing /admin. This bar carries the two
// utility entries (API 文档 for integrators, 管理 for the operator):
// deliberately quiet (faint text, hairline top border,
// canvas background) because it serves the operator, not visitors.
// The left side is the site name only — the search page's own footer
// already prints name + tagline, and repeating the same line twice
// in a row reads as a rendering bug.
//
// Admin pages keep their own shell, so this renders nothing under
// /admin (same exclusion as MobileBottomNav). Mobile clearance: the
// bottom nav's in-flow spacer is rendered after this footer in the
// layout, so the footer never slides under the fixed nav.

export default function PublicFooter({ siteName }: { siteName: string }) {
  const pathname = usePathname() ?? "";
  if (pathname.startsWith("/admin")) return null;

  return (
    <footer className="border-t border-line">
      <div className="mx-auto flex w-full max-w-3xl items-center justify-between gap-4 px-4">
        <span className="py-3 text-xs text-faint">{siteName}</span>
        <span className="-mr-2 flex items-center">
          <Link
            href="/api-docs"
            className="flex min-h-11 items-center gap-1.5 px-2 text-xs text-mute transition-colors hover:text-accent-ink"
          >
            <IconCode />
            API 文档
          </Link>
          <Link
            href="/admin"
            prefetch={false}
            className="flex min-h-11 items-center gap-1.5 px-2 text-xs text-mute transition-colors hover:text-accent-ink"
          >
            <IconLock />
            管理
          </Link>
        </span>
      </div>
    </footer>
  );
}

function IconCode() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={2}
      strokeLinecap="round"
      strokeLinejoin="round"
      className="h-3.5 w-3.5"
      aria-hidden
    >
      <path d="m8 6-6 6 6 6" />
      <path d="m16 6 6 6-6 6" />
    </svg>
  );
}

function IconLock() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={2}
      strokeLinecap="round"
      strokeLinejoin="round"
      className="h-3.5 w-3.5"
      aria-hidden
    >
      <rect x="4" y="11" width="16" height="10" rx="2" />
      <path d="M8 11V7a4 4 0 0 1 8 0v4" />
    </svg>
  );
}
