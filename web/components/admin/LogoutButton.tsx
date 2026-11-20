"use client";

import { useRouter } from "next/navigation";
import { useState } from "react";

export default function LogoutButton({
  compact = false,
}: {
  /** Icon-only variant for the collapsed sidebar rail. */
  compact?: boolean;
}) {
  const router = useRouter();
  const [busy, setBusy] = useState(false);
  const onClick = async () => {
    setBusy(true);
    try {
      await fetch("/api/admin-session", { method: "DELETE" });
    } finally {
      router.push("/admin/login");
      router.refresh();
    }
  };
  if (compact) {
    return (
      <button
        type="button"
        disabled={busy}
        onClick={onClick}
        aria-label="退出登录"
        title="退出登录"
        className="inline-flex min-h-11 min-w-11 items-center justify-center rounded-lg border border-input bg-soft text-ink-2 transition-colors hover:border-accent hover:text-accent-ink disabled:opacity-50"
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
          <path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4" />
          <path d="m16 17 5-5-5-5M21 12H9" />
        </svg>
      </button>
    );
  }
  return (
    <button
      type="button"
      disabled={busy}
      onClick={onClick}
      className="inline-flex min-h-11 w-full items-center rounded-lg border border-input bg-soft px-4 text-sm text-ink-2 transition-colors hover:border-accent hover:text-accent-ink disabled:opacity-50"
    >
      退出登录
    </button>
  );
}
