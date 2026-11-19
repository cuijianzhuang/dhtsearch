"use client";

import { useRouter } from "next/navigation";
import { useState } from "react";

export type AdminActionKind = "approve" | "block" | "delete" | "unblock";

export async function postAdminAction(
  body: Record<string, unknown>
): Promise<{ ok: boolean; status: number; error?: string }> {
  try {
    const res = await fetch("/api/admin-action", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    if (res.ok) return { ok: true, status: res.status };
    let error: string | undefined;
    try {
      error = ((await res.json()) as { error?: string }).error;
    } catch {
      // Non-JSON error body — the status alone will have to do.
    }
    return { ok: false, status: res.status, error };
  } catch {
    return { ok: false, status: 0, error: "network" };
  }
}

const BTN =
  "inline-flex min-h-11 items-center justify-center rounded-lg border px-3 text-sm transition-colors disabled:opacity-50";

/**
 * Per-torrent operator actions: approve (mark reviewed), block (delete
 * + record in the blocked table so it cannot be re-indexed) and delete
 * (remove only). Block and delete ask for confirmation first.
 */
export default function TorrentActions({
  hash,
  name,
  reviewed,
  afterDeleteHref,
}: {
  hash: string;
  name: string;
  reviewed: boolean;
  /** When set, navigate here after a successful block/delete. */
  afterDeleteHref?: string;
}) {
  const router = useRouter();
  const [busy, setBusy] = useState<string | null>(null);
  const [failed, setFailed] = useState(false);

  async function run(kind: AdminActionKind, confirmText?: string) {
    if (confirmText && !window.confirm(confirmText)) return;
    setBusy(kind);
    setFailed(false);
    const res = await postAdminAction({ kind, hash });
    setBusy(null);
    if (!res.ok) {
      if (res.status === 401) {
        router.push("/admin/login");
        return;
      }
      setFailed(true);
      return;
    }
    if ((kind === "block" || kind === "delete") && afterDeleteHref) {
      router.push(afterDeleteHref);
    }
    router.refresh();
  }

  return (
    <div className="flex flex-wrap items-center gap-2">
      {!reviewed && (
        <button
          type="button"
          disabled={busy !== null}
          onClick={() => run("approve")}
          className={`${BTN} border-accent-line bg-accent-soft text-accent-ink hover:border-accent`}
        >
          {busy === "approve" ? "处理中…" : "通过"}
        </button>
      )}
      <button
        type="button"
        disabled={busy !== null}
        onClick={() =>
          run("block", `拉黑并删除「${name}」？该 infohash 将记录进黑名单，爬虫不会再次收录。`)
        }
        className={`${BTN} border-input bg-soft text-warn-ink hover:border-warn`}
      >
        {busy === "block" ? "处理中…" : "拉黑"}
      </button>
      <button
        type="button"
        disabled={busy !== null}
        onClick={() =>
          run("delete", `删除「${name}」？只删这一条记录，不进黑名单，之后被重新发现仍会收录。`)
        }
        className={`${BTN} border-input bg-soft text-ink-2 hover:border-warn hover:text-warn-ink`}
      >
        {busy === "delete" ? "处理中…" : "删除"}
      </button>
      {failed && <span className="text-xs text-warn-ink">操作失败，请重试</span>}
    </div>
  );
}
