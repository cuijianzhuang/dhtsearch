"use client";

// The overview hero: the ingest pipeline as a live flow diagram —
//   DHT 网络 → 采样发现 → 元数据抓取 → 内容过滤 → 入库
// It replaces the old static funnel bars. Every moving element is
// driven by the deltas between the last two /api/admin-data polls
// (the dashboard polls every 8s and passes the previous payload in):
// no delta, no motion. The mapping is deliberately monotonic — a
// bigger delta never renders slower or sparser than a smaller one:
//
//   segment            rate source (per-poll delta)
//   DHT → 采样发现     Δ seen            (discoveries arriving)
//   采样 → 元数据抓取  Δ attempts        (fetched + prescreen_dropped
//                                         + timed_out + failed: work
//                                         leaving the fetch queue)
//   抓取 → 内容过滤    Δ fetched         (metadata successes)
//   过滤 → 入库        Δ passed          (fetched − filtered)
//
//   delta tier         dots   dot period
//   0                  0      — (stage visibly still)
//   1–9                1      3.6s
//   10–49              2      2.4s
//   50–199             3      1.6s
//   ≥200               3      0.9s
//
// Failure modes are first-class, because this pipeline has earned
// them: the crawler being disabled stops the whole flow and says so;
// discoveries arriving while the fetch stage completes nothing (and
// its queue is non-empty) flags 元数据抓取 as 疑似停滞 in warn colors
// with its outbound segments still; a failed poll freezes the dots
// (animation-play-state via .flow-paused) at the last measured state
// while the page's status line reports the failure. Dots are pure
// CSS keyframes on transform (see globals.css), re-tiered only when
// a poll lands — never per-frame React state — and are hidden under
// prefers-reduced-motion, leaving a static diagram with counters.

import type { AdminOverview } from "@/lib/admin";
import { formatCount } from "@/lib/format";
import AnimatedNumber from "./AnimatedNumber";

interface Tier {
  dots: number;
  seconds: number;
}

function flowTier(delta: number | null): Tier {
  if (delta === null || delta <= 0) return { dots: 0, seconds: 0 };
  if (delta < 10) return { dots: 1, seconds: 3.6 };
  if (delta < 50) return { dots: 2, seconds: 2.4 };
  if (delta < 200) return { dots: 3, seconds: 1.6 };
  return { dots: 3, seconds: 0.9 };
}

/** Non-negative delta between two cumulative counters; a counter that
 *  went backwards (process restart) reads as 0, not as negative flow. */
function delta(cur: number, prev: number | undefined): number | null {
  if (prev === undefined) return null;
  return Math.max(0, cur - prev);
}

function statsOf(o: AdminOverview | null | undefined) {
  const s = o?.stats ?? {};
  const fetched = s.fetched ?? 0;
  const filtered =
    (s.adult_filtered ?? 0) + (s.spam_filtered ?? 0) + (s.size_filtered ?? 0);
  return {
    seen: s.seen ?? 0,
    fetched,
    filtered,
    passed: Math.max(0, fetched - filtered),
    timedOut: o?.fetch?.timed_out ?? 0,
    failed: o?.fetch?.failed ?? 0,
    prescreen: o?.fetch?.prescreen_dropped ?? 0,
  };
}

export default function PipelineFlow({
  overview,
  prev,
  stale,
}: {
  overview: AdminOverview;
  /** The previous poll's payload; null until the first poll lands. */
  prev: AdminOverview | null;
  /** Latest poll failed: freeze the animation at the last state. */
  stale: boolean;
}) {
  const cur = statsOf(overview);
  const before = prev ? statsOf(prev) : null;

  const dSeen = before ? delta(cur.seen, before.seen) : null;
  const dFetched = before ? delta(cur.fetched, before.fetched) : null;
  const dFiltered = before ? delta(cur.filtered, before.filtered) : null;
  const dPassed = before ? delta(cur.passed, before.passed) : null;
  const dAttempts =
    before === null
      ? null
      : (delta(cur.fetched, before.fetched) ?? 0) +
        (delta(cur.prescreen, before.prescreen) ?? 0) +
        (delta(cur.timedOut, before.timedOut) ?? 0) +
        (delta(cur.failed, before.failed) ?? 0);

  const queueDepth = overview.fetch?.queue_depth ?? 0;
  const nodes = overview.crawler?.nodes ?? 0;
  const queuedNodes = overview.crawler?.queued_nodes ?? 0;
  const disabled = overview.crawler ? !overview.crawler.enabled : false;
  // The lived failure mode: hashes keep arriving, the fetch stage
  // completes nothing at all, and its queue is holding work.
  const fetchStalled =
    !disabled &&
    dSeen !== null &&
    dSeen > 0 &&
    dAttempts === 0 &&
    queueDepth > 0;

  const seg = (d: number | null): Tier => (disabled ? { dots: 0, seconds: 0 } : flowTier(d));

  return (
    <section className="rounded-xl border border-line bg-surface p-4 shadow-card">
      <div className="flex min-h-8 flex-wrap items-center justify-between gap-3">
        <h2 className="text-sm font-semibold text-ink">收录管线</h2>
        <span className="inline-flex items-center gap-1.5 text-xs text-faint">
          <span
            className={`h-2 w-2 rounded-full ${
              disabled
                ? "bg-faint"
                : fetchStalled
                  ? "bg-warn"
                  : "anim-live-dot bg-accent"
            }`}
          />
          {disabled
            ? "爬虫已禁用，管线停止"
            : fetchStalled
              ? "元数据抓取疑似停滞"
              : "管线运行中"}
          {stale && " · 数据已过期，动画冻结"}
        </span>
      </div>

      <div
        className={`mt-3 flex flex-col sm:flex-row sm:items-center ${
          stale ? "flow-paused" : ""
        }`}
      >
        <Stage label="DHT 网络" value={nodes} sub={`排队节点 ${formatCount(queuedNodes)}`} dim={disabled} />
        <Segment tier={seg(dSeen)} />
        <Stage label="采样发现" value={cur.seen} delta={dSeen} dim={disabled} />
        <Segment tier={seg(dAttempts)} />
        <Stage
          label="元数据抓取"
          value={cur.fetched}
          delta={dFetched}
          sub={`队列深度 ${formatCount(queueDepth)}`}
          warn={fetchStalled}
          dim={disabled}
        />
        <Segment tier={seg(dFetched)} />
        <Stage label="内容过滤" value={cur.filtered} delta={dFiltered} dim={disabled} />
        <Segment tier={seg(dPassed)} />
        <Stage label="入库" value={cur.passed} delta={dPassed} dim={disabled} />
      </div>

      <p className="mt-3 border-t border-line-2 pt-2 text-xs leading-relaxed text-faint">
        元数据超时 {formatCount(cur.timedOut)} · 元数据失败{" "}
        {formatCount(cur.failed)} · 早筛丢弃 {formatCount(cur.prescreen)}
        。管线计数为进程累计值，「本轮」为最近一次轮询间隔内的增量；
        库内总量另含订阅源直投与人工删除，不在此管线内。
      </p>
    </section>
  );
}

function Stage({
  label,
  value,
  delta: d,
  sub,
  warn,
  dim,
}: {
  label: string;
  value: number;
  /** Per-poll delta; null until a previous poll exists. */
  delta?: number | null;
  sub?: string;
  warn?: boolean;
  dim?: boolean;
}) {
  return (
    <div
      className={`min-w-0 rounded-lg border px-3 py-2.5 sm:flex-1 ${
        warn
          ? "border-warn/60 bg-warn/10"
          : "border-line bg-canvas/60"
      } ${dim ? "opacity-60" : ""}`}
    >
      <p className="text-[11px] font-medium text-mute">{label}</p>
      <p className="mt-0.5 text-lg leading-6 font-semibold tabular-nums text-ink">
        <AnimatedNumber value={value} />
      </p>
      {/* Both caption lines always render (sub may be empty) so every
          stage box in the row keeps the same height. A stage with no
          delta concept (DHT 网络) carries its sub on the first line. */}
      <p
        className={`mt-0.5 h-4 truncate text-[11px] tabular-nums ${
          warn ? "font-medium text-warn-ink" : "text-faint"
        }`}
      >
        {warn
          ? "队列有积压，本轮无进展"
          : d === undefined
            ? (sub ?? "")
            : d === null
              ? "等待下一轮数据"
              : d > 0
                ? `本轮 +${formatCount(d)}`
                : "本轮 +0"}
      </p>
      <p className="h-4 truncate text-[11px] tabular-nums text-faint">
        {d === undefined ? "" : (sub ?? "")}
      </p>
    </div>
  );
}

/** A rail with tier-driven dots. Renders both orientations (vertical
 *  on phones, horizontal from sm up); the dots exist only when the
 *  tier says the stage moved during the last poll interval. */
function Segment({ tier }: { tier: Tier }) {
  const dots = Array.from({ length: tier.dots }, (_, i) => i);
  const style = (i: number) => ({
    animationDuration: `${tier.seconds}s`,
    animationDelay: `${(-tier.seconds * i) / Math.max(1, tier.dots)}s`,
  });
  return (
    <>
      <div
        aria-hidden
        className="relative mx-auto h-10 w-6 shrink-0 overflow-hidden sm:hidden"
      >
        <div className="absolute top-0 bottom-0 left-1/2 w-px -translate-x-1/2 bg-input" />
        {dots.map((i) => (
          <span key={i} className="flow-dot flow-dot-y" style={style(i)} />
        ))}
      </div>
      <div
        aria-hidden
        className="relative hidden h-6 w-12 shrink-0 overflow-hidden sm:block lg:w-16"
      >
        <div className="absolute top-1/2 right-0 left-0 h-px -translate-y-1/2 bg-input" />
        {dots.map((i) => (
          <span key={i} className="flow-dot flow-dot-x" style={style(i)} />
        ))}
      </div>
    </>
  );
}
