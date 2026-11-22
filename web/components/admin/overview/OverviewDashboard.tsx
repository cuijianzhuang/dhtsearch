"use client";

// The admin overview dashboard: server-rendered first paint (the page
// passes its SSR fetch as initial props), then a client poll keeps it
// live. Polling goes through /api/admin-data (the token never reaches
// the browser), pauses while the tab is hidden, and updates state in
// place — no reload, no scroll jump, no flash.

import Link from "next/link";
import { useCallback, useEffect, useRef, useState } from "react";
import type {
  AdminListResponse,
  AdminOverview,
  AdminTorrentItem,
  AdminTrends,
  AdminTrendsRange,
} from "@/lib/admin";
import { formatCount, formatDate, formatRelativeTime, formatSize } from "@/lib/format";
import AnimatedNumber from "./AnimatedNumber";
import PipelineFlow from "./PipelineFlow";
import { CHART, DonutChart, MeterBar, TrendLine } from "./charts";

const POLL_MS = 8000;

/** The first re-poll after mount comes early: the SSR snapshot has
 *  no predecessor, so until one poll lands the pipeline hero can
 *  only show its idle state (no deltas exist yet). One refresh ~2s
 *  in gives it a real first interval and lights the flow up; the
 *  cadence then settles back to POLL_MS. */
const FIRST_POLL_MS = 2000;

/** Display labels for the fetch section's by_source rows, keyed by
 *  the API's source names. */
const FETCH_SOURCE_LABELS: Record<string, string> = {
  dht: "DHT 采样",
  scrape: "Tracker 刮削",
  feed: "订阅源",
};

async function fetchKind<T>(kind: string, range?: string): Promise<T> {
  const q = new URLSearchParams({ kind });
  if (range) q.set("range", range);
  const res = await fetch(`/api/admin-data?${q}`, { cache: "no-store" });
  if (!res.ok) throw new Error(`admin-data ${kind}: ${res.status}`);
  return (await res.json()) as T;
}

function pct(part: number, whole: number): string {
  if (whole <= 0) return "—";
  return `${((part / whole) * 100).toFixed(1)}%`;
}

export default function OverviewDashboard({
  initialOverview,
  initialTrends,
  initialLatest,
}: {
  initialOverview: AdminOverview;
  initialTrends: AdminTrends;
  initialLatest: AdminListResponse;
}) {
  const [overview, setOverview] = useState(initialOverview);
  // The previous poll's payload, kept so the pipeline hero can derive
  // per-interval deltas (the only honest source of "is it moving").
  // Seeded from the SSR payload, so the first poll already yields a
  // real interval instead of a fabricated one.
  const overviewRef = useRef(initialOverview);
  const [prevOverview, setPrevOverview] = useState<AdminOverview | null>(
    null
  );
  const [trends, setTrends] = useState(initialTrends);
  const [range, setRange] = useState<AdminTrendsRange>(initialTrends.range);
  const [latest, setLatest] = useState<AdminTorrentItem[]>(
    initialLatest.results
  );
  const [fresh, setFresh] = useState<ReadonlySet<string>>(new Set());
  const [updatedAt, setUpdatedAt] = useState<number | null>(null);
  const [stale, setStale] = useState(false);
  const [hidden, setHidden] = useState(false);

  const rangeRef = useRef(range);
  const inFlight = useRef(false);
  const prevHashes = useRef(
    new Set(initialLatest.results.map((it) => it.info_hash))
  );

  const applyLatest = useCallback((items: AdminTorrentItem[]) => {
    const prev = prevHashes.current;
    const arrived = items
      .filter((it) => !prev.has(it.info_hash))
      .map((it) => it.info_hash);
    prevHashes.current = new Set(items.map((it) => it.info_hash));
    setFresh(new Set(arrived));
    setLatest(items);
  }, []);

  const refreshAll = useCallback(async () => {
    if (inFlight.current) return;
    inFlight.current = true;
    try {
      const [o, t, l] = await Promise.all([
        fetchKind<AdminOverview>("overview"),
        fetchKind<AdminTrends>("trends", rangeRef.current),
        fetchKind<AdminListResponse>("latest"),
      ]);
      setPrevOverview(overviewRef.current);
      overviewRef.current = o;
      setOverview(o);
      setTrends(t);
      applyLatest(l.results);
      setUpdatedAt(Date.now());
      setStale(false);
    } catch {
      setStale(true);
    } finally {
      inFlight.current = false;
    }
  }, [applyLatest]);

  useEffect(() => {
    let timer = 0;
    const tick = async () => {
      if (!document.hidden) await refreshAll();
      timer = window.setTimeout(tick, POLL_MS);
    };
    timer = window.setTimeout(tick, FIRST_POLL_MS);
    const onVis = () => {
      setHidden(document.hidden);
      if (!document.hidden) void refreshAll();
    };
    document.addEventListener("visibilitychange", onVis);
    return () => {
      window.clearTimeout(timer);
      document.removeEventListener("visibilitychange", onVis);
    };
  }, [refreshAll]);

  const changeRange = (r: AdminTrendsRange) => {
    if (r === range) return;
    setRange(r);
    rangeRef.current = r;
    fetchKind<AdminTrends>("trends", r)
      .then(setTrends)
      .catch(() => setStale(true));
  };

  // Manual trending refresh: queues one fetch cycle on the backend
  // (the fetcher otherwise waits out its interval/backoff). The note
  // is stamped with the freshness state at click time and hides
  // itself as soon as a poll shows that state move — the refresh
  // landed, one way or the other.
  const [trendBusy, setTrendBusy] = useState(false);
  const [trendNote, setTrendNote] = useState<{
    text: string;
    updatedAt: number;
    failStreak: number;
  } | null>(null);

  async function onRefreshTrending() {
    if (trendBusy) return;
    setTrendBusy(true);
    const stamp = {
      updatedAt: overview.trending?.updated_at ?? 0,
      failStreak: overview.trending?.fail_streak ?? 0,
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
        setTrendNote({ text: "已触发刷新，完成后本页自动更新", ...stamp });
        void refreshAll();
      } else if (res.status === 409) {
        setTrendNote({ text: "已有榜单刷新在排队，请稍候", ...stamp });
      } else {
        setTrendNote({
          text: `触发刷新失败：${body.error ?? `HTTP ${res.status}`}`,
          ...stamp,
        });
      }
    } catch {
      setTrendNote({ text: "触发刷新失败：无法连接到后端", ...stamp });
    } finally {
      setTrendBusy(false);
    }
  }

  const o = overview;
  const s = o.stats ?? {};
  const fetched = s.fetched ?? 0;
  const adult = s.adult_filtered ?? 0;
  const spam = s.spam_filtered ?? 0;
  const size = s.size_filtered ?? 0;
  const blocked = s.blocked_filtered ?? 0;
  const filtered = adult + spam + size + blocked;
  const mod = o.moderation;

  return (
    <div>
      {/* Live status line */}
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-faint">
        <span className="inline-flex items-center gap-1.5">
          <span
            className={`h-2 w-2 rounded-full bg-accent ${hidden ? "" : "anim-live-dot"}`}
          />
          {hidden ? "标签页隐藏，已暂停刷新" : "实时 · 每 8 秒自动刷新"}
        </span>
        {updatedAt && (
          <span className="tabular-nums">
            更新于 {new Date(updatedAt).toLocaleTimeString("zh-CN")}
          </span>
        )}
        {stale && <span className="text-warn-ink">刷新失败，显示上次数据</span>}
        <button
          type="button"
          onClick={() => void refreshAll()}
          className="ml-auto inline-flex min-h-11 items-center rounded-md border border-input bg-soft px-3 text-xs text-ink-2 transition-colors hover:border-accent hover:text-accent-ink"
        >
          立即刷新
        </button>
      </div>

      {/* Hero: the ingest pipeline, live. First because it is the
          page's primary signal — is data moving, and where does it
          stop — and because every pipeline counter lives here and
          nowhere else (the blocks below never repeat them). */}
      <div className="mt-3">
        <PipelineFlow overview={o} prev={prevOverview} stale={stale} />
      </div>

      {/* KPI row. min-w-0 on every grid item below (here and in the
          panels): a grid item's automatic minimum size is its content's
          min-content, and a truncating (nowrap) descendant still
          contributes its full unwrapped text width there — one long
          torrent name in 最新入库 used to stretch the whole column to
          ~1900px on a 390px phone, and the browser then rendered the
          page at desktop width. min-w-0 caps the item at its track. */}
      <section className="mt-4 grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-6 [&>*]:min-w-0">
        <KpiCard label="种子总数" value={o.torrents} sub={`数据库 ${o.dialect}`} />
        <KpiCard label="今日新增" value={o.today} sub="今日 0 点以来" />
        <KpiCard
          label="未审核"
          value={o.pending_review}
          sub={`占总量 ${pct(o.pending_review, o.torrents)}`}
        />
        <KpiCard
          label="已审核"
          value={o.reviewed}
          sub={`占总量 ${pct(o.reviewed, o.torrents)}`}
        />
        <KpiCard label="已拉黑" value={o.blocked} sub="黑名单条目" />
        <Link
          href="/admin/reports"
          className="rounded-xl border border-line bg-surface p-4 shadow-card transition-colors hover:border-accent"
        >
          <p className="text-xs text-mute">待处理举报</p>
          <p className="mt-2 text-2xl font-semibold tabular-nums text-ink">
            <AnimatedNumber value={o.reports_pending ?? 0} />
          </p>
          <p className="mt-1 truncate text-xs text-faint">访客举报待处理</p>
        </Link>
      </section>

      {/* Trend + filter composition */}
      <div className="mt-4 grid gap-4 lg:grid-cols-3 [&>*]:min-w-0">
        <Panel
          title="入库趋势"
          className="lg:col-span-2"
          action={
            <div className="flex gap-1" role="group" aria-label="趋势范围">
              {(
                [
                  ["24h", "24 小时"],
                  ["14d", "14 天"],
                ] as [AdminTrendsRange, string][]
              ).map(([r, label]) => (
                <button
                  key={r}
                  type="button"
                  onClick={() => changeRange(r)}
                  aria-pressed={range === r}
                  className={`inline-flex min-h-11 items-center rounded-md border px-3 text-xs transition-colors ${
                    range === r
                      ? "border-accent bg-accent-soft font-medium text-accent-ink"
                      : "border-input bg-soft text-ink-2 hover:border-accent hover:text-accent-ink"
                  }`}
                >
                  {label}
                </button>
              ))}
            </div>
          }
        >
          <TrendLine
            buckets={trends.buckets}
            range={trends.range}
            bucketSeconds={trends.bucket_seconds}
          />
        </Panel>

        <Panel title="过滤构成">
          <DonutChart
            centerLabel="累计过滤"
            segments={[
              { label: "成人过滤", value: adult, color: CHART.danger },
              { label: "垃圾过滤", value: spam, color: CHART.warn },
              { label: "自定义屏蔽", value: blocked, color: CHART.neutral },
              { label: "体积过滤", value: size, color: CHART.priSoft },
            ]}
          />
          <p className="mt-3 mb-3 text-xs leading-relaxed text-faint">
            过滤率 {pct(filtered, fetched)}（按元数据抓取成功数计）；体积过滤是
            小于最小体积的种子，不属于内容问题。
          </p>
          {o.filter && (
            <div className="mt-auto border-t border-line-2 pt-2.5">
              {/* Each switch is one unbreakable item, so the row wraps
                  between switches at any card width — the old single
                  sentence could strand a lone fragment ("结）") on a
                  last line at the card's bottom edge. */}
              <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1 text-xs">
                <span className="text-faint">过滤开关</span>
                {(
                  [
                    ["总开关", o.filter.enabled],
                    ["成人", o.filter.adult_enabled],
                    ["垃圾", o.filter.spam_enabled],
                    ["太小", o.filter.too_small_enabled],
                  ] as [string, boolean][]
                ).map(([label, on]) => (
                  <span key={label} className="whitespace-nowrap text-mute">
                    {label}{" "}
                    <span className="font-medium text-ink-2">
                      {on ? "开" : "关"}
                    </span>
                  </span>
                ))}
              </div>
              <p className="mt-1.5 text-xs leading-relaxed text-faint">
                在
                <Link
                  href="/admin/settings"
                  className="text-accent-ink underline decoration-accent-line underline-offset-2"
                >
                  设置
                </Link>
                中调整；关掉的类别计数随之冻结。
              </p>
            </div>
          )}
        </Panel>
      </div>


      {/* Module status panels: secondary detail. Their 明细 lists no
          longer repeat numbers shown in the hero, the KPI row, the
          donut or the panel's own meters/ministats — each counter
          lives in exactly one visible place on this page. */}
      <p className="mt-6 mb-3 text-xs font-medium tracking-wide text-faint">
        模块状态
      </p>
      <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-4 [&>*]:min-w-0">
        {o.crawler && (
          <Panel title="爬虫状态">
            <StatusLine
              ok={o.crawler.enabled}
              text={o.crawler.enabled ? "运行中" : "已禁用"}
            />
            <MeterBar
              label="采样收获"
              value={o.crawler.harvested}
              max={o.crawler.sampled}
            />
            <DetailRows
              rows={[
                ["采样次数", formatCount(o.crawler.sampled)],
                ["采样出错", formatCount(o.crawler.sample_errors)],
                ["评分追踪节点", formatCount(o.crawler.tracked_nodes)],
                ["节点平均分", (o.crawler.avg_node_score ?? 0).toFixed(2)],
                ["冷却剔除节点", formatCount(o.crawler.banned_nodes)],
                ["启动恢复节点", formatCount(o.crawler.restored_nodes)],
              ]}
            />
          </Panel>
        )}

        {o.scraper && (
          <Panel title="Tracker 刮削">
            <StatusLine ok text="运行中" />
            <MeterBar
              label="队列深度"
              value={o.scraper.queue}
              max={o.scraper.queue + o.scraper.scraped}
              color={CHART.warn}
            />
            <MeterBar
              label="有 seeder"
              value={o.scraper.seeded}
              max={o.scraper.scraped}
              display={`${formatCount(o.scraper.seeded)} / ${formatCount(o.scraper.scraped)}`}
            />
            <DetailRows
              rows={[
                ["刮削出错", formatCount(o.scraper.scrape_errors)],
                ["丢弃", formatCount(o.scraper.dropped)],
                ["被挤出", formatCount(o.scraper.evicted)],
              ]}
            />
          </Panel>
        )}

        {o.fetch?.by_source && (
          <Panel title="抓取来源">
            <div className="divide-y divide-line-2">
              {o.fetch.by_source.map((row) => (
                <div
                  key={row.source}
                  className="flex items-baseline justify-between gap-3 py-2 first:pt-0 last:pb-0"
                >
                  <span className="truncate text-sm text-mute">
                    {FETCH_SOURCE_LABELS[row.source] ?? row.source}
                  </span>
                  <span className="text-sm whitespace-nowrap tabular-nums text-ink-2">
                    成功 {formatCount(row.fetched)} / 尝试{" "}
                    {formatCount(row.attempts)}
                    <span className="ml-2 font-medium text-ink">
                      {pct(row.fetched, row.attempts)}
                    </span>
                  </span>
                </div>
              ))}
            </div>
            <p className="mt-2 text-xs leading-relaxed text-faint">
              元数据抓取按发现来源归因（累计）：哪个渠道真正产出种子，看
              成功率即可。
            </p>
            <DetailRows
              rows={o.fetch.by_source.flatMap((row) => {
                const label = FETCH_SOURCE_LABELS[row.source] ?? row.source;
                return [
                  [`${label} 超时`, formatCount(row.timed_out)],
                  [`${label} 失败`, formatCount(row.failed)],
                  [`${label} 早筛丢弃`, formatCount(row.prescreen_dropped)],
                ] as [string, string][];
              })}
            />
          </Panel>
        )}

        <Panel title="LLM 审核">
          {mod?.enabled ? (
            <>
              <div className="grid grid-cols-2 gap-x-4 gap-y-3">
                <MiniStat label="已审核" value={mod.reviewed} />
                <MiniStat label="待审核" value={mod.pending} />
                <MiniStat label="成人删除" value={mod.adult_removed} />
                <MiniStat label="垃圾删除" value={mod.spam_removed} />
              </div>
              {mod.errors > 0 && (
                <p className="mt-3 text-xs text-warn-ink">
                  审核出错 {formatCount(mod.errors)} 次，请检查接口配置
                </p>
              )}
            </>
          ) : (
            <>
              <StatusLine ok={false} text="未启用" />
              <p className="mt-2 text-sm leading-relaxed text-mute">
                未配置 LLM 审核：待审种子不会被自动审核。在
                <Link
                  href="/admin/settings"
                  className="text-accent-ink underline decoration-accent-line underline-offset-2"
                >
                  设置
                </Link>
                中填入 API 密钥并开启后生效。
              </p>
              <div className="mt-3">
                <MiniStat label="当前待审核" value={mod?.pending ?? o.pending_review} />
              </div>
            </>
          )}
          <DetailRows
            rows={[["审核出错", formatCount(mod?.errors)]]}
          />
        </Panel>

        <Panel
          title="榜单与系统"
          action={
            o.trending ? (
              <button
                type="button"
                onClick={() => void onRefreshTrending()}
                disabled={trendBusy}
                title="立即向豆瓣刷新一轮榜单，不必等刷新周期"
                className="inline-flex min-h-11 items-center rounded-lg border border-input bg-soft px-3 text-xs text-ink-2 transition-colors hover:border-accent hover:text-accent-ink disabled:opacity-50"
              >
                {trendBusy ? "触发中…" : "立即刷新"}
              </button>
            ) : undefined
          }
        >
          {o.trending ? (
            <>
              <div className="grid grid-cols-2 gap-x-4 gap-y-3">
                <MiniStat label="欧美电影" value={o.trending.movies} />
                <MiniStat label="美剧" value={o.trending.tv} />
                <MiniStat label="日剧" value={o.trending.tv_jp} />
                <MiniStat label="韩剧" value={o.trending.tv_kr} />
              </div>
              <p className="mt-3 text-xs text-faint">
                榜单更新于{" "}
                {o.trending.updated_at
                  ? formatDate(o.trending.updated_at)
                  : "-"}
              </p>
              {trendNote &&
                trendNote.updatedAt === o.trending.updated_at &&
                trendNote.failStreak === (o.trending.fail_streak ?? 0) && (
                  <p className="mt-2 text-xs text-mute">{trendNote.text}</p>
                )}
              {o.trending.failing && (
                <p className="mt-2 rounded-lg border border-danger/50 bg-danger/10 px-3 py-2 text-xs leading-relaxed text-danger-ink">
                  抓取失败中（已连续 {o.trending.fail_streak ?? 0}{" "}
                  轮），正在展示上次榜单
                  {o.trending.last_success
                    ? `，上次成功 ${formatDate(o.trending.last_success)}`
                    : ""}
                  。
                </p>
              )}
            </>
          ) : (
            <p className="text-sm text-mute">热门榜单未启用</p>
          )}
          <DetailRows
            rows={[
              ["数据库后端", o.dialect],
              ...(o.runtime
                ? ([
                    ...(o.runtime.rss > 0
                      ? [["进程内存 RSS", formatSize(o.runtime.rss)] as [string, string]]
                      : []),
                    [
                      "Go 堆（已用 / 总量）",
                      `${formatSize(o.runtime.heap_alloc)} / ${formatSize(o.runtime.heap_sys)}`,
                    ],
                    ["Goroutine", formatCount(o.runtime.goroutines)],
                  ] as [string, string][])
                : []),
              ...(o.feeds
                ? Object.entries(o.feeds).map(
                    ([k, v]) => [`订阅源 ${k}`, formatCount(v)] as [string, string]
                  )
                : []),
            ]}
          />
        </Panel>
      </div>

      {/* Latest ingested stream: last, it is a feed, not a status. */}
      <div className="mt-4 [&>*]:min-w-0">
        <Panel
          title="最新入库"
          action={
            <span className="inline-flex items-center gap-1.5 text-xs text-faint">
              <span className="anim-live-dot h-1.5 w-1.5 rounded-full bg-accent" />
              实时
            </span>
          }
        >
          {latest.length === 0 ? (
            <p className="py-6 text-center text-sm text-faint">暂无数据</p>
          ) : (
            <ul className="-my-1 sm:grid sm:grid-cols-2 sm:gap-x-8">
              {latest.map((it) => (
                <li
                  key={it.info_hash}
                  className={fresh.has(it.info_hash) ? "anim-rise-in" : undefined}
                >
                  <Link
                    href={`/admin/torrents/${it.info_hash}`}
                    className="block py-2"
                  >
                    <span className="block truncate text-sm text-ink-2 transition-colors hover:text-accent-ink">
                      {it.name || it.raw_name}
                    </span>
                    <span className="mt-0.5 block text-xs tabular-nums text-faint">
                      {formatRelativeTime(it.created_at)} ·{" "}
                      {formatSize(it.total_size)} · {it.file_count} 个文件
                    </span>
                  </Link>
                </li>
              ))}
            </ul>
          )}
        </Panel>
      </div>
    </div>
  );
}

function KpiCard({
  label,
  value,
  sub,
}: {
  label: string;
  value: number;
  sub?: string;
}) {
  return (
    <div className="rounded-xl border border-line bg-surface p-4 shadow-card">
      <p className="text-xs text-mute">{label}</p>
      <p className="mt-2 text-2xl font-semibold tabular-nums text-ink">
        <AnimatedNumber value={value} />
      </p>
      {sub && <p className="mt-1 truncate text-xs text-faint">{sub}</p>}
    </div>
  );
}

function Panel({
  title,
  action,
  className,
  children,
}: {
  title: string;
  action?: React.ReactNode;
  className?: string;
  children: React.ReactNode;
}) {
  return (
    <section
      className={`flex flex-col rounded-xl border border-line bg-surface p-4 shadow-card ${className ?? ""}`}
    >
      <div className="flex min-h-8 items-center justify-between gap-3">
        <h2 className="text-sm font-semibold text-ink">{title}</h2>
        {action}
      </div>
      <div className="mt-2 flex min-h-0 flex-1 flex-col">{children}</div>
    </section>
  );
}

function StatusLine({ ok, text }: { ok: boolean; text: string }) {
  return (
    <div className="flex items-center gap-2 py-1">
      <span
        className={`h-2 w-2 rounded-full ${ok ? "bg-accent" : "bg-faint"}`}
      />
      <span className="text-sm font-medium text-ink-2">{text}</span>
    </div>
  );
}

function MiniStat({ label, value }: { label: string; value: number }) {
  return (
    <div>
      <p className="text-xs text-mute">{label}</p>
      <p className="mt-0.5 text-lg font-semibold tabular-nums text-ink">
        <AnimatedNumber value={value} />
      </p>
    </div>
  );
}

function DetailRows({ rows }: { rows: [string, string][] }) {
  return (
    <details className="mt-3 border-t border-line-2 pt-2">
      <summary className="inline-flex min-h-6 cursor-pointer items-center text-xs text-mute transition-colors hover:text-accent-ink">
        全部明细
      </summary>
      <div className="mt-1 divide-y divide-line-2">
        {rows.map(([label, value]) => (
          <div
            key={label}
            className="flex items-baseline justify-between gap-3 py-1.5"
          >
            <span className="truncate text-sm text-mute">{label}</span>
            <span className="text-sm tabular-nums text-ink-2">{value}</span>
          </div>
        ))}
      </div>
    </details>
  );
}
