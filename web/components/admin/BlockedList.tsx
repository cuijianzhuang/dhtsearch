"use client";

import { useRouter } from "next/navigation";
import { useState } from "react";
import type { BlockedItem } from "@/lib/admin";
import { formatDate } from "@/lib/format";
import { postAdminAction } from "./TorrentActions";

/** The blocked-table list, with one-click unblock per entry. */
export default function BlockedList({ items }: { items: BlockedItem[] }) {
  const router = useRouter();
  const [busy, setBusy] = useState<string | null>(null);
  const [failed, setFailed] = useState<string | null>(null);

  async function unblock(item: BlockedItem) {
    if (
      !window.confirm(
        `取消拉黑「${item.name || item.info_hash}」？取消后该 infohash 被重新发现时可以再次收录。`
      )
    ) {
      return;
    }
    setBusy(item.info_hash);
    setFailed(null);
    const res = await postAdminAction({ kind: "unblock", hash: item.info_hash });
    setBusy(null);
    if (!res.ok) {
      if (res.status === 401) {
        router.push("/admin/login");
        return;
      }
      setFailed(item.info_hash);
      return;
    }
    router.refresh();
  }

  if (items.length === 0) {
    return (
      <div className="rounded-xl border border-line bg-surface p-8 text-center shadow-card">
        <p className="text-ink-2">黑名单是空的</p>
        <p className="mt-2 text-sm text-mute">
          被审核拉黑的 infohash 会出现在这里，拉黑后爬虫不会再次收录。
        </p>
      </div>
    );
  }

  return (
    <div className="space-y-3">
      {items.map((item) => (
        <div
          key={item.info_hash}
          className="rounded-xl border border-line bg-surface p-4 shadow-card"
        >
          <div className="flex flex-wrap items-start justify-between gap-3">
            <div className="min-w-0 flex-1">
              <p className="font-medium wrap-anywhere text-ink">
                {item.name || "(未命名)"}
              </p>
              <p className="mt-1 flex flex-wrap gap-x-3 gap-y-1 text-xs text-mute">
                <span>原因：{item.reason}</span>
                <span>拉黑于 {formatDate(item.created_at)}</span>
              </p>
              <p className="mt-1 font-mono text-[11px] break-all text-faint">
                {item.info_hash}
              </p>
              {failed === item.info_hash && (
                <p className="mt-1 text-xs text-warn-ink">操作失败，请重试</p>
              )}
            </div>
            <button
              type="button"
              disabled={busy !== null}
              onClick={() => unblock(item)}
              className="inline-flex min-h-11 shrink-0 items-center rounded-lg border border-input bg-soft px-3 text-sm text-ink-2 transition-colors hover:border-accent hover:text-accent-ink disabled:opacity-50"
            >
              {busy === item.info_hash ? "处理中…" : "取消拉黑"}
            </button>
          </div>
        </div>
      ))}
    </div>
  );
}
