import Link from "next/link";
import { settingsGroupHref } from "@/lib/settings-groups";

// Sub-navigation for the settings area. Every group is its own page
// (/admin/settings/<key>); this file carries the desktop rendering —
// a sticky rail — while the mobile pill row lives in
// SettingsNavChips.tsx (it needs client-side scroll handling). Both
// render the same list with real links.

export interface SettingsNavEntry {
  key: string;
  label: string;
  count: number;
}

function entryClass(active: boolean) {
  return active
    ? "bg-accent-soft font-medium text-accent-ink"
    : "text-mute hover:bg-soft hover:text-accent-ink";
}

export function SettingsNavRail({
  groups,
  active,
}: {
  groups: SettingsNavEntry[];
  active: string;
}) {
  return (
    <nav
      aria-label="设置分组"
      className="sticky top-4 hidden w-40 shrink-0 lg:block"
    >
      <ul className="space-y-1">
        <li>
          <Link
            href="/admin/settings"
            className="flex min-h-9 items-center gap-2 rounded-lg px-3 text-sm text-mute transition-colors hover:bg-soft hover:text-accent-ink"
          >
            <svg
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              strokeWidth="2"
              strokeLinecap="round"
              strokeLinejoin="round"
              className="h-4 w-4 shrink-0"
              aria-hidden
            >
              <path d="m15 18-6-6 6-6" />
            </svg>
            <span className="min-w-0 truncate">设置总览</span>
          </Link>
        </li>
        {groups.map((g) => (
          <li key={g.key}>
            <Link
              href={settingsGroupHref(g.key)}
              aria-current={active === g.key ? "page" : undefined}
              className={`flex min-h-9 items-center justify-between gap-2 rounded-lg px-3 text-sm transition-colors ${entryClass(
                active === g.key
              )}`}
            >
              <span className="min-w-0 truncate">{g.label}</span>
              <span className="shrink-0 text-xs tabular-nums opacity-70">
                {g.count}
              </span>
            </Link>
          </li>
        ))}
      </ul>
    </nav>
  );
}

