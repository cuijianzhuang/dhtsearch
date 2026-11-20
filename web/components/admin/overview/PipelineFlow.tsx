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
//   过滤 → 入库        Δ stored          (net new rows landing in the
//                                         library: the stored_total
//                                         poll delta — 入库 displays
//                                         the library's actual current
//                                         size, not a counter
//                                         derivation)
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
//
// The track also reads as what it is — a spider crawling a network:
// each stage is itself a node (the ring described below), and one
// spider — the crawler itself — rides the track at the position
// the data says work currently reaches:
//   Δstored > 0            it travels the whole track, one anchor per
//                          quarter poll (a full traverse per poll
//                          interval), looping while items keep
//                          landing in the DB;
//   flow stops mid-track   it waits at the first stage that received
//                          nothing (frontier + 1), in warn colors
//                          when that stage is the diagnosed fetch
//                          stall, plain accent otherwise;
//   all deltas 0/disabled  it rests at the first node, muted.
// A stale poll freezes it mid-glide (its computed transform is
// pinned in place); reduced motion drops the leg wiggle and the
// transitions and steps it once per poll — the position is data, so
// it still updates. Movement is a transform transition stepped by
// state at poll cadence; there is no per-frame JS in this layer.
//
// The spider always faces the direction of data flow — head right
// on the horizontal track, head down the vertical one — including
// while it waits or rests. It never crawls backwards: any move to
// a lower anchor (the loop wrap at the end of a traverse, or the
// frontier retreating) is a fade cycle — fade out in place,
// reposition while invisible, fade back in, crawl forward again.
// And it alone breaks the teal discipline: a near-black spider
// with red eyes and a red abdomen marking (a soft light halo keeps
// it readable in dark appearance). On the diagnosed fetch stall
// the red intensifies — brighter, larger marking, plus a soft red
// ring — so the spider itself signals trouble alongside the stage's
// warn treatment; at rest it sits slightly transparent.
//
// The visual layer follows the approved design mockup: each stage
// is a large node ring — a hollow, luminous donut band with a
// hairline orbit and a fine inner ring, its centre open so the web
// shows through — with the stage name, the big count and the
// captions stacked under it, and the connector rails run ring to
// ring at ring height. The ring carries the stage's state in the
// same language: full saturation and glow while work reaches it,
// desaturated and dim at a zero inbound delta, warning-colored on
// the stalled fetch stage, muted across the board when the crawler
// is disabled. Behind the DHT ring a true spider web (radial
// threads and concentric arcs) spreads, and a node-mesh extends
// across the whole track, densest at the DHT end; both pulse at
// segment 1's tier rate and sit dim and still at a zero Δseen.
//
// Refinement pass: the rings read as nodes OF the web — a faint
// thread crosses each ring's open centre and a small node dot sits
// where the threads converge, lit with the stage's tone — and a
// soft radial ambience behind the whole track binds the hero into
// one atmosphere. A flowing stage's ring breathes (its glow pulses
// at the inbound segment's tier rate; idle rings stay calm). The
// spider sits ON its line with a soft shadow under it, and a
// hair-thin silk thread trails from it back to the previous ring
// while it travels or waits (never at rest, faded with it through
// the fade cycle). The whole hero is deliberately compact — rings
// step 60px phones / 84px sm / 80px lg with tightened stacks and
// chrome — so the overview's KPI row and trend chart stay in the
// first viewport instead of below the fold.

import {
  useEffect,
  useId,
  useRef,
  useState,
  useSyncExternalStore,
} from "react";
import type { AdminOverview } from "@/lib/admin";
import { formatCount } from "@/lib/format";
import AnimatedNumber from "./AnimatedNumber";

interface Tier {
  dots: number;
  seconds: number;
}

/** How a network node (a stage glyph, or the spider) is lit. */
type NodeTone = "accent" | "warn" | "faint";

/** Mirrors POLL_MS in OverviewDashboard: the dashboard's poll
 *  interval, which the spider's traverse is paced against. */
const POLL_MS = 8000;

let motionQueryCache: MediaQueryList | null = null;
function motionQuery(): MediaQueryList {
  if (!motionQueryCache) {
    motionQueryCache = window.matchMedia("(prefers-reduced-motion: reduce)");
  }
  return motionQueryCache;
}

function subscribeMotion(cb: () => void): () => void {
  const mq = motionQuery();
  mq.addEventListener("change", cb);
  return () => mq.removeEventListener("change", cb);
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
    // 入库 is the library itself: the current torrents row count
    // the overview payload carries as stored_total (the same Count
    // behind the 种子总数 KPI; torrents is the pre-rename field and
    // the fallback for an older server). The old fetched − filtered
    // derivation drifted above the real total — a re-fetch of a
    // known hash increments fetched on every upsert, and deletions
    // never decrement a counter.
    stored: o?.stored_total ?? o?.torrents ?? 0,
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
  // Δ stored is net new rows in the interval: deletions net against
  // additions, and the shared delta() clamp floors a net-negative
  // interval at 0 — the stage never reports negative flow.
  const dStored = before ? delta(cur.stored, before.stored) : null;
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

  // --- Spider layer -------------------------------------------------
  // The frontier is the run of consecutive segments, from segment 1,
  // whose delta is positive: work is currently getting that far. The
  // spider's anchor and color follow the position rule documented at
  // the top of this file; its stepping is state at poll cadence.
  const segDeltas = [dSeen, dAttempts, dFetched, dStored];
  let frontier = 0;
  for (const d of segDeltas) {
    if (d !== null && d > 0) frontier += 1;
    else break;
  }
  const allStill = segDeltas.every((d) => d === null || d <= 0);
  const traveling = !disabled && !allStill && dStored !== null && dStored > 0;
  const waiting = !disabled && !allStill && !traveling;

  const reducedMotion = useSyncExternalStore(
    subscribeMotion,
    () => motionQuery().matches,
    () => false,
  );

  const waitAnchor = Math.min(frontier + 1, 4);
  const spiderAlarm = waiting && fetchStalled;
  const spiderIdle = disabled || allStill;

  // The rendered position: an anchor, whether arriving there jumps,
  // and whether the spider is faded out mid fade-cycle. Forward
  // moves glide; ANY move to a lower anchor — the loop wrap at the
  // end of a traverse, or the frontier retreating — goes through
  // fadeTo instead: the spider never crawls backwards.
  const [spider, setSpider] = useState({ anchor: 0, jump: true, faded: false });
  const [fadeTo, setFadeTo] = useState<number | null>(null);
  const anchorRef = useRef(spider.anchor);
  useEffect(() => {
    anchorRef.current = spider.anchor;
  }, [spider.anchor]);

  // The fade cycle: fade out in place, reposition while invisible
  // (jump = no transform transition), fade back in, then release.
  // Frozen with everything else while the data is stale.
  useEffect(() => {
    if (fadeTo === null || stale) return;
    const t1 = setTimeout(
      () => setSpider((p) => ({ ...p, faded: true })),
      30,
    );
    const t2 = setTimeout(
      () => setSpider({ anchor: fadeTo, jump: true, faded: true }),
      400,
    );
    const t3 = setTimeout(
      () => setSpider((p) => ({ ...p, faded: false })),
      480,
    );
    const t4 = setTimeout(() => setFadeTo(null), 860);
    return () => {
      clearTimeout(t1);
      clearTimeout(t2);
      clearTimeout(t3);
      clearTimeout(t4);
    };
  }, [fadeTo, stale]);

  // Traveling: advance one anchor per quarter poll — a full traverse
  // per poll interval (one anchor per poll under reduced motion,
  // where the transitions are off anyway). At the last anchor, the
  // next tick starts the fade cycle back to the start node instead
  // of stepping. The stepping stops when the mode changes, the data
  // goes stale, or a fade cycle is running.
  useEffect(() => {
    if (!traveling || stale || fadeTo !== null) return;
    const id = setInterval(
      () => {
        if (anchorRef.current >= 4) setFadeTo(0);
        else
          setSpider((p) => ({
            anchor: p.anchor + 1,
            jump: false,
            faded: false,
          }));
      },
      reducedMotion ? POLL_MS : POLL_MS / 4,
    );
    return () => clearInterval(id);
  }, [traveling, stale, reducedMotion, fadeTo]);

  // Not traveling: settle at the position rule's anchor. Adjusted
  // during render (the store-the-previous-value pattern) whenever
  // the mode or the target changes — including settling again at a
  // previously visited anchor after a travel session moved it away.
  // Forward settles glide; a retreat is handed to the fade cycle.
  const settleTarget = traveling ? null : waiting ? waitAnchor : 0;
  const modeKey = settleTarget === null ? "travel" : `settle:${settleTarget}`;
  const [prevModeKey, setPrevModeKey] = useState(modeKey);
  if (modeKey !== prevModeKey) {
    setPrevModeKey(modeKey);
    if (settleTarget !== null) {
      if (settleTarget < spider.anchor) {
        setFadeTo(settleTarget);
      } else {
        setFadeTo(null);
        if (settleTarget !== spider.anchor || spider.faded) {
          setSpider({ anchor: settleTarget, jump: false, faded: false });
        }
      }
    }
  }

  const toneOf = (d: number | null): NodeTone =>
    !disabled && d !== null && d > 0 ? "accent" : "faint";

  return (
    <section className="overflow-hidden rounded-xl border border-line bg-surface p-3 shadow-card sm:p-4 lg:py-3">
      <div className="flex min-h-7 flex-wrap items-center justify-between gap-3 lg:min-h-5">
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
        className={`relative mt-2 overflow-hidden lg:mt-1.5 ${stale ? "flow-paused" : ""}`}
      >
        <div aria-hidden className="ambience-glow pointer-events-none absolute inset-0 z-0" />
        <MeshBackdrop tier={seg(dSeen)} />
        <div className="relative z-10 flex flex-col sm:flex-row sm:items-start">
          <Stage label="DHT 网络" value={nodes} sub={`排队节点 ${formatCount(queuedNodes)}`} dim={disabled} nodeTone={disabled ? "faint" : "accent"} backdropTier={seg(dSeen)} breatheTier={seg(dSeen)} />
          <Segment tier={seg(dSeen)} />
          <Stage label="采样发现" value={cur.seen} delta={dSeen} dim={disabled} nodeTone={toneOf(dSeen)} breatheTier={seg(dSeen)} />
          <Segment tier={seg(dAttempts)} />
          <Stage
            label="元数据抓取"
            value={cur.fetched}
            delta={dFetched}
            sub={`队列深度 ${formatCount(queueDepth)}`}
            warn={fetchStalled}
            dim={disabled}
            nodeTone={fetchStalled ? "warn" : toneOf(dAttempts)}
            breatheTier={seg(dAttempts)}
          />
          <Segment tier={seg(dFetched)} />
          <Stage label="内容过滤" value={cur.filtered} delta={dFiltered} dim={disabled} nodeTone={toneOf(dFetched)} breatheTier={seg(dFetched)} />
          <Segment tier={seg(dStored)} />
          <Stage label="入库" value={cur.stored} delta={dStored} dim={disabled} nodeTone={toneOf(dStored)} breatheTier={seg(dStored)} />
        </div>
        <SpiderLayer
          anchor={spider.anchor}
          alarm={spiderAlarm}
          idle={spiderIdle}
          faded={spider.faded}
          moving={traveling && !stale}
          jump={spider.jump}
          stale={stale}
        />
      </div>

      <p className="mt-2 border-t border-line-2 pt-1.5 text-[11px] leading-4 text-faint">
        元数据超时 {formatCount(cur.timedOut)} · 元数据失败{" "}
        {formatCount(cur.failed)} · 早筛丢弃 {formatCount(cur.prescreen)}
        。除「入库」外，管线计数为进程累计值，「本轮」为最近一次
        轮询间隔内的增量；「入库」为库内当前总数，与下方种子总数
        一致，其「本轮」为间隔内的净增行数。
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
  nodeTone = "faint",
  backdropTier,
  breatheTier,
}: {
  label: string;
  value: number;
  /** Per-poll delta; null until a previous poll exists. */
  delta?: number | null;
  sub?: string;
  warn?: boolean;
  dim?: boolean;
  /** How this stage's node ring is lit (its inbound flow state). */
  nodeTone?: NodeTone;
  /** First stage only: drives the spider-web backdrop's pulse. */
  backdropTier?: Tier;
  /** This stage's inbound segment tier: while work is flowing in,
   *  the ring's glow breathes at this rate (see .stage-ring-breathe
   *  in globals.css). Idle and warning rings stay calm. */
  breatheTier?: Tier;
}) {
  const breathing =
    !dim &&
    nodeTone === "accent" &&
    breatheTier !== undefined &&
    breatheTier.dots > 0;
  return (
    <div
      className={`relative flex min-w-0 flex-col items-center text-center sm:flex-1 ${
        dim ? "opacity-60" : ""
      }`}
    >
      {backdropTier && <WebBackdrop tier={backdropTier} />}
      {/* The stage as a network node: a large hollow ring (drawn by
          StageRingSvg; its size and palette live in globals.css)
          lit by the stage's tone — saturated and glowing in the
          accent while work reaches it, warning-colored on the
          stalled fetch stage, desaturated when nothing arrived
          this poll. */}
      <div
        aria-hidden
        className={`stage-ring relative z-10 ${
          nodeTone === "warn"
            ? "stage-ring-warn"
            : nodeTone === "faint"
              ? "stage-ring-faint"
              : ""
        } ${breathing ? "stage-ring-breathe" : ""}`}
        style={
          breathing
            ? { animationDuration: `${breatheTier.seconds}s` }
            : undefined
        }
      >
        <StageRingSvg />
      </div>
      <div className="relative z-10 flex flex-col items-center">
        <p className="mt-1 h-[15px] text-[13px] leading-[15px] font-semibold text-ink sm:mt-2 sm:h-5 sm:text-sm sm:leading-5 lg:mt-0.5 lg:h-4 lg:leading-4">
          {label}
        </p>
        <p className="mt-px h-5 text-base leading-5 font-bold tabular-nums text-ink sm:mt-0.5 sm:h-8 sm:text-2xl sm:leading-8 lg:mt-0 lg:h-7 lg:text-xl lg:leading-7">
          <AnimatedNumber value={value} />
        </p>
        {/* Both caption lines always render (sub may be empty) so
            every stage in the row keeps the same height — the mobile
            spider anchors are fixed steps of that height. A stage
            with no delta concept (DHT 网络) carries its sub on the
            first line. */}
        <p
          className={`mt-px h-3 max-w-full truncate text-[10px] leading-3 tabular-nums sm:mt-0.5 sm:h-4 sm:text-[11px] sm:leading-4 lg:mt-0 lg:h-[11px] lg:leading-[11px] ${
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
        <p className="h-3 max-w-full truncate text-[10px] leading-3 tabular-nums text-faint sm:h-4 sm:text-[11px] sm:leading-4 lg:h-[11px] lg:leading-[11px]">
          {d === undefined ? "" : (sub ?? "")}
        </p>
      </div>
    </div>
  );
}

/** The stage ring itself: a hollow donut in the mockup's language —
 *  a hairline orbit circle, a luminous band (a gradient along the
 *  stroke, bright at the top), a fine inner ring, and an open centre
 *  over a barely-there deep tint, so the web and mesh show through.
 *  The ring is a node OF that web: a faint thread crosses its centre
 *  on the connector axis and a small node dot sits where the
 *  threads converge, lit with the stage's tone (bright while work
 *  reaches it, faint when idle, warning-colored on the stall).
 *  All colors hang off the wrapper's --ring-c (see globals.css), so
 *  the warn/faint states recolor the whole drawing by variable. The
 *  gradient id is per-instance: five rings share the document, and a
 *  shared id would resolve every band to the first ring's gradient
 *  context. */
function StageRingSvg() {
  const gid = useId().replace(/:/g, "");
  return (
    <svg viewBox="0 0 96 96" className="block h-full w-full" fill="none">
      <defs>
        <linearGradient id={gid} x1="0" y1="0" x2="0.3" y2="1">
          <stop offset="0%" className="ring-stop-hi" />
          <stop offset="55%" className="ring-stop-mid" />
          <stop offset="100%" className="ring-stop-lo" />
        </linearGradient>
      </defs>
      <circle cx="48" cy="48" r="46.5" className="ring-orbit" strokeWidth="0.8" />
      <line x1="3" y1="48" x2="93" y2="48" className="ring-thread" strokeWidth="0.8" />
      <circle cx="48" cy="48" r="31.5" className="ring-core" />
      <circle
        cx="48"
        cy="48"
        r="39"
        stroke={`url(#${gid})`}
        strokeWidth="10.5"
      />
      <circle cx="48" cy="48" r="31.5" className="ring-inner" strokeWidth="0.8" />
      <circle cx="48" cy="48" r="3" className="ring-node-dot" />
    </svg>
  );
}

/** The spider web behind the DHT ring: fine radial threads and
 *  concentric arcs drawn as one static SVG, centred on the ring and
 *  reaching well past it — a radial fade mask dissolves the outer
 *  arcs around the second and third rings instead of clipping them.
 *  The threads are pale cyan-grey (--web-c in globals.css), never
 *  the full-strength accent. Its group opacity pulses at segment
 *  1's tier rate while discoveries arrive and sits dimmer and still
 *  at a zero Δseen; .flow-paused freezes the pulse with everything
 *  else when the data goes stale. */
function WebBackdrop({ tier }: { tier: Tier }) {
  const active = tier.dots > 0;
  return (
    <svg
      aria-hidden
      className="web-fade pointer-events-none absolute top-[30px] left-1/2 z-0 h-[300px] w-[300px] -translate-x-1/2 -translate-y-1/2 sm:top-[42px] sm:h-[440px] sm:w-[440px] lg:top-10 lg:h-[620px] lg:w-[620px]"
      viewBox="0 0 240 240"
      fill="none"
    >
      <g
        className={active ? "web-pulse" : undefined}
        style={
          active
            ? { animationDuration: `${tier.seconds}s` }
            : { opacity: 0.3 }
        }
        strokeWidth="0.5"
      >
        <g className="web-thread">
          <path d="M6 120 H234 M120 6 V234 M21.3 63 L218.7 177 M21.3 177 L218.7 63 M63 21.3 L177 218.7 M63 218.7 L177 21.3" />
          <circle cx="120" cy="120" r="26" />
          <circle cx="120" cy="120" r="44" />
          <circle cx="120" cy="120" r="62" />
          <circle cx="120" cy="120" r="80" />
          <circle cx="120" cy="120" r="98" />
          <circle cx="120" cy="120" r="114" />
          <g className="web-node">
            <circle cx="182" cy="120" r="1.3" />
            <circle cx="173.7" cy="151" r="1.3" />
            <circle cx="151" cy="173.7" r="1.3" />
            <circle cx="120" cy="182" r="1.3" />
            <circle cx="89" cy="173.7" r="1.3" />
            <circle cx="66.3" cy="151" r="1.3" />
            <circle cx="58" cy="120" r="1.3" />
            <circle cx="66.3" cy="89" r="1.3" />
            <circle cx="89" cy="66.3" r="1.3" />
            <circle cx="120" cy="58" r="1.3" />
            <circle cx="151" cy="66.3" r="1.3" />
            <circle cx="173.7" cy="89" r="1.3" />
          </g>
        </g>
      </g>
    </svg>
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
        className="relative mx-auto h-5 w-6 shrink-0 overflow-hidden sm:hidden"
      >
        <div className="absolute top-0 bottom-0 left-1/2 w-px -translate-x-1/2 bg-input" />
        {dots.map((i) => (
          <span key={i} className="flow-dot flow-dot-y" style={style(i)} />
        ))}
      </div>
      <div
        aria-hidden
        className="relative hidden h-[84px] w-12 shrink-0 overflow-hidden sm:block lg:h-[80px] lg:w-16"
      >
        <div className="absolute top-1/2 right-0 left-0 h-px -translate-y-1/2 bg-input" />
        {dots.map((i) => (
          <span key={i} className="flow-dot flow-dot-x" style={style(i)} />
        ))}
      </div>
    </>
  );
}

/** The node-mesh behind the whole track (sm and up; on phones the
 *  spider web behind the first ring carries the motif alone): a
 *  static SVG routing graph whose nodes thin out from the DHT end
 *  toward 入库 but never vanish — the graph runs the full width of
 *  the hero so the ambience stays perceptible behind the last ring
 *  too. It is sliced from the left so the dense end always shows.
 *  Its group opacity pulses at segment 1's tier rate while
 *  discoveries arrive; at a zero Δseen tier it renders dim and
 *  still; .flow-paused freezes the pulse when the data goes stale. */
function MeshBackdrop({ tier }: { tier: Tier }) {
  const active = tier.dots > 0;
  return (
    <svg
      aria-hidden
      className="pointer-events-none absolute inset-0 z-0 hidden h-full w-full sm:block"
      viewBox="0 0 1600 200"
      preserveAspectRatio="xMinYMid slice"
      fill="none"
    >
      <g
        className={active ? "mesh-pulse" : undefined}
        style={
          active
            ? { animationDuration: `${tier.seconds}s` }
            : { opacity: 0.3 }
        }
      >
        <path
          d="M30 60 L80 25 M30 60 L95 95 M80 25 L150 55 M80 25 L215 20 M95 95 L150 55 M95 95 L185 110 M150 55 L215 20 M150 55 L260 80 M185 110 L260 80 M185 110 L350 120 M215 20 L320 40 M260 80 L320 40 M260 80 L350 120 M260 80 L410 70 M320 40 L410 70 M350 120 L410 70 M410 70 L470 30 M410 70 L520 95 M470 30 L600 55 M520 95 L600 55 M600 55 L690 85 M690 85 L770 40 M690 85 L860 75 M770 40 L860 75 M860 75 L950 50 M950 50 L1050 90 M1050 90 L1150 45 M860 75 L1050 90 M1150 45 L1260 80 M1050 90 L1260 80 M1260 80 L1360 35 M1260 80 L1460 95 M1360 35 L1460 95 M1360 35 L1560 60 M1460 95 L1560 60"
          strokeWidth="1"
          className="mesh-lines"
        />
        <g>
          <circle cx="30" cy="60" r="3.2" className="mesh-node-hot" />
          <circle cx="80" cy="25" r="2" className="mesh-node" />
          <circle cx="95" cy="95" r="2" className="mesh-node" />
          <circle cx="150" cy="55" r="2.6" className="mesh-node-hot" />
          <circle cx="185" cy="110" r="1.8" className="mesh-node" />
          <circle cx="215" cy="20" r="1.8" className="mesh-node" />
          <circle cx="260" cy="80" r="3" className="mesh-node-hot" />
          <circle cx="320" cy="40" r="2" className="mesh-node" />
          <circle cx="350" cy="120" r="1.8" className="mesh-node" />
          <circle cx="410" cy="70" r="2.2" className="mesh-node" />
          <circle cx="470" cy="30" r="1.8" className="mesh-node" />
          <circle cx="520" cy="95" r="2" className="mesh-node" />
          <circle cx="600" cy="55" r="2.6" className="mesh-node-hot" />
          <circle cx="690" cy="85" r="2" className="mesh-node" />
          <circle cx="770" cy="40" r="1.8" className="mesh-node" />
          <circle cx="860" cy="75" r="2.2" className="mesh-node" />
          <circle cx="950" cy="50" r="1.8" className="mesh-node" />
          <circle cx="1050" cy="90" r="2" className="mesh-node" />
          <circle cx="1150" cy="45" r="1.8" className="mesh-node" />
          <circle cx="1260" cy="80" r="2.2" className="mesh-node" />
          <circle cx="1360" cy="35" r="1.8" className="mesh-node" />
          <circle cx="1460" cy="95" r="2" className="mesh-node" />
          <circle cx="1560" cy="60" r="1.8" className="mesh-node" />
        </g>
      </g>
    </svg>
  );
}

/** The spider layer: one spider riding the track. Its anchor comes
 *  from the position rule (see the file header); movement is the
 *  .spider-step transform transition in globals.css, stepped by the
 *  parent's state. A stale poll pins the computed transform inline,
 *  freezing the spider mid-glide until fresh data lands. */
function SpiderLayer({
  anchor,
  alarm,
  idle,
  faded,
  moving,
  jump,
  stale,
}: {
  anchor: number;
  /** Diagnosed fetch stall: the red accents intensify. */
  alarm: boolean;
  /** Resting (all still or crawler disabled): slightly transparent. */
  idle: boolean;
  /** Mid fade-cycle: invisible. */
  faded: boolean;
  /** Traveling: the legs wiggle. */
  moving: boolean;
  /** This step goes to a lower anchor: no backward glide. */
  jump: boolean;
  stale: boolean;
}) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    if (stale) {
      el.style.transform = getComputedStyle(el).transform;
      el.style.transition = "none";
    } else {
      el.style.transform = "";
      el.style.transition = "";
    }
  }, [stale]);
  // The silk thread trails exactly one pitch behind the spider —
  // drawn in layer space from the previous anchor to the spider's
  // own, so the layer's transform carries it with the glide. It
  // exists only while the spider travels or waits (never at rest,
  // never before the first ring) and fades with the spider through
  // the fade cycle, so repositioning never draws it backwards.
  const silk = !idle && anchor > 0;
  return (
    <div
      aria-hidden
      ref={ref}
      className={`spider-step spider-step-${anchor} ${
        jump ? "spider-jump" : ""
      } pointer-events-none absolute inset-0 z-20`}
    >
      {silk && (
        <span className={`spider-silk ${faded ? "spider-silk-faded" : ""}`} />
      )}
      <span
        className={`spider-body ${alarm ? "spider-alarm" : ""} ${
          idle ? "spider-idle" : ""
        } ${faded ? "spider-faded" : ""} ${moving ? "spider-moving" : ""}`}
      >
        <SpiderSvg />
      </span>
    </div>
  );
}

/** A simple top-down spider, drawn head-up (CSS rotates it into the
 *  flow direction per breakpoint): two near-black body segments and
 *  eight bent legs in the same ink, red eyes and a red abdomen
 *  marking. The legs are split into the two alternating gait groups
 *  (.spider-legs-a/-b) whose subtle rotation wiggle runs only while
 *  the spider travels. The soft ring (.spider-ring) stays invisible
 *  until the alarm state fades it in. Palette lives in globals.css. */
function SpiderSvg() {
  return (
    <svg
      viewBox="0 0 48 48"
      className="spider-svg block h-7 w-7 sm:h-11 sm:w-11"
      fill="none"
    >
      <circle cx="24" cy="24" r="21.5" className="spider-ring" />
      <g
        className="spider-ink"
        strokeWidth="2.2"
        strokeLinecap="round"
        fill="none"
      >
        <g className="spider-legs spider-legs-a">
          <path d="M19.5 15.5 L11 8 L6 10.5" />
          <path d="M29.5 21 L39.5 16.5 L44.5 20" />
          <path d="M18 26.5 L8 27.5 L3 32" />
          <path d="M29 32 L36.5 38 L40 43" />
        </g>
        <g className="spider-legs spider-legs-b">
          <path d="M28.5 15.5 L37 8 L42 10.5" />
          <path d="M18.5 21 L8.5 16.5 L3.5 20" />
          <path d="M30 26.5 L40 27.5 L45 32" />
          <path d="M19 32 L11.5 38 L8 43" />
        </g>
      </g>
      <ellipse cx="24" cy="29" rx="6.5" ry="8.5" className="spider-core" />
      <circle cx="24" cy="19" r="4.2" className="spider-core" />
      <ellipse cx="24" cy="31.5" rx="2.6" ry="3.8" className="spider-marking" />
      <g className="spider-eyes">
        <circle cx="22.2" cy="17.2" r="1.05" />
        <circle cx="25.8" cy="17.2" r="1.05" />
      </g>
    </svg>
  );
}
