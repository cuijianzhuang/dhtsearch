// Hand-rolled SVG/div charts for the admin overview dashboard.
//
// No chart library: the dashboard needs exactly two shapes (a donut
// and an area line) and a dependency would cost more bundle than
// these ~150 lines. (The ingest funnel that used to live here was
// replaced by the PipelineFlow hero, which shows the same counters
// as a live flow instead of static bars.) Every color is a CSS custom property (or a
// color-mix of one), so charts follow the operator's chosen theme
// color and both appearances — nothing here names a raw hue.

import { useEffect, useRef, useState } from "react";
import { formatCount } from "@/lib/format";

export const CHART = {
  pri: "var(--pri)",
  priSoft: "color-mix(in srgb, var(--pri) 55%, var(--card))",
  priFaint: "color-mix(in srgb, var(--pri) 30%, var(--card))",
  neutral: "color-mix(in srgb, var(--ink) 32%, var(--card))",
  neutralFaint: "color-mix(in srgb, var(--ink) 16%, var(--card))",
  danger: "var(--danger)",
  warn: "var(--warn)",
} as const;

export interface DonutSegment {
  label: string;
  value: number;
  color: string;
}

/** Donut of category shares with an HTML legend (values + percents). */
export function DonutChart({
  segments,
  centerLabel,
}: {
  segments: DonutSegment[];
  centerLabel: string;
}) {
  const total = segments.reduce((a, s) => a + s.value, 0);
  const R = 52;
  const C = 2 * Math.PI * R;
  let acc = 0;
  const arcs = segments.map((s) => {
    const frac = total > 0 ? s.value / total : 0;
    const arc = { ...s, dash: frac * C, offset: -acc * C };
    acc += frac;
    return arc;
  });
  return (
    <div className="flex flex-wrap items-center gap-x-5 gap-y-4">
      <div className="relative h-[120px] w-[120px] shrink-0">
        <svg viewBox="0 0 120 120" className="h-full w-full -rotate-90">
          <circle
            cx="60"
            cy="60"
            r={R}
            fill="none"
            stroke="var(--line-2)"
            strokeWidth="17"
          />
          {total > 0 &&
            arcs
              .filter((a) => a.value > 0)
              .map((a) => (
                <circle
                  key={a.label}
                  cx="60"
                  cy="60"
                  r={R}
                  fill="none"
                  stroke={a.color}
                  strokeWidth="17"
                  strokeDasharray={`${Math.max(0, a.dash - 1.5)} ${C - a.dash + 1.5}`}
                  strokeDashoffset={a.offset}
                  style={{
                    transition:
                      "stroke-dasharray .7s ease, stroke-dashoffset .7s ease",
                  }}
                />
              ))}
        </svg>
        <div className="absolute inset-0 flex flex-col items-center justify-center">
          <span className="text-xl font-semibold tabular-nums text-ink">
            {formatCount(total)}
          </span>
          <span className="text-[11px] text-faint">{centerLabel}</span>
        </div>
      </div>
      <ul className="min-w-40 flex-1 space-y-2">
        {segments.map((s) => (
          <li
            key={s.label}
            className="grid grid-cols-[0.625rem_1fr_auto_2.75rem] items-baseline gap-2 text-sm"
          >
            <span
              className="h-2.5 w-2.5 translate-y-px rounded-sm"
              style={{ background: s.color }}
            />
            <span className="whitespace-nowrap text-mute">{s.label}</span>
            <span className="font-medium tabular-nums text-ink-2">
              {formatCount(s.value)}
            </span>
            <span className="text-right text-xs tabular-nums text-faint">
              {total > 0 ? `${((s.value / total) * 100).toFixed(1)}%` : "—"}
            </span>
          </li>
        ))}
      </ul>
    </div>
  );
}

export interface TrendBucket {
  start: number;
  count: number;
}

/** Area line for the ingest series. Axis labels render as HTML around
 *  the SVG so the plot itself can stretch without distorting text.
 *
 *  The last bucket is normally still filling (the current hour, or
 *  today in the 14d view). Plotting its raw partial count — as a
 *  solid point OR as the ghost endpoint — makes the tail plunge
 *  toward zero and reads as an ingest collapse, so the ghost
 *  endpoint is the bucket's run-rate projection instead: partial
 *  count ÷ the elapsed fraction of the bucket. The projection is
 *  capped at 1.5× the peak full bucket (early in a bucket the
 *  division explodes; in the first 2% the ghost simply extends
 *  flat from the last full point), it counts toward the y scale
 *  so it never clips, and it stays out of the 峰值 label. The
 *  hover readout keeps showing the actual partial count, labelled
 *  as such. With zero elapsed time there is no ghost at all — the
 *  unfinished bucket is simply not plotted yet. */
export function TrendLine({
  buckets,
  range,
  bucketSeconds,
}: {
  buckets: TrendBucket[];
  range: "24h" | "14d";
  bucketSeconds?: number;
}) {
  const [hover, setHover] = useState<number | null>(null);
  // "Now" for the partial-bucket test, kept in state (render must stay
  // pure) and ticked on an interval so a page left open across an hour
  // boundary still retires the ghost segment on time.
  const [nowSec, setNowSec] = useState(() => Math.floor(Date.now() / 1000));
  useEffect(() => {
    const t = window.setInterval(
      () => setNowSec(Math.floor(Date.now() / 1000)),
      30_000
    );
    return () => window.clearInterval(t);
  }, []);
  const plotRef = useRef<HTMLDivElement>(null);
  const W = 640;
  const H = 180;
  const PAD = { l: 6, r: 6, t: 12, b: 6 };
  const iw = W - PAD.l - PAD.r;
  const ih = H - PAD.t - PAD.b;
  const n = buckets.length;
  const span = bucketSeconds ?? (range === "24h" ? 3600 : 86400);
  const partialLast = n > 1 && nowSec < buckets[n - 1].start + span;
  const solid = partialLast ? buckets.slice(0, -1) : buckets;
  const peak = Math.max(1, ...solid.map((b) => b.count));
  // Run-rate projection of the in-progress bucket (see the header
  // comment): null when the bucket has not started (zero elapsed —
  // no ghost), flat from the last full point in its first 2%, and
  // clamped to 1.5× the peak full bucket everywhere else.
  const elapsed = partialLast ? nowSec - buckets[n - 1].start : 0;
  const frac = span > 0 ? elapsed / span : 0;
  const ghostValue =
    partialLast && elapsed > 0
      ? frac < 0.02
        ? (solid[solid.length - 1]?.count ?? 0)
        : Math.min(buckets[n - 1].count / Math.max(frac, 0.001), peak * 1.5)
      : null;
  // The y scale must contain the projection so it never clips;
  // the 峰值 label below keeps reporting the full-bucket peak.
  const max = ghostValue !== null ? Math.max(peak, ghostValue) : peak;
  const x = (i: number) => PAD.l + (n <= 1 ? iw / 2 : (i * iw) / (n - 1));
  const y = (v: number) => Math.max(PAD.t, PAD.t + ih - (v / max) * ih);
  const solidPts = solid.map((b, i) => [x(i), y(b.count)] as const);
  const ghostPt =
    ghostValue !== null ? ([x(n - 1), y(ghostValue)] as const) : null;
  const lastSolid = solidPts[solidPts.length - 1];
  const lineOf = (pts: readonly (readonly [number, number])[]) =>
    pts
      .map((p, i) => `${i === 0 ? "M" : "L"}${p[0].toFixed(1)},${p[1].toFixed(1)}`)
      .join(" ");
  const solidLine = lineOf(solidPts);
  const solidArea =
    solidPts.length > 0
      ? `${solidLine} L${lastSolid[0].toFixed(1)},${PAD.t + ih} L${solidPts[0][0].toFixed(1)},${PAD.t + ih} Z`
      : "";
  const ghostLine =
    ghostPt && lastSolid
      ? `M${lastSolid[0].toFixed(1)},${lastSolid[1].toFixed(1)} L${ghostPt[0].toFixed(1)},${ghostPt[1].toFixed(1)}`
      : "";
  const ghostArea =
    ghostPt && lastSolid
      ? `${ghostLine} L${ghostPt[0].toFixed(1)},${PAD.t + ih} L${lastSolid[0].toFixed(1)},${PAD.t + ih} Z`
      : "";
  const fmt = (ts: number) => {
    const d = new Date(ts * 1000);
    if (range === "24h") {
      return `${String(d.getHours()).padStart(2, "0")}:00`;
    }
    return `${d.getMonth() + 1}/${d.getDate()}`;
  };
  const fmtFull = (ts: number) => {
    const d = new Date(ts * 1000);
    const md = `${d.getMonth() + 1}/${d.getDate()}`;
    return range === "24h"
      ? `${md} ${String(d.getHours()).padStart(2, "0")}:00`
      : md;
  };
  const labelIdx = [
    ...new Set(
      [0, 1, 2, 3, 4].map((k) => Math.round((k * (n - 1)) / 4))
    ),
  ].filter((i) => i >= 0 && i < n);
  const allZero = solid.length > 0 && solid.every((b) => b.count === 0);
  const TICKS = [1, 2 / 3, 1 / 3, 0];
  const tickTop = (f: number) => ((PAD.t + ih * (1 - f)) / H) * 100;
  const pick = (clientX: number) => {
    const el = plotRef.current;
    if (!el || n === 0) return;
    const r = el.getBoundingClientRect();
    const vx = ((clientX - r.left) / r.width) * W;
    const i = Math.round(((vx - PAD.l) / iw) * (n - 1));
    setHover(Math.max(0, Math.min(n - 1, i)));
  };
  const hoverBucket = hover !== null ? buckets[hover] : undefined;
  return (
    <div>
      <div className="flex items-baseline justify-between gap-3 text-xs text-faint">
        <span>
          峰值{" "}
          <span className="font-medium tabular-nums text-ink-2">
            {formatCount(peak)}
          </span>
          {range === "24h" ? " / 小时" : " / 天"}
        </span>
        {allZero ? (
          <span>该时段暂无入库</span>
        ) : partialLast ? (
          <span>
            虚线为{range === "24h" ? "当前小时" : "今日"}，进行中·按进度预估
          </span>
        ) : null}
      </div>
      <div className="mt-1 flex">
        {/* Y axis: four ticks aligned to the plot's own scale — the
            labels sit in a gutter so the SVG keeps its full width. */}
        <div className="relative h-44 w-9 shrink-0" aria-hidden="true">
          {TICKS.map((f) => (
            <span
              key={f}
              className={`absolute right-2 text-[11px] leading-none tabular-nums text-faint ${
                f === 1
                  ? ""
                  : f === 0
                    ? "-translate-y-full"
                    : "-translate-y-1/2"
              }`}
              style={{ top: `${tickTop(f)}%` }}
            >
              {formatCount(Math.round(max * f))}
            </span>
          ))}
        </div>
        <div className="min-w-0 flex-1">
          <div
            ref={plotRef}
            className="relative"
            onMouseMove={(e) => pick(e.clientX)}
            onMouseLeave={() => setHover(null)}
            onTouchStart={(e) => pick(e.touches[0].clientX)}
            onTouchMove={(e) => pick(e.touches[0].clientX)}
            onTouchEnd={() => setHover(null)}
          >
            <svg
              viewBox={`0 0 ${W} ${H}`}
              className="block h-44 w-full"
              preserveAspectRatio="none"
              aria-hidden="true"
            >
              {[1 / 3, 2 / 3].map((f) => (
                <line
                  key={f}
                  x1={PAD.l}
                  x2={W - PAD.r}
                  y1={PAD.t + ih * (1 - f)}
                  y2={PAD.t + ih * (1 - f)}
                  stroke="var(--line)"
                  strokeWidth="1"
                  vectorEffect="non-scaling-stroke"
                />
              ))}
              <line
                x1={PAD.l}
                x2={W - PAD.r}
                y1={PAD.t + ih}
                y2={PAD.t + ih}
                stroke="var(--line-2)"
                strokeWidth="1"
                vectorEffect="non-scaling-stroke"
              />
              {solidArea && (
                <path
                  d={solidArea}
                  style={{
                    fill: "color-mix(in srgb, var(--pri) 16%, transparent)",
                  }}
                />
              )}
              {ghostArea && (
                <path
                  d={ghostArea}
                  style={{
                    fill: "color-mix(in srgb, var(--pri) 7%, transparent)",
                  }}
                />
              )}
              {solidLine && (
                <path
                  d={solidLine}
                  fill="none"
                  stroke="var(--pri)"
                  strokeWidth="2"
                  strokeLinejoin="round"
                  strokeLinecap="round"
                  vectorEffect="non-scaling-stroke"
                />
              )}
              {ghostLine && (
                <path
                  d={ghostLine}
                  fill="none"
                  stroke="var(--pri)"
                  strokeWidth="2"
                  strokeDasharray="5 4"
                  strokeLinecap="round"
                  opacity="0.55"
                  vectorEffect="non-scaling-stroke"
                />
              )}
              {lastSolid && (
                <circle cx={lastSolid[0]} cy={lastSolid[1]} r="3.5" fill="var(--pri)" />
              )}
              {ghostPt && (
                <circle
                  cx={ghostPt[0]}
                  cy={ghostPt[1]}
                  r="3.5"
                  fill="var(--card)"
                  stroke="var(--pri)"
                  strokeWidth="2"
                  opacity="0.7"
                  vectorEffect="non-scaling-stroke"
                />
              )}
            </svg>
            {hoverBucket && hover !== null && (
              <>
                <div
                  className="pointer-events-none absolute inset-y-0 w-px"
                  style={{
                    left: `${(x(hover) / W) * 100}%`,
                    background: "var(--pri-line)",
                  }}
                />
                <div
                  className="pointer-events-none absolute h-2 w-2 -translate-x-1/2 -translate-y-1/2 rounded-full border-2 border-[var(--pri)] bg-[var(--card)]"
                  style={{
                    left: `${(x(hover) / W) * 100}%`,
                    top: `${(y(
                      partialLast && hover === n - 1 && ghostValue !== null
                        ? ghostValue
                        : hoverBucket.count
                    ) / H) * 100}%`,
                  }}
                />
                <div
                  className="pointer-events-none absolute top-0 -translate-x-1/2 rounded-md border border-line bg-surface px-2 py-1 whitespace-nowrap shadow-card"
                  style={{
                    left: `${Math.max(13, Math.min(87, (x(hover) / W) * 100))}%`,
                  }}
                >
                  <span className="text-[11px] tabular-nums text-ink-2">
                    {fmtFull(hoverBucket.start)} ·{" "}
                    {partialLast && hover === n - 1
                      ? ghostValue !== null
                        ? `当前实际 ${formatCount(hoverBucket.count)} · 预估 ${formatCount(Math.round(ghostValue))}`
                        : `${formatCount(hoverBucket.count)}（进行中）`
                      : formatCount(hoverBucket.count)}
                  </span>
                </div>
              </>
            )}
          </div>
          <div className="flex justify-between text-[11px] tabular-nums text-faint">
            {labelIdx.map((i) => (
              <span key={i}>
                {range === "24h" && i === n - 1 ? "现在" : fmt(buckets[i].start)}
              </span>
            ))}
          </div>
        </div>
      </div>
    </div>
  );
}

/** Slim labelled meter (queue depth, seeded ratio, ...). */
export function MeterBar({
  label,
  value,
  max,
  color = CHART.pri,
  display,
}: {
  label: string;
  value: number;
  max: number;
  color?: string;
  display?: string;
}) {
  const pct = max > 0 ? Math.min(100, (value / max) * 100) : 0;
  return (
    <div className="py-1.5">
      <div className="flex items-baseline justify-between gap-3">
        <span className="text-sm text-mute">{label}</span>
        <span className="text-sm font-medium tabular-nums text-ink-2">
          {display ?? formatCount(value)}
        </span>
      </div>
      <div className="mt-1.5 h-2 overflow-hidden rounded-full bg-soft">
        <div
          className="h-full w-full origin-left rounded-full transition-transform duration-700 ease-out"
          style={{ transform: `scaleX(${pct / 100})`, background: color }}
        />
      </div>
    </div>
  );
}
