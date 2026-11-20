// Hand-rolled SVG/div charts for the admin overview dashboard.
//
// No chart library: the dashboard needs exactly two shapes (a donut
// and an area line) and a dependency would cost more bundle than
// these ~150 lines. (The ingest funnel that used to live here was
// replaced by the PipelineFlow hero, which shows the same counters
// as a live flow instead of static bars.) Every color is a CSS custom property (or a
// color-mix of one), so charts follow the operator's chosen theme
// color and both appearances — nothing here names a raw hue.

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
          <li key={s.label} className="flex items-baseline gap-2 text-sm">
            <span
              className="h-2.5 w-2.5 shrink-0 translate-y-px rounded-sm"
              style={{ background: s.color }}
            />
            <span className="whitespace-nowrap text-mute">{s.label}</span>
            <span className="ml-auto font-medium tabular-nums text-ink-2">
              {formatCount(s.value)}
            </span>
            <span className="w-11 text-right text-xs tabular-nums text-faint">
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

/** Area line for the ingest series. X labels render as HTML under the
 *  SVG so the plot itself can stretch without distorting text. */
export function TrendLine({
  buckets,
  range,
}: {
  buckets: TrendBucket[];
  range: "24h" | "14d";
}) {
  const W = 640;
  const H = 180;
  const PAD = { l: 6, r: 6, t: 12, b: 6 };
  const iw = W - PAD.l - PAD.r;
  const ih = H - PAD.t - PAD.b;
  const max = Math.max(1, ...buckets.map((b) => b.count));
  const x = (i: number) =>
    PAD.l + (buckets.length <= 1 ? iw / 2 : (i * iw) / (buckets.length - 1));
  const y = (v: number) => PAD.t + ih - (v / max) * ih;
  const pts = buckets.map((b, i) => [x(i), y(b.count)] as const);
  const line = pts
    .map((p, i) => `${i === 0 ? "M" : "L"}${p[0].toFixed(1)},${p[1].toFixed(1)}`)
    .join(" ");
  const area = `${line} L${x(buckets.length - 1).toFixed(1)},${PAD.t + ih} L${x(0).toFixed(1)},${PAD.t + ih} Z`;
  const fmt = (ts: number) => {
    const d = new Date(ts * 1000);
    if (range === "24h") {
      return `${String(d.getHours()).padStart(2, "0")}:00`;
    }
    return `${d.getMonth() + 1}/${d.getDate()}`;
  };
  const mid = Math.floor(buckets.length / 2);
  const allZero = buckets.every((b) => b.count === 0);
  return (
    <div>
      <div className="flex items-baseline justify-between text-xs text-faint">
        <span>
          峰值{" "}
          <span className="font-medium tabular-nums text-ink-2">
            {formatCount(max)}
          </span>
          {range === "24h" ? " / 小时" : " / 天"}
        </span>
        {allZero && <span>该时段暂无入库</span>}
      </div>
      <svg
        viewBox={`0 0 ${W} ${H}`}
        className="mt-1 h-44 w-full"
        preserveAspectRatio="none"
        aria-hidden="true"
      >
        {[0.25, 0.5, 0.75].map((f) => (
          <line
            key={f}
            x1={PAD.l}
            x2={W - PAD.r}
            y1={PAD.t + ih * f}
            y2={PAD.t + ih * f}
            stroke="var(--line)"
            strokeWidth="1"
            vectorEffect="non-scaling-stroke"
          />
        ))}
        <path
          d={area}
          style={{ fill: "color-mix(in srgb, var(--pri) 16%, transparent)" }}
        />
        <path
          d={line}
          fill="none"
          stroke="var(--pri)"
          strokeWidth="2"
          strokeLinejoin="round"
          strokeLinecap="round"
          vectorEffect="non-scaling-stroke"
        />
        {pts.length > 0 && (
          <circle
            cx={pts[pts.length - 1][0]}
            cy={pts[pts.length - 1][1]}
            r="3.5"
            fill="var(--pri)"
          />
        )}
      </svg>
      <div className="flex justify-between text-[11px] tabular-nums text-faint">
        <span>{buckets.length > 0 ? fmt(buckets[0].start) : ""}</span>
        <span>{buckets.length > 0 ? fmt(buckets[mid].start) : ""}</span>
        <span>{range === "24h" ? "现在" : buckets.length > 0 ? fmt(buckets[buckets.length - 1].start) : ""}</span>
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
