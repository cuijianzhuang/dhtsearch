"use client";

// Trending freshness card (settings → 趋势， above the form).
//
// The settings in this group only take effect on the fetcher's next
// cycle, and a fetcher stuck failing (Douban unreachable) otherwise
// looks exactly like a healthy one from this page. The card mirrors
// the freshness state the overview's 榜单 panel shows — same API
// fields from the overview payload: when the snapshot last changed,
// when a cycle last fully succeeded, and how many cycles in a row
// have failed — plus the same 立即刷新 trigger, so a recovered
// upstream does not have to wait out the interval or the backoff.

import { useEffect, useState } from "react";
import type { AdminOverview } from "@/lib/admin";
import { formatDate } from "@/lib/format";

type TrendingSection = NonNullable<AdminOverview["trending"]>;

const POLL_MS = 30_000;

async function fetchTrending(): Promise<TrendingSection | null | undefined> {
  try {
    const res = await fetch(`/api/admin-data?kind=overview`, {
      cache: "no-store",
    });
    if (!res.ok) return undefined;
    const body = (await res.json()) as AdminOverview;
    return body.trending ?? null;
  } catch {
    return undefined;
  }
}

export default function TrendingStatusCard({
  initial,
}: {
  initial: TrendingSection | null;
}) {
  const [data, setData] = useState<TrendingSection | null>(initial);
  const [busy, setBusy] = useState(false);
  const [note, setNote] = useState<{
    text: string;
    updatedAt: number;
    failStreak: number;
  } | null>(null);

  useEffect(() => {
    let timer = 0;
    let stopped = false;
    const tick = async () => {
      if (!document.hidden) {
        const fresh = await fetchTrending();
        if (!stopped && fresh !== undefined) setData(fresh);
      }
      if (!stopped) timer = window.setTimeout(tick, POLL_MS);
    };
    timer = window.setTimeout(tick, POLL_MS);
    return () => {
      stopped = true;
      window.clearTimeout(timer);
    };
  }, []);

  async function onRefresh() {
    if (busy) return;
    setBusy(true);
    const stamp = {
      updatedAt: data?.updated_at ?? 0,
      failStreak: data?.fail_streak ?? 0,
    };
    try {
      const res = await fetch("/api/admin-action", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ kind: "refresh-trending" }),
      });
      const body = (await res.json().catch(() => ({}))) as {
        error?: string;
      };
      if (res.status === 202) {
        setNote({ text: "已触发刷新，完成后自动更新", ...stamp });
        const fresh = await fetchTrending();
        if (fresh !== undefined) setData(fresh);
      } else if (res.status === 409) {
        setNote({ text: "已有榜单刷新在排队，请稍候", ...stamp });
      } else {
        setNote({
          text: `触发刷新失败：${body.error ?? `HTTP ${res.status}`}`,
          ...stamp,
        });
      }
    } catch {
      setNote({ text: "触发刷新失败：无法连接到后端", ...stamp });
    } finally {
      setBusy(false);
    }
  }

  if (!data) {
    return (
      <section className="rounded-xl border border-line bg-surface p-4 shadow-card">
        <h2 className="text-base font-semibold">榜单状态</h2>
        <p className="mt-1 text-sm text-mute">
          暂时读不到榜单状态（后端不可达或版本过旧），稍后刷新页面重试。
        </p>
      </section>
    );
  }

  const noteVisible =
    note &&
    note.updatedAt === data.updated_at &&
    note.failStreak === (data.fail_streak ?? 0);

  return (
    <section className="rounded-xl border border-line bg-surface p-4 shadow-card">
      <div className="flex flex-wrap items-center gap-3">
        <h2 className="text-base font-semibold">榜单状态</h2>
        <div className="ml-auto">
          <button
            type="button"
            onClick={() => void onRefresh()}
            disabled={busy}
            title="立即向豆瓣刷新一轮榜单，不必等刷新周期"
            className="inline-flex min-h-11 items-center rounded-lg border border-input bg-soft px-4 text-sm text-ink-2 transition-colors hover:border-accent hover:text-accent-ink disabled:opacity-50"
          >
            {busy ? "触发中…" : "立即刷新"}
          </button>
        </div>
      </div>

      <dl className="mt-3 grid grid-cols-2 gap-3 text-sm sm:grid-cols-4">
        <div className="rounded-lg border border-line-2 bg-soft px-3 py-2">
          <dt className="text-mute">榜单条数</dt>
          <dd className="mt-0.5 text-base font-semibold">
            {data.movies + data.tv + data.tv_jp + data.tv_kr} 条
          </dd>
        </div>
        <div className="rounded-lg border border-line-2 bg-soft px-3 py-2">
          <dt className="text-mute">榜单更新于</dt>
          <dd className="mt-0.5 text-base font-semibold">
            {data.updated_at ? formatDate(data.updated_at) : "尚无"}
          </dd>
        </div>
        <div className="rounded-lg border border-line-2 bg-soft px-3 py-2">
          <dt className="text-mute">上次全量成功</dt>
          <dd className="mt-0.5 text-base font-semibold">
            {data.last_success ? formatDate(data.last_success) : "本次运行尚无"}
          </dd>
        </div>
        <div className="rounded-lg border border-line-2 bg-soft px-3 py-2">
          <dt className="text-mute">连续失败</dt>
          <dd className="mt-0.5 text-base font-semibold">
            {data.fail_streak ?? 0} 轮
          </dd>
        </div>
      </dl>

      {data.failing && (
        <p className="mt-3 rounded-lg border border-danger/50 bg-danger/10 px-3 py-2 text-xs leading-relaxed text-danger-ink">
          抓取失败中（已连续 {data.fail_streak ?? 0}{" "}
          轮），首页正在展示上次榜单
          {data.last_success
            ? `，上次成功 ${formatDate(data.last_success)}`
            : ""}
          。失败会自动退避重试，也可立即刷新试一轮。
        </p>
      )}
      {noteVisible && <p className="mt-3 text-sm text-mute">{note.text}</p>}
    </section>
  );
}
