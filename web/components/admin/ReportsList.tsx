"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useState } from "react";
import type { ReportItem } from "@/lib/admin";
import { formatRelativeTime } from "@/lib/format";
import { postAdminAction } from "./TorrentActions";

export const REPORT_REASON_LABELS: Record<string, string> = {
  adult: "成人内容",
  spam: "垃圾信息",
  infringement: "侵权",
  other: "其他",
};

const STATUS_LABELS: Record<number, string> = {
  0: "待处理",
  1: "已处理",
  2: "已忽略",
};

const BTN =
  "inline-flex min-h-11 items-center justify-center rounded-lg border px-3 text-xs transition-colors disabled:opacity-50";

/** The visitor-report queue: each row shows what was reported and why,
 *  with the three operator outcomes — mark handled, block the torrent
 *  (delete + blacklist, handled in one click), or dismiss. */
export default function ReportsList({ items }: { items: ReportItem[] }) {
  const router = useRouter();
  const [busy, setBusy] = useState<number | null>(null);
  const [error, setError] = useState<string | null>(null);

  async function act(id: number, kind: "report-resolve" | "report-dismiss" | "report-block", label: string) {
    if (kind === "report-block" && !window.confirm("拉黑会删除该种子并阻止再次收录，确定？")) {
      return;
    }
    setBusy(id);
    setError(null);
    const res = await postAdminAction({ kind, id });
    setBusy(null);
    if (res.ok) {
      router.refresh();
    } else {
      setError(`${label}失败${res.error ? `：${res.error}` : `（HTTP ${res.status}）`}`);
    }
  }

  if (items.length === 0) {
    return (
      <div className="rounded-xl border border-line bg-surface p-10 text-center text-sm text-mute">
        该筛选下暂无举报记录。
      </div>
    );
  }

  return (
    <div>
      {error && (
        <p className="mb-3 rounded-lg border border-bad-line bg-bad-bg px-3 py-2 text-sm text-bad-ink">
          {error}
        </p>
      )}
      <ul className="space-y-3">
        {items.map((r) => (
          <li
            key={r.id}
            className="rounded-xl border border-line bg-surface p-4 shadow-card"
          >
            <div className="flex flex-wrap items-start justify-between gap-3">
              <div className="min-w-0">
                <p className="wrap-anywhere text-sm font-medium text-ink">
                  {r.name ? (
                    <Link
                      href={`/search?q=${encodeURIComponent(r.name)}`}
                      className="hover:text-accent-ink hover:underline"
                    >
                      {r.name}
                    </Link>
                  ) : (
                    <span className="text-mute">（种子已删除）</span>
                  )}
                </p>
                <p className="mt-1 font-mono text-[11px] break-all text-faint">
                  {r.info_hash}
                </p>
                <p className="mt-2 text-xs text-ink-2">
                  <span className="rounded-md bg-warn-bg px-2 py-0.5 font-medium text-warn-ink">
                    {REPORT_REASON_LABELS[r.reason] ?? r.reason}
                  </span>
                  <span className="ml-2 text-mute">
                    {formatRelativeTime(r.created_at)} · 状态：
                    {STATUS_LABELS[r.status] ?? r.status}
                  </span>
                </p>
                {r.note && (
                  <p className="mt-1 wrap-anywhere text-xs text-mute">
                    备注：{r.note}
                  </p>
                )}
              </div>
              {r.status === 0 && (
                <div className="flex shrink-0 flex-wrap gap-2">
                  <button
                    type="button"
                    disabled={busy === r.id}
                    onClick={() => act(r.id, "report-resolve", "标记已处理")}
                    className={`${BTN} border-input bg-soft text-ink-2 hover:border-accent hover:text-accent-ink`}
                  >
                    标记已处理
                  </button>
                  <button
                    type="button"
                    disabled={busy === r.id}
                    onClick={() => act(r.id, "report-block", "拉黑")}
                    className={`${BTN} border-bad-line bg-bad-bg text-bad-ink hover:opacity-80`}
                  >
                    拉黑该种子
                  </button>
                  <button
                    type="button"
                    disabled={busy === r.id}
                    onClick={() => act(r.id, "report-dismiss", "忽略")}
                    className={`${BTN} border-input bg-soft text-mute hover:border-accent hover:text-accent-ink`}
                  >
                    忽略
                  </button>
                </div>
              )}
            </div>
          </li>
        ))}
      </ul>
    </div>
  );
}
