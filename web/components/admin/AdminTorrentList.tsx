"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useMemo, useState } from "react";
import type { AdminTorrentItem } from "@/lib/admin";
import { formatCount, formatRelativeTime, formatSize } from "@/lib/format";
import TorrentActions, { postAdminAction } from "./TorrentActions";

/**
 * The torrent management list: a real table on desktop, cards on
 * mobile. With `selectable` rows get checkboxes and a floating bulk
 * action bar (approve / block / delete) once anything is checked.
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

  async function bulk(action: "approve" | "block" | "delete") {
    if (selectedHashes.length === 0) return;
    if (
      action === "block" &&
      !window.confirm(
        `拉黑选中的 ${selectedHashes.length} 条？它们会被删除并记录进黑名单，不会再次收录。`
      )
    ) {
      return;
    }
    if (
      action === "delete" &&
      !window.confirm(
        `删除选中的 ${selectedHashes.length} 条？只删记录、不进黑名单，之后被重新发现仍会收录。`
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
      {/* Desktop table. From xl up the layout is fixed with explicit
          column widths: the data columns are sized to their content
          and the actions column absorbs the slack with its buttons
          right-anchored, so on wide monitors the name column can no
          longer hoard the extra width and leave a void before 大小.
          At 2xl the name column itself is capped (33rem — roomier
          than the old 105-wide content cap) and every remaining pixel
          goes to the actions column. Below xl the auto layout stays:
          it already composes fine at those widths. */}
      <div className="hidden overflow-x-auto rounded-xl border border-line bg-surface shadow-card md:block">
        <table className="w-full text-left text-sm xl:table-fixed">
          <colgroup>
            {selectable && <col className="xl:w-12" />}
            <col className="2xl:w-[33rem]" />
            <col className="xl:w-28" />
            <col className="xl:w-20" />
            <col className="xl:w-32" />
            <col className="xl:w-24" />
            <col />
          </colgroup>
          <thead>
            <tr className="border-b border-line text-xs text-mute">
              {selectable && <th className="px-3 py-3 font-medium">选择</th>}
              <th className="px-3 py-3 font-medium">名称</th>
              <th className="px-3 py-3 font-medium whitespace-nowrap text-right">大小</th>
              <th className="px-3 py-3 font-medium whitespace-nowrap text-right">文件</th>
              <th className="px-3 py-3 font-medium whitespace-nowrap">收录</th>
              <th className="px-3 py-3 font-medium whitespace-nowrap">状态</th>
              <th className="px-3 py-3 font-medium whitespace-nowrap text-right">操作</th>
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
                <td className="max-w-105 px-3 py-3 2xl:max-w-none">
                  {/* Clamp the name to two lines: release names run to
                      200+ characters and an unclamped wrap made single
                      rows 4-6 lines tall, wrecking the table's scan
                      rhythm. Full text is one click away on the detail
                      page (and in the title tooltip). */}
                  <Link
                    href={`/admin/torrents/${t.info_hash}`}
                    title={t.name || undefined}
                    className="line-clamp-2 font-medium wrap-anywhere text-accent-ink hover:underline"
                  >
                    {t.name || "(未命名)"}
                  </Link>
                  {t.alias && t.alias !== t.name && (
                    <p className="mt-1 line-clamp-1 text-xs text-mute">{t.alias}</p>
                  )}
                  <p
                    className="mt-1 truncate font-mono text-[11px] text-faint"
                    title={t.info_hash}
                  >
                    {t.info_hash}
                  </p>
                </td>
                <td className="px-3 py-3 font-medium whitespace-nowrap tabular-nums text-right text-ink-2">
                  {formatSize(t.total_size)}
                </td>
                <td className="px-3 py-3 whitespace-nowrap tabular-nums text-right text-mute">
                  {formatCount(t.file_count)}
                </td>
                <td className="px-3 py-3 whitespace-nowrap text-mute">
                  {formatRelativeTime(t.created_at)}
                </td>
                <td className="px-3 py-3 whitespace-nowrap">
                  <ReviewBadge reviewed={t.reviewed_at > 0} />
                </td>
                <td className="px-3 py-3">
                  {/* Right-anchored: in the fixed layout this column
                      absorbs the wide-screen slack, and the buttons
                      belong at the table's right edge, not floating
                      wherever the slack begins. */}
                  <div className="flex justify-end">
                    <TorrentActions hash={t.info_hash} name={t.name || t.info_hash} reviewed={t.reviewed_at > 0} />
                  </div>
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
                  title={t.name || undefined}
                  className="line-clamp-3 font-medium wrap-anywhere text-accent-ink"
                >
                  {t.name || "(未命名)"}
                </Link>
                <p className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-mute">
                  <span className="font-medium tabular-nums text-ink-2">
                    {formatSize(t.total_size)}
                  </span>
                  <span className="tabular-nums">
                    {formatCount(t.file_count)} 个文件
                  </span>
                  <span>{formatRelativeTime(t.created_at)}</span>
                  <ReviewBadge reviewed={t.reviewed_at > 0} />
                </p>
                <p
                  className="mt-1 truncate font-mono text-[11px] text-faint"
                  title={t.info_hash}
                >
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

      {selectable && selectedHashes.length > 0 && (
        // Floating bulk bar. Sticky (not fixed), same pattern as the
        // front result list: it rides the viewport bottom while the
        // list scrolls, then settles into the flow at the end of the
        // list — so it can never sit on top of the pagination that
        // follows the list. Solid surface, no backdrop-blur (a blurred
        // bar re-samples the scrolling content every frame on mobile).
        // The bottom offset respects the iPhone home-indicator inset.
        <div className="sticky bottom-[max(1rem,env(safe-area-inset-bottom))] z-20 mt-3">
          <div className="flex w-full flex-wrap items-center gap-2 rounded-xl border border-input bg-surface px-4 py-3 shadow-bar">
            <span className="shrink-0 text-sm text-ink-2">
              已选{" "}
              <span className="font-medium text-accent-ink">
                {selectedHashes.length}
              </span>{" "}
              项
            </span>
            <div className="ml-auto flex flex-wrap items-center gap-2">
              <button
                type="button"
                disabled={busy}
                onClick={() => bulk("approve")}
                className="inline-flex min-h-11 items-center rounded-lg border border-accent-line bg-accent-soft px-3 text-sm font-medium text-accent-ink transition-colors hover:border-accent disabled:opacity-50"
              >
                通过
              </button>
              <button
                type="button"
                disabled={busy}
                onClick={() => bulk("block")}
                className="inline-flex min-h-11 items-center rounded-lg border border-input bg-soft px-3 text-sm text-warn-ink transition-colors hover:border-warn disabled:opacity-50"
              >
                拉黑
              </button>
              <button
                type="button"
                disabled={busy}
                onClick={() => bulk("delete")}
                className="inline-flex min-h-11 items-center rounded-lg border border-input bg-soft px-3 text-sm text-ink-2 transition-colors hover:border-warn hover:text-warn-ink disabled:opacity-50"
              >
                删除
              </button>
              <button
                type="button"
                disabled={busy}
                onClick={() => setSelected(new Set())}
                className="inline-flex min-h-11 items-center rounded-lg px-3 text-sm text-mute transition-colors hover:text-ink disabled:opacity-50"
              >
                清除选择
              </button>
            </div>
            {failed && (
              <span className="w-full text-xs text-warn-ink">
                批量操作失败，请重试
              </span>
            )}
          </div>
        </div>
      )}
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
