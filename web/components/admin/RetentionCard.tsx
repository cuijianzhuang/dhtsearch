"use client";

// Data-retention card (settings → 数据保留, above the form).
//
// The point of this card is the number at its centre: how many
// torrents are eligible for deletion under the settings as they
// stand right now — the blast radius to read BEFORE enabling the
// automatic runner below. Eligibility is the backend's health
// derivation (likely_dead: zero seeders and leechers across at
// least 3 consecutive scrapes spanning 48+ hours) plus the
// minimum-age gate; nothing on this card redefines it.
//
// 立即清理 fires one pass by hand. It is an explicit operator
// action, so it is not gated by the 自动清理 switch (that switch
// gates the schedule) — but it honours the minimum age like any
// pass, and the confirm step carries the eligible count so the
// second tap is never blind. Deletion is irreversible.

import { useEffect, useRef, useState } from "react";
import type { AdminRetentionStatus } from "@/lib/admin";
import { formatDate, formatExactCount } from "@/lib/format";

const POLL_ACTIVE_MS = 3_000;
const POLL_IDLE_MS = 30_000;

type PollResult =
  | { ok: true; state: AdminRetentionStatus }
  | { ok: false };

async function fetchRetention(): Promise<PollResult> {
  try {
    const res = await fetch(`/api/admin-data?kind=retention`, {
      cache: "no-store",
    });
    if (!res.ok) return { ok: false };
    return { ok: true, state: (await res.json()) as AdminRetentionStatus };
  } catch {
    return { ok: false };
  }
}

async function postRun(): Promise<{
  status: number;
  body: AdminRetentionStatus & { error?: string };
}> {
  const res = await fetch("/api/admin-action", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ kind: "run-retention" }),
  });
  const body = (await res.json().catch(() => ({}))) as AdminRetentionStatus & {
    error?: string;
  };
  return { status: res.status, body };
}

export default function RetentionCard({
  initial,
}: {
  initial: AdminRetentionStatus | null;
}) {
  const [data, setData] = useState<AdminRetentionStatus | null>(initial);
  const [confirming, setConfirming] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);
  const runningRef = useRef(false);
  useEffect(() => {
    runningRef.current = (data?.running ?? false) || submitting;
  }, [data, submitting]);

  // Two-tap confirm disarms itself after a few seconds.
  useEffect(() => {
    if (!confirming) return;
    const t = window.setTimeout(() => setConfirming(false), 5_000);
    return () => window.clearTimeout(t);
  }, [confirming]);

  useEffect(() => {
    let timer = 0;
    let stopped = false;
    const tick = async () => {
      if (!document.hidden) {
        const res = await fetchRetention();
        if (!stopped && res.ok) setData(res.state);
      }
      if (!stopped) {
        timer = window.setTimeout(
          tick,
          runningRef.current ? POLL_ACTIVE_MS : POLL_IDLE_MS,
        );
      }
    };
    timer = window.setTimeout(tick, POLL_IDLE_MS);
    return () => {
      stopped = true;
      window.clearTimeout(timer);
    };
  }, []);

  async function onRun() {
    if (submitting || data?.running) return;
    if (!confirming) {
      setConfirming(true);
      return;
    }
    setConfirming(false);
    setSubmitting(true);
    setNotice(null);
    try {
      const { status, body } = await postRun();
      if (status === 202) {
        setData(body);
      } else if (status === 409) {
        setNotice(body.error ?? "已有清理正在执行，请等它完成。");
        const res = await fetchRetention();
        if (res.ok) setData(res.state);
      } else {
        setNotice(`提交清理失败：${body.error ?? `HTTP ${status}`}`);
      }
    } catch {
      setNotice("提交清理失败：无法连接到后端。");
    } finally {
      setSubmitting(false);
    }
  }

  if (!data) {
    return (
      <section className="rounded-xl border border-line bg-surface p-4 shadow-card">
        <h2 className="text-base font-semibold">死种清理</h2>
        <p className="mt-1 text-sm text-mute">
          暂时读不到清理状态（后端不可达或版本过旧），稍后刷新页面重试。
        </p>
      </section>
    );
  }

  const running = data.running || submitting;
  const last = data.last_run;

  return (
    <section className="rounded-xl border border-line bg-surface p-4 shadow-card">
      <div className="flex flex-wrap items-center gap-3">
        <h2 className="text-base font-semibold">死种清理</h2>
        <div className="ml-auto flex items-center gap-2">
          {confirming && !running && (
            <button
              type="button"
              onClick={() => setConfirming(false)}
              className="inline-flex min-h-11 items-center rounded-lg px-3 text-sm text-mute transition-colors hover:text-ink"
            >
              取消
            </button>
          )}
          <button
            type="button"
            onClick={onRun}
            disabled={running || data.eligible_now === 0}
            title="立即执行一轮清理：删除当前符合条件的疑似死亡种子，不受自动清理开关影响"
            className={
              confirming
                ? "inline-flex min-h-11 items-center rounded-lg border border-danger bg-danger/15 px-4 text-sm font-semibold text-danger-ink transition-colors disabled:cursor-not-allowed disabled:opacity-50"
                : "inline-flex min-h-11 items-center rounded-lg border border-danger/60 px-4 text-sm font-medium text-danger-ink transition-colors hover:bg-danger/10 disabled:cursor-not-allowed disabled:opacity-50"
            }
          >
            {submitting
              ? "提交中…"
              : data.running
                ? "清理中…"
                : confirming
                  ? `确认清理这 ${formatExactCount(data.eligible_now)} 条？再次点击立即执行`
                  : "立即清理"}
          </button>
        </div>
      </div>

      <p className="mt-2 text-sm leading-relaxed text-mute">
        清理对象只有「疑似死亡」的种子：连续至少 3 次 Tracker
        刮削都是零做种、零下载，且这段观察跨度不少于 48
        小时，同时入库已超过最短保留天数。删除连同文件列表一并移除、不可恢复；黑名单不受影响。先看清下面的可清理数量，再决定是否在下方开启自动清理。
      </p>

      <dl className="mt-3 grid grid-cols-2 gap-3 text-sm sm:grid-cols-4">
        <div className="rounded-lg border border-line-2 bg-soft px-3 py-2">
          <dt className="text-mute">当前可清理</dt>
          <dd className="mt-0.5 text-base font-semibold">
            {formatExactCount(data.eligible_now)} 条
          </dd>
        </div>
        <div className="rounded-lg border border-line-2 bg-soft px-3 py-2">
          <dt className="text-mute">累计已清理</dt>
          <dd className="mt-0.5 text-base font-semibold">
            {formatExactCount(data.deleted_total)} 条
          </dd>
        </div>
        <div className="rounded-lg border border-line-2 bg-soft px-3 py-2">
          <dt className="text-mute">自动清理</dt>
          <dd className="mt-0.5 text-base font-semibold">
            {data.enabled ? `开 · 每 ${data.interval_h} 小时` : "关"}
          </dd>
        </div>
        <div className="rounded-lg border border-line-2 bg-soft px-3 py-2">
          <dt className="text-mute">最短保留</dt>
          <dd className="mt-0.5 text-base font-semibold">
            {data.min_age_days} 天
          </dd>
        </div>
      </dl>

      {(running || last) && (
        <div className="mt-3 rounded-lg border border-line-2 bg-soft px-3 py-2 text-sm leading-relaxed">
          {running ? (
            <p>
              <span className="inline-flex items-center gap-2 font-medium">
                <span className="h-2 w-2 animate-pulse rounded-full bg-danger" />
                清理中…
              </span>
              <span className="ml-2 text-mute">
                正在分批删除符合条件的种子，完成后本卡会显示本轮结果。
              </span>
            </p>
          ) : last ? (
            <p>
              <span className="font-medium">上次清理</span>
              <span className="ml-2 text-mute">
                {formatDate(last.at)} · 删除{" "}
                {formatExactCount(last.deleted)} 条 · 仍符合条件{" "}
                {formatExactCount(last.eligible_remaining)} 条
              </span>
            </p>
          ) : null}
        </div>
      )}

      {notice && <p className="mt-3 text-sm text-warn-ink">{notice}</p>}
    </section>
  );
}
