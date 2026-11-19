"use client";

import { useRouter } from "next/navigation";
import { useState } from "react";
import type { APITokenItem, CreateAPITokenResponse } from "@/lib/admin";
import { copyText } from "@/lib/clipboard";
import { formatCount, formatDate, formatRelativeTime } from "@/lib/format";

/** POST one action to the admin-action proxy and parse its JSON body. */
async function postAction<T>(
  body: Record<string, unknown>
): Promise<{ ok: boolean; status: number; data?: T; error?: string }> {
  try {
    const res = await fetch("/api/admin-action", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    let data: T | undefined;
    let error: string | undefined;
    try {
      const parsed = (await res.json()) as T & { error?: string };
      if (res.ok) data = parsed;
      else error = parsed.error;
    } catch {
      // Non-JSON body — the status alone will have to do.
    }
    return { ok: res.ok, status: res.status, data, error };
  } catch {
    return { ok: false, status: 0, error: "network" };
  }
}

const BTN =
  "inline-flex min-h-11 shrink-0 items-center justify-center rounded-lg border border-input bg-soft px-3 text-sm text-ink-2 transition-colors hover:border-accent hover:text-accent-ink disabled:opacity-50";
const INPUT =
  "min-h-11 rounded-lg border border-input bg-surface px-3 text-sm text-ink outline-none focus:border-accent";

/**
 * Public API token management: create (plaintext shown once), list
 * with usage counters, enable/disable, per-token rate, delete.
 */
export default function ApiTokensManager({ items }: { items: APITokenItem[] }) {
  const router = useRouter();
  const [name, setName] = useState("");
  const [rate, setRate] = useState("");
  const [creating, setCreating] = useState(false);
  const [createError, setCreateError] = useState<string | null>(null);
  const [created, setCreated] = useState<CreateAPITokenResponse | null>(null);
  const [copied, setCopied] = useState(false);
  const [busy, setBusy] = useState<string | null>(null);
  const [failed, setFailed] = useState<number | null>(null);
  const [editingRate, setEditingRate] = useState<number | null>(null);
  const [rateDraft, setRateDraft] = useState("");

  function guard(res: { ok: boolean; status: number }): boolean {
    if (res.ok) return true;
    if (res.status === 401) router.push("/admin/login");
    return false;
  }

  async function create(e: React.FormEvent) {
    e.preventDefault();
    const trimmed = name.trim();
    if (!trimmed) {
      setCreateError("请填写令牌名称");
      return;
    }
    let ratePerMin: number | undefined;
    if (rate.trim() !== "") {
      ratePerMin = Number(rate);
      if (!Number.isInteger(ratePerMin) || ratePerMin < 0) {
        setCreateError("限速必须是 ≥ 0 的整数（0 = 不限）");
        return;
      }
    }
    setCreating(true);
    setCreateError(null);
    const res = await postAction<CreateAPITokenResponse>({
      kind: "create-api-token",
      name: trimmed,
      ...(ratePerMin !== undefined ? { ratePerMin } : {}),
    });
    setCreating(false);
    if (!guard(res)) {
      setCreateError(res.error ?? "创建失败，请重试");
      return;
    }
    setCreated(res.data ?? null);
    setCopied(false);
    setName("");
    setRate("");
    router.refresh();
  }

  async function runOn(
    item: APITokenItem,
    kind: string,
    extra?: Record<string, unknown>,
    confirmText?: string
  ) {
    if (confirmText && !window.confirm(confirmText)) return;
    setBusy(`${kind}:${item.id}`);
    setFailed(null);
    const res = await postAction({ kind, id: item.id, ...extra });
    setBusy(null);
    if (!guard(res)) {
      setFailed(item.id);
      return;
    }
    setEditingRate(null);
    router.refresh();
  }

  return (
    <div className="space-y-6">
      {/* One-time plaintext reveal. Deliberately loud: closing this
          panel loses the token forever (only its hash is stored). */}
      {created && (
        <div className="rounded-xl border-2 border-warn bg-surface p-4 shadow-card">
          <p className="font-medium text-warn-ink">
            令牌已创建 — 明文只显示这一次
          </p>
          <p className="mt-1 text-sm text-mute">
            后端只保存令牌的哈希，关掉这个面板就再也看不到明文了。
            请立即复制并存到安全的地方；丢失只能删除后重建。
          </p>
          <div className="mt-3 flex flex-wrap items-center gap-2">
            <code className="min-w-0 flex-1 rounded-lg bg-soft px-3 py-2 font-mono text-sm break-all text-ink">
              {created.token}
            </code>
            <button
              type="button"
              className={BTN}
              onClick={async () => {
                await copyText(created.token);
                setCopied(true);
              }}
            >
              {copied ? "已复制" : "复制令牌"}
            </button>
            <button
              type="button"
              className={BTN}
              onClick={() => setCreated(null)}
            >
              我已保存，关闭
            </button>
          </div>
        </div>
      )}

      <form
        onSubmit={create}
        className="rounded-xl border border-line bg-surface p-4 shadow-card"
      >
        <p className="font-medium text-ink">新建令牌</p>
        <div className="mt-3 flex flex-wrap items-end gap-2">
          <label className="flex min-w-52 flex-1 flex-col gap-1 text-sm text-mute">
            名称（给谁用，如「合作方 A」）
            <input
              className={INPUT}
              value={name}
              onChange={(e) => setName(e.target.value)}
              maxLength={100}
              placeholder="partner-a"
            />
          </label>
          <label className="flex w-44 flex-col gap-1 text-sm text-mute">
            限速（次/分钟，空 = 默认）
            <input
              className={INPUT}
              value={rate}
              onChange={(e) => setRate(e.target.value)}
              inputMode="numeric"
              placeholder="600"
            />
          </label>
          <button
            type="submit"
            disabled={creating}
            className="inline-flex min-h-11 items-center rounded-lg bg-accent px-4 text-sm font-medium text-on-accent transition-colors hover:bg-accent-hover disabled:opacity-50"
          >
            {creating ? "创建中…" : "创建令牌"}
          </button>
        </div>
        {createError && (
          <p className="mt-2 text-sm text-warn-ink">{createError}</p>
        )}
      </form>

      {items.length === 0 ? (
        <div className="rounded-xl border border-line bg-surface p-8 text-center shadow-card">
          <p className="text-ink-2">还没有 API 令牌</p>
          <p className="mt-2 text-sm text-mute">
            用上面的表单创建第一个。是否强制要求令牌在「设置 → 公开 API」
            里开关；未开启强制时，带有效令牌的请求也会被计数。
          </p>
        </div>
      ) : (
        <div className="space-y-3">
          {items.map((item) => (
            <div
              key={item.id}
              className="rounded-xl border border-line bg-surface p-4 shadow-card"
            >
              <div className="flex flex-wrap items-start justify-between gap-3">
                <div className="min-w-0 flex-1">
                  <p className="flex flex-wrap items-center gap-2 font-medium wrap-anywhere text-ink">
                    {item.name}
                    <span
                      className={`rounded-md px-2 py-0.5 text-xs font-medium ${
                        item.enabled
                          ? "bg-accent-soft text-accent-ink"
                          : "bg-soft text-mute"
                      }`}
                    >
                      {item.enabled ? "启用" : "已禁用"}
                    </span>
                  </p>
                  <p className="mt-1 font-mono text-xs text-faint">
                    {item.prefix}…
                  </p>
                  <p className="mt-1 flex flex-wrap gap-x-3 gap-y-1 text-xs text-mute">
                    <span>调用 {formatCount(item.call_count)} 次</span>
                    <span>
                      最后使用：
                      {item.last_used_at
                        ? formatRelativeTime(item.last_used_at)
                        : "从未"}
                    </span>
                    <span>创建于 {formatDate(item.created_at)}</span>
                    <span>
                      限速：
                      {item.rate_per_min > 0
                        ? `${formatCount(item.rate_per_min)} 次/分钟`
                        : "不限"}
                    </span>
                  </p>
                  {failed === item.id && (
                    <p className="mt-1 text-xs text-warn-ink">
                      操作失败，请重试
                    </p>
                  )}
                  {editingRate === item.id && (
                    <span className="mt-2 flex flex-wrap items-center gap-2">
                      <input
                        className={`${INPUT} w-32`}
                        value={rateDraft}
                        onChange={(e) => setRateDraft(e.target.value)}
                        inputMode="numeric"
                        aria-label="新的限速（次/分钟，0 = 不限）"
                      />
                      <button
                        type="button"
                        className={BTN}
                        disabled={busy !== null}
                        onClick={() => {
                          const v = Number(rateDraft);
                          if (!Number.isInteger(v) || v < 0) {
                            setFailed(item.id);
                            return;
                          }
                          void runOn(item, "rate-api-token", {
                            ratePerMin: v,
                          });
                        }}
                      >
                        保存限速
                      </button>
                      <button
                        type="button"
                        className={BTN}
                        onClick={() => setEditingRate(null)}
                      >
                        取消
                      </button>
                    </span>
                  )}
                </div>
                <div className="flex shrink-0 flex-wrap gap-2">
                  <button
                    type="button"
                    className={BTN}
                    disabled={busy !== null}
                    onClick={() =>
                      void runOn(
                        item,
                        item.enabled ? "disable-api-token" : "enable-api-token"
                      )
                    }
                  >
                    {busy ===
                    `${item.enabled ? "disable" : "enable"}-api-token:${item.id}`
                      ? "处理中…"
                      : item.enabled
                        ? "禁用"
                        : "启用"}
                  </button>
                  <button
                    type="button"
                    className={BTN}
                    disabled={busy !== null}
                    onClick={() => {
                      setEditingRate(item.id);
                      setRateDraft(String(item.rate_per_min));
                    }}
                  >
                    改限速
                  </button>
                  <button
                    type="button"
                    className={BTN}
                    disabled={busy !== null}
                    onClick={() =>
                      void runOn(
                        item,
                        "delete-api-token",
                        undefined,
                        `删除令牌「${item.name}」？使用它的客户端会立即失效（要求令牌时返回 401），此操作不可恢复。`
                      )
                    }
                  >
                    删除
                  </button>
                </div>
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
