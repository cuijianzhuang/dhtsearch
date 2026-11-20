"use client";

// Compatibility shim for pre-split bookmarks: the old single-page
// settings screen addressed groups by in-page anchor
// (/admin/settings#settings-group-<key>). Those hashes are
// enumerable from the registry group keys, so on landing on the
// index with one, forward to the group's own page.

import { useEffect } from "react";
import { useRouter } from "next/navigation";
import { settingsGroupHref } from "@/lib/settings-groups";

export default function SettingsHashRedirect({ keys }: { keys: string[] }) {
  const router = useRouter();
  useEffect(() => {
    const m = window.location.hash.match(/^#settings-group-(.+)$/);
    if (m && keys.includes(m[1])) {
      router.replace(settingsGroupHref(m[1]));
    }
  }, [keys, router]);
  return null;
}
