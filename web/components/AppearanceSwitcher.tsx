"use client";

// Three-state appearance switcher （浅色 / 深色 / 跟随系统）, shown in
// the front and admin headers. The visitor's choice is applied to
// <html data-appearance> immediately and stored twice: in
// localStorage (read back by this control and by the boot script)
// and in a cookie, which the root layout reads server-side so it
// renders the visitor's choice directly. The cookie is what makes
// the choice survive router.refresh() and soft navigations — those
// re-render the layout from the server, and a server that cannot
// see the choice re-renders the admin default over it. The boot
// script in the root layout covers choices that exist only in
// localStorage (older visits, expired cookie) before first paint.
//
// localStorage is an external store, so the current choice is read
// through useSyncExternalStore: the server snapshot is the admin
// default, the client snapshot is the stored choice, and choose()
// notifies subscribers through a window event (the "storage" event
// covers other tabs).

import { useSyncExternalStore } from "react";
import {
  APPEARANCE_COOKIE,
  APPEARANCE_STORAGE_KEY,
  APPEARANCES,
  parseAppearance,
  type Appearance,
} from "@/lib/theme";

const CHANGE_EVENT = "dhtsearch-appearance-change";

function subscribe(onChange: () => void) {
  window.addEventListener("storage", onChange);
  window.addEventListener(CHANGE_EVENT, onChange);
  return () => {
    window.removeEventListener("storage", onChange);
    window.removeEventListener(CHANGE_EVENT, onChange);
  };
}

function readStored(): Appearance | null {
  try {
    return parseAppearance(localStorage.getItem(APPEARANCE_STORAGE_KEY));
  } catch {
    return null;
  }
}

/** Persist a choice to both stores (see the header comment for why
 *  there are two). Each write is independent: in an environment
 *  where one store is unavailable (e.g. localStorage throwing in a
 *  private window) the other still carries the choice. */
function persistChoice(next: Appearance) {
  try {
    localStorage.setItem(APPEARANCE_STORAGE_KEY, next);
  } catch {
    // Storage unavailable — the cookie below still persists it.
  }
  document.cookie = `${APPEARANCE_COOKIE}=${next};path=/;max-age=31536000;samesite=lax`;
}

function Icon({ mode }: { mode: Appearance }) {
  const common = {
    width: 16,
    height: 16,
    viewBox: "0 0 24 24",
    fill: "none",
    stroke: "currentColor",
    strokeWidth: 2,
    strokeLinecap: "round",
    strokeLinejoin: "round",
    "aria-hidden": true,
  } as const;
  if (mode === "light") {
    return (
      <svg {...common}>
        <circle cx="12" cy="12" r="4" />
        <path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M6.34 17.66l-1.41 1.41M19.07 4.93l-1.41 1.41" />
      </svg>
    );
  }
  if (mode === "dark") {
    return (
      <svg {...common}>
        <path d="M12 3a6 6 0 0 0 9 9 9 9 0 1 1-9-9Z" />
      </svg>
    );
  }
  return (
    <svg {...common}>
      <rect width="20" height="14" x="2" y="3" rx="2" />
      <line x1="8" x2="16" y1="21" y2="21" />
      <line x1="12" x2="12" y1="17" y2="21" />
    </svg>
  );
}

export default function AppearanceSwitcher({
  defaultAppearance,
}: {
  defaultAppearance: Appearance;
}) {
  const mode = useSyncExternalStore(
    subscribe,
    () => readStored() ?? defaultAppearance,
    () => defaultAppearance
  );

  function choose(next: Appearance) {
    persistChoice(next);
    document.documentElement.setAttribute("data-appearance", next);
    window.dispatchEvent(new Event(CHANGE_EVENT));
  }

  return (
    <div
      className="inline-flex items-center gap-1 rounded-full bg-soft p-1"
      role="group"
      aria-label="外观切换"
    >
      {APPEARANCES.map((a) => (
        <button
          key={a.key}
          type="button"
          onClick={() => choose(a.key)}
          aria-pressed={mode === a.key}
          title={a.label}
          className={`inline-flex h-8 w-8 items-center justify-center rounded-full transition-colors ${
            mode === a.key
              ? "bg-surface text-ink shadow-card"
              : "text-mute hover:text-ink"
          }`}
        >
          <Icon mode={a.key} />
          <span className="sr-only">{a.label}</span>
        </button>
      ))}
    </div>
  );
}
