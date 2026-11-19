"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useMemo, useState } from "react";
import type { AdminTorrentItem } from "@/lib/admin";
import { formatCount, formatRelativeTime, formatSize } from "@/lib/format";
import TorrentActions, { postAdminAction } from "./TorrentActions";

/**
 * The torrent management list: a real table on desktop, cards on
 * mobile. With `selectable` (the review queue) rows get checkboxes and
 * a bulk approve/block bar.
 */
export default function AdminTorrentList({
  items,
  selectable = false,
}: {
  items: AdminTorrentItem[];
  selectable?: boolean;
}) {
  const router = useRouter();
  const [selected, setSelected] = useState<ReadonlySet<string>>(new Set());
  const [busy, setBusy] = useState(false);
  const [failed, setFailed] = useState(false);

  const selectedHashes = useMemo(
    () => items.filter((t) => selected.has(t.info_hash)).map((t) => t.info_hash),
    [items, selected]
  );

  function toggle(hash: string) {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(hash)) next.delete(hash);
      else next.add(hash);
      return next;
    });
  }

  async function bulk(action: "approve" | "block") {
    if (selectedHashes.length === 0) return;
    if (
      action === "block" &&
      !window.confirm(
        `拉黑选中的 ${selectedHashes.length} 条？它们会被删除并记录进黑名单，不会再次收录。`
      )
    ) {
      return;
    }
    setBusy(true);
    setFailed(false);
    const res = await postAdminAction({
      kind: "batch",
      hashes: selectedHashes,
      action,
    });
    setBusy(false);
    if (!res.ok) {
      if (res.status === 401) {
        router.push("/admin/login");
        return;
      }
      setFailed(true);
      return;
    }
    setSelected(new Set());
    router.refresh();
  }

  if (items.length === 0) {
    return (
      <div className="rounded-xl border border-line bg-surface p-8 text-center shadow-card">
        <p className="text-ink-2">没有符合条件的种子</p>
        <p className="mt-2 text-sm text-mute">换个关键词或状态过滤试试。</p>
      </div>
    );
  }

  return (
    <div>
      {selectable && selectedHashes.length > 0 && (
        <div className="sticky top-2 z-10 mb-4 flex flex-wrap items-center gap-2 rounded-xl border border-accent-line bg-accent-soft px-4 py-2 shadow-bar">
          <span className="text-sm text-accent-ink">
            已选 {selectedHashes.length} 条
          </span>
          <button
            type="button"
            disabled={busy}
            onClick={() => bulk("approve")}
            className="inline-flex min-h-11 items-center rounded-lg border border-accent-line bg-surface px-3 text-sm text-accent-ink disabled:opacity-50"
          >
            批量通过
          </button>
          <button
            type="button"
            disabled={busy}
            onClick={() => bulk("block")}
            className="inline-flex min-h-11 items-center rounded-lg border border-input bg-surface px-3 text-sm text-warn-ink disabled:opacity-50"
          >
            批量拉黑
          </button>
          <button
            type="button"
            disabled={busy}
            onClick={() => setSelected(new Set())}
            className="inline-flex min-h-11 items-center rounded-lg px-3 text-sm text-mute disabled:opacity-50"
          >
            清除选择
          </button>
          {failed && <span className="text-xs text-warn-ink">批量操作失败，请重试</span>}
        </div>
      )}

      {/* Desktop table */}
      <div className="hidden overflow-x-auto rounded-xl border border-line bg-surface shadow-card md:block">
        <table className="w-full text-left text-sm">
          <thead>
            <tr className="border-b border-line text-xs text-mute">
              {selectable && <th className="px-3 py-3 font-medium">选择</th>}
              <th className="px-3 py-3 font-medium">名称</th>
              <th className="px-3 py-3 font-medium whitespace-nowrap">大小</th>
              <th className="px-3 py-3 font-medium whitespace-nowrap">文件</th>
              <th className="px-3 py-3 font-medium whitespace-nowrap">收录</th>
              <th className="px-3 py-3 font-medium whitespace-nowrap">状态</th>
              <th className="px-3 py-3 font-medium whitespace-nowrap">操作</th>
            </tr>
          </thead>
          <tbody>
            {items.map((t) => (
              <tr key={t.info_hash} className="border-b border-line-2 align-top last:border-0">
                {selectable && (
                  <td className="px-3 py-3">
                    <label className="-m-3 block cursor-pointer p-3">
                      <input
                        type="checkbox"
                        checked={selected.has(t.info_hash)}
                        onChange={() => toggle(t.info_hash)}
                        aria-label={`选择 ${t.name || t.info_hash}`}
                        className="block h-5 w-5 cursor-pointer accent-accent"
                      />
                    </label>
                  </td>
                )}
                <td className="max-w-105 px-3 py-3">
                  <Link
                    href={`/admin/torrents/${t.info_hash}`}
                    className="font-medium wrap-anywhere text-accent-ink hover:underline"
                  >
                    {t.name || "(未命名)"}
                  </Link>
                  {t.alias && t.alias !== t.name && (
                    <p className="mt-1 line-clamp-1 text-xs text-mute">{t.alias}</p>
                  )}
                  <p className="mt-1 font-mono text-[11px] break-all text-faint">
                    {t.info_hash}
                  </p>
                </td>
                <td className="px-3 py-3 whitespace-nowrap text-mute">
                  {formatSize(t.total_size)}
                </td>
                <td className="px-3 py-3 whitespace-nowrap text-mute">
                  {formatCount(t.file_count)}
                </td>
                <td className="px-3 py-3 whitespace-nowrap text-mute">
                  {formatRelativeTime(t.created_at)}
                </td>
                <td className="px-3 py-3 whitespace-nowrap">
                  <ReviewBadge reviewed={t.reviewed_at > 0} />
                </td>
                <td className="px-3 py-3">
                  <TorrentActions hash={t.info_hash} name={t.name || t.info_hash} reviewed={t.reviewed_at > 0} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {/* Mobile cards */}
      <div className="space-y-3 md:hidden">
        {items.map((t) => (
          <div
            key={t.info_hash}
            className="rounded-xl border border-line bg-surface p-4 shadow-card"
          >
            <div className="flex items-start gap-2">
              {selectable && (
                <label className="-m-3 shrink-0 cursor-pointer p-3">
                  <input
                    type="checkbox"
                    checked={selected.has(t.info_hash)}
                    onChange={() => toggle(t.info_hash)}
                    aria-label={`选择 ${t.name || t.info_hash}`}
                    className="block h-5 w-5 cursor-pointer accent-accent"
                  />
                </label>
              )}
              <div className="min-w-0 flex-1">
                <Link
                  href={`/admin/torrents/${t.info_hash}`}
                  className="font-medium wrap-anywhere text-accent-ink"
                >
                  {t.name || "(未命名)"}
                </Link>
                <p className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-mute">
                  <span>{formatSize(t.total_size)}</span>
                  <span>{formatCount(t.file_count)} 个文件</span>
                  <span>{formatRelativeTime(t.created_at)}</span>
                  <ReviewBadge reviewed={t.reviewed_at > 0} />
                </p>
                <p className="mt-1 font-mono text-[11px] break-all text-faint">
                  {t.info_hash}
                </p>
              </div>
            </div>
            <div className="mt-3">
              <TorrentActions hash={t.info_hash} name={t.name || t.info_hash} reviewed={t.reviewed_at > 0} />
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}

export function ReviewBadge({ reviewed }: { reviewed: boolean }) {
  return reviewed ? (
    <span className="inline-flex items-center rounded-md bg-accent-soft px-2 py-1 text-xs text-accent-ink">
      已审核
    </span>
  ) : (
    <span className="inline-flex items-center rounded-md bg-soft px-2 py-1 text-xs text-warn-ink">
      未审核
    </span>
  );
}
