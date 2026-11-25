"use client";

import { useState } from "react";

const REASONS = [
  { key: "adult", label: "成人内容" },
  { key: "spam", label: "垃圾信息" },
  { key: "infringement", label: "侵权" },
  { key: "other", label: "其他" },
] as const;

/** Visitor report form inside a result's detail area. Submits through
 *  the /api/report proxy route; the backend rate-limits per
 *  visitor+torrent, and its 429 is surfaced as a "already reported"
 *  style message rather than a raw error. */
export default function ReportForm({
  infoHash,
  name,
}: {
  infoHash: string;
  name: string;
}) {
  const [open, setOpen] = useState(false);
  const [reason, setReason] = useState<string>("adult");
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function submit() {
    setBusy(true);
    setError(null);
    try {
      const res = await fetch("/api/report", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ info_hash: infoHash, reason, note: note.trim() }),
      });
      if (res.status === 201) {
        setDone(true);
        return;
      }
      if (res.status === 429) {
        setError("你对这条资源的举报次数过多，请稍后再试");
      } else if (res.status === 404) {
        setError("该资源已不存在，可能已被处理");
      } else {
        setError("提交失败，请稍后重试");
      }
    } catch {
      setError("提交失败，请稍后重试");
    } finally {
      setBusy(false);
    }
  }

  if (done) {
    return (
      <p className="mt-3 border-t border-line-2 pt-2 text-xs text-accent-ink">
        ✓ 已收到举报，管理员会尽快处理，谢谢反馈。
      </p>
    );
  }
  if (!open) {
    return (
      <div className="mt-3 border-t border-line-2 pt-2">
        <button
          type="button"
          onClick={() => setOpen(true)}
          className="inline-flex min-h-11 items-center text-xs text-mute transition-colors hover:text-warn-ink"
          aria-label={`举报 ${name || "该资源"}`}
        >
          🚩 举报这条资源
        </button>
      </div>
    );
  }
  return (
    <div className="mt-3 border-t border-line-2 pt-3">
      <p className="text-xs font-medium text-ink-2">举报理由</p>
      <div className="mt-2 flex flex-wrap gap-2">
        {REASONS.map((r) => (
          <label
            key={r.key}
            className={`inline-flex min-h-11 cursor-pointer items-center rounded-full border px-3 text-xs transition-colors ${
              reason === r.key
                ? "border-accent bg-accent-soft text-accent-ink"
                : "border-input bg-soft text-ink-2 hover:border-accent"
            }`}
          >
            <input
              type="radio"
              name={`report-reason-${infoHash}`}
              value={r.key}
              checked={reason === r.key}
              onChange={() => setReason(r.key)}
              className="sr-only"
            />
            {r.label}
          </label>
        ))}
      </div>
      <textarea
        value={note}
        onChange={(e) => setNote(e.target.value)}
        maxLength={500}
        rows={2}
        placeholder="备注（可选）：补充说明有助于管理员判断"
        className="mt-2 w-full rounded-control border border-input bg-surface px-3 py-2 text-xs text-ink placeholder-faint outline-none focus:border-accent"
      />
      {error && <p className="mt-1 text-xs text-warn-ink">{error}</p>}
      <div className="mt-2 flex gap-2">
        <button
          type="button"
          disabled={busy}
          onClick={submit}
          className="inline-flex min-h-11 items-center rounded-full bg-accent px-4 text-xs font-medium text-on-accent transition-colors hover:bg-accent-hover disabled:opacity-50"
        >
          {busy ? "提交中…" : "提交举报"}
        </button>
        <button
          type="button"
          disabled={busy}
          onClick={() => setOpen(false)}
          className="inline-flex min-h-11 items-center rounded-full border border-input bg-soft px-4 text-xs text-ink-2 transition-colors hover:border-accent disabled:opacity-50"
        >
          取消
        </button>
      </div>
    </div>
  );
}
