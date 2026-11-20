"use client";

import { useRef, useState } from "react";
import type {
  SearchResult,
  SimilarItem,
  TorrentFile,
  TorrentFilesResponse,
} from "@/lib/api";
import {
  commonDirPrefix,
  formatCount,
  formatRelativeTime,
  formatSize,
  formatSpan,
  isOlderThan,
  shortenDir,
} from "@/lib/format";
import CopyMagnetButton from "./CopyMagnetButton";
import ReportForm from "./ReportForm";
import { CATEGORIES } from "@/lib/search-ui";
import { highlightText } from "@/lib/highlight";

const CATEGORY_LABELS: Record<string, string> = Object.fromEntries(
  CATEGORIES.map((c) => [c.key, c.label])
);

const FILES_PAGE_SIZE = 100;

const pagerBtnCls =
  "inline-flex min-h-8 items-center rounded-md border border-input bg-soft px-3 text-xs text-ink-2 transition-colors hover:border-accent hover:text-accent-ink disabled:cursor-not-allowed disabled:opacity-50";

export default function ResultCard({
  result,
  trackers,
  selected,
  onToggleSelect,
  query = "",
}: {
  result: SearchResult;
  /** Announce URLs stamped into this result's magnet (site-wide list
   *  from the search response), shown in the detail area. */
  trackers: string[];
  selected: boolean;
  onToggleSelect: () => void;
  /** The search query, for keyword highlighting in the name/alias. */
  query?: string;
}) {
  const [expanded, setExpanded] = useState(false);
  // Similar torrents load lazily on first expand: null = not asked yet.
  const [similar, setSimilar] = useState<SimilarItem[] | null>(null);
  const [similarLoading, setSimilarLoading] = useState(false);

  function toggleExpanded() {
    const next = !expanded;
    setExpanded(next);
    if (next && similar === null && !similarLoading) {
      setSimilarLoading(true);
      fetch(`/api/similar?hash=${encodeURIComponent(result.info_hash)}`)
        .then((res) => (res.ok ? res.json() : { results: [] }))
        .then((data: { results?: SimilarItem[] }) =>
          setSimilar(data.results ?? [])
        )
        .catch(() => setSimilar([]))
        .finally(() => setSimilarLoading(false));
    }
  }
  // File-list paging. The search payload embeds exactly one page of
  // the stored list (the backend caps it), which doubles as page 1;
  // further pages load through the /api/files proxy and are cached by
  // page number. Paging follows the STORED entry count, not
  // file_count: the crawler stores at most 50 entries but records the
  // real count, so file_count can far exceed what exists to page
  // through. A full embedded page means there may be more stored
  // entries; the true stored total arrives with the first fetch.
  const embeddedFiles = result.files ?? [];
  const totalFiles = Math.max(result.file_count ?? 0, embeddedFiles.length);
  const [filePage, setFilePage] = useState(1);
  const [extraPages, setExtraPages] = useState<Record<number, TorrentFile[]>>(
    {}
  );
  const [storedTotal, setStoredTotal] = useState<number | null>(
    embeddedFiles.length < FILES_PAGE_SIZE ? embeddedFiles.length : null
  );
  const [filesLoading, setFilesLoading] = useState(false);

  // File-name filter. The embedded page only ever holds the
  // unfiltered first page, so a non-empty filter switches the list to
  // server-filtered pages (?q= on the files endpoint filters the
  // stored list before paging). Filtered pages/totals are cached
  // separately from the unfiltered ones; clearing the filter returns
  // to the embedded view untouched.
  const [filterInput, setFilterInput] = useState("");
  const [filterQuery, setFilterQuery] = useState("");
  const [filteredPages, setFilteredPages] = useState<
    Record<number, TorrentFile[]>
  >({});
  const [filteredTotal, setFilteredTotal] = useState<number | null>(null);
  const [storedTotalAll, setStoredTotalAll] = useState<number | null>(null);
  const filterTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const filtering = filterQuery.trim() !== "";

  const shownFiles = filtering
    ? (filteredPages[filePage] ?? [])
    : filePage === 1
      ? embeddedFiles
      : (extraPages[filePage] ?? []);
  const knownStored = storedTotal ?? embeddedFiles.length;
  const activeTotal = filtering ? (filteredTotal ?? 0) : knownStored;
  const totalFilePages = Math.max(
    1,
    Math.ceil(activeTotal / FILES_PAGE_SIZE)
  );
  // Computed over the first page, so the prefix doesn't shift while
  // paging through the rest.
  const root = commonDirPrefix(embeddedFiles.map((f) => f.path));

  function fetchFilesPage(p: number, q: string) {
    setFilesLoading(true);
    const params = new URLSearchParams({
      hash: result.info_hash,
      page: String(p),
      page_size: String(FILES_PAGE_SIZE),
    });
    if (q) params.set("q", q);
    fetch(`/api/files?${params.toString()}`)
      .then((res) => (res.ok ? res.json() : null))
      .then((data: TorrentFilesResponse | null) => {
        if (!data) return;
        if (q) {
          setFilteredTotal(data.total);
          setStoredTotalAll(data.total_all);
          if ((data.files ?? []).length === 0 && p > 1 && data.total > 0) {
            setFilePage(Math.max(1, Math.ceil(data.total / FILES_PAGE_SIZE)));
            return;
          }
          setFilteredPages((prev) => ({ ...prev, [p]: data.files ?? [] }));
          setFilePage(p);
          return;
        }
        setStoredTotal(data.total);
        if ((data.files ?? []).length === 0 && p > 1) {
          // The stored list ended earlier than the embedded page
          // suggested; stay on the last page that has entries.
          setFilePage(Math.max(1, Math.ceil(data.total / FILES_PAGE_SIZE)));
          return;
        }
        setExtraPages((prev) => ({ ...prev, [p]: data.files ?? [] }));
        setFilePage(p);
      })
      .catch(() => {})
      .finally(() => setFilesLoading(false));
  }

  function loadFilePage(p: number) {
    if (filtering) {
      if (filteredPages[p]) {
        setFilePage(p);
        return;
      }
      fetchFilesPage(p, filterQuery.trim());
      return;
    }
    if (p === 1 || extraPages[p]) {
      setFilePage(p);
      return;
    }
    fetchFilesPage(p, "");
  }

  function onFilterInput(value: string) {
    setFilterInput(value);
    if (filterTimer.current) clearTimeout(filterTimer.current);
    filterTimer.current = setTimeout(() => {
      const q = value.trim();
      setFilterQuery(value);
      setFilePage(1);
      setFilteredPages({});
      setFilteredTotal(null);
      if (q) fetchFilesPage(1, q);
    }, 300);
  }

  return (
    <div
      className={`rounded-xl border bg-surface/60 p-4 shadow-card transition-colors ${
        selected
          ? "border-accent bg-accent-soft"
          : "border-line hover:border-input"
      }`}
    >
      <div className="flex items-start justify-between gap-3">
        {/* Native checkbox for free keyboard/screen-reader semantics. The
            label's padding (undone by negative margins so layout is unchanged)
            grows the tap target to 44px for touch; -mt-2 + p-3 nets out to
            the mt-1 that aligns the box with the title's first line. */}
        <label className="-m-3 -mt-2 shrink-0 cursor-pointer p-3">
          <input
            type="checkbox"
            checked={selected}
            onChange={onToggleSelect}
            aria-label={`选择 ${result.name || "(未命名)"}`}
            className="block h-5 w-5 cursor-pointer accent-accent"
          />
        </label>
        <button
          onClick={toggleExpanded}
          className="min-w-0 flex-1 text-left"
          title={result.name}
        >
          {/* Release names carry their resolution, codec and group at the very
              end, so clamp to two lines rather than truncating to one — and
              drop the clamp entirely once the card is open. Phone lines hold
              far fewer characters, so they get a third line to compensate. */}
          <h3
            className={`text-sm font-medium wrap-anywhere text-accent-ink hover:text-accent-ink sm:text-base ${
              expanded ? "" : "line-clamp-3 sm:line-clamp-2"
            }`}
          >
            {highlightText(result.name || "(未命名)", query)}
          </h3>
          {/* The publisher's title, shown when it differs from the torrent
              name — usually the Chinese name a romaji release was found by. */}
          {result.alias && result.alias !== result.name && (
            <p
              className={`mt-1 text-xs wrap-anywhere text-mute ${
                expanded ? "" : "line-clamp-1"
              }`}
            >
              {highlightText(result.alias, query)}
            </p>
          )}
          {/* Meta row, ordered by what a scanner looks for first:
              what kind it is (category chip), how big (emphasised —
              the field download decisions turn on), how many files,
              how fresh. The backend computes the category for every
              result; the card used to drop it on the floor. */}
          <p className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-mute">
            <span className="rounded-md bg-soft px-1.5 py-0.5 text-[11px] font-medium text-ink-2">
              {CATEGORY_LABELS[result.category] ?? "其他"}
            </span>
            <span className="font-medium tabular-nums text-ink-2">
              {formatSize(result.total_size)}
            </span>
            <span className="tabular-nums">{formatCount(totalFiles)} 个文件</span>
            {/* Seeder badge only when a tracker has actually reported:
                an unknown count shows nothing rather than a fake 0. */}
            {result.seeders != null && (
              <span className="tabular-nums">做种 {formatCount(result.seeders)}</span>
            )}
            {result.health === "likely_dead" && (
              <span className="rounded-md bg-soft px-1.5 py-0.5 text-[11px] font-medium text-warn-ink">
                疑似死种
              </span>
            )}
            <span>收录于 {formatRelativeTime(result.created_at)}</span>
          </p>
        </button>
        <CopyMagnetButton magnet={result.magnet} />
      </div>

      {expanded && (
        <div className="mt-3 rounded-lg border border-line bg-canvas/60 p-3">
          {/* Swarm health from tracker scrapes. Rendered only when a
              scrape actually reported: no section at all beats a
              section of invented zeros. */}
          {result.scraped_at != null && (
            <div className="mb-3 border-b border-line-2 pb-2">
              <p className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-mute">
                <span>
                  做种{" "}
                  <span className="font-medium tabular-nums text-ink-2">
                    {formatCount(result.seeders)}
                  </span>
                </span>
                <span>
                  正在下载{" "}
                  <span className="font-medium tabular-nums text-ink-2">
                    {formatCount(result.leechers)}
                  </span>
                </span>
                <span>
                  已完成{" "}
                  <span className="font-medium tabular-nums text-ink-2">
                    {formatCount(result.completed)}
                  </span>
                </span>
                {(() => {
                  const stale = isOlderThan(result.scraped_at, 24 * 3600);
                  return (
                    <span className={stale ? "text-faint" : undefined}>
                      数据更新于 {formatRelativeTime(result.scraped_at)}
                      {stale && "（数据较旧）"}
                    </span>
                  );
                })()}
              </p>
              {result.health === "likely_dead" && (
                <p className="mt-2 flex flex-wrap items-center gap-2">
                  <span className="inline-flex items-center rounded-md bg-soft px-2 py-1 text-xs font-medium text-warn-ink">
                    疑似死种
                  </span>
                  <span className="text-xs text-mute">
                    连续 {formatCount(result.zero_count)} 次抓取均无做种
                    {result.zero_since != null &&
                      `，已持续 ${formatSpan(result.scraped_at - result.zero_since)}`}
                    。这只是概率判断，种子仍可能复活。
                  </span>
                </p>
              )}
              {result.health === "no_seed" && (
                <p className="mt-1 text-xs text-mute">
                  暂无完整做种源，但仍有人在下载，凑齐的希望还在。
                </p>
              )}
              {result.health === "suspect" && (
                <p className="mt-1 text-xs text-mute">
                  最近抓取未见做种，但记录还太短，暂不能判断是否已死。
                </p>
              )}
            </div>
          )}
          {totalFiles > 0 ? (
            <>
              <div className="mb-2 flex flex-wrap items-center gap-2">
                <input
                  type="search"
                  value={filterInput}
                  onChange={(e) => onFilterInput(e.target.value)}
                  placeholder="按文件名过滤…"
                  aria-label="按文件名过滤文件列表"
                  className="h-10 min-w-0 flex-1 rounded-lg border border-input bg-surface px-3 text-xs text-ink placeholder-faint outline-none focus:border-accent focus:ring-4 focus:ring-accent/15 sm:max-w-64"
                />
                {(filterInput.trim() !== "" || filtering) && (
                  <span className="text-xs tabular-nums text-mute">
                    共 {formatCount(storedTotalAll ?? knownStored)} 个文件
                    {filtering &&
                      ` · 筛出 ${formatCount(filteredTotal ?? 0)} 个`}
                  </span>
                )}
              </div>
              {/* The root line describes the unfiltered list's common
                  prefix; while filtering, each row shows its own
                  directory, so the line would only mislead. */}
              {root && !filtering && (
                <p
                  className="mb-2 line-clamp-2 border-b border-line-2 pb-2 font-mono text-[11px] wrap-anywhere text-mute"
                  title={root}
                >
                  <svg
                    viewBox="0 0 24 24"
                    fill="none"
                    stroke="currentColor"
                    strokeWidth="2"
                    strokeLinecap="round"
                    strokeLinejoin="round"
                    className="mr-1 inline h-3.5 w-3.5 align-[-2px] text-faint"
                    aria-hidden
                  >
                    <path d="M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2Z" />
                  </svg>
                  {root}
                </p>
              )}
              {shownFiles.length > 0 ? (
                <ul className="space-y-2 text-xs text-mute">
                  {shownFiles.map((f, i) => {
                    const rest = f.path.startsWith(root)
                      ? f.path.slice(root.length)
                      : f.path;
                    const cut = rest.lastIndexOf("/");
                    const dir = cut >= 0 ? rest.slice(0, cut + 1) : "";
                    const name = cut >= 0 ? rest.slice(cut + 1) : rest;
                    return (
                      <li key={i} className="flex items-baseline justify-between gap-3">
                        <span className="min-w-0 font-mono" title={f.path}>
                          {/* The directory is context, so it may truncate; the
                              file name is what tells two rows apart, so it wraps
                              in full on its own line. */}
                          {dir && (
                            <span className="block truncate text-faint">
                              {shortenDir(dir)}
                            </span>
                          )}
                          <span className="block wrap-anywhere text-ink-2">
                            {name}
                          </span>
                        </span>
                        <span className="shrink-0 tabular-nums text-mute">
                          {formatSize(f.size)}
                        </span>
                      </li>
                    );
                  })}
                </ul>
              ) : (
                <p className="text-xs text-mute">
                  {filesLoading
                    ? "正在加载…"
                    : filtering
                      ? `没有文件名包含「${filterQuery.trim()}」的文件`
                      : "暂无文件列表信息"}
                </p>
              )}
              {filesLoading && shownFiles.length > 0 && (
                <p className="pt-2 text-xs text-faint">正在加载…</p>
              )}
              {(totalFilePages > 1 ||
                (!filtering && storedTotal === null) ||
                (filtering && filteredTotal === null)) &&
                activeTotal > 0 && (
                  <div className="mt-3 flex flex-wrap items-center gap-3 border-t border-line-2 pt-2 text-xs">
                    <button
                      type="button"
                      className={pagerBtnCls}
                      disabled={filePage <= 1}
                      onClick={() => loadFilePage(filePage - 1)}
                    >
                      ← 上一页
                    </button>
                    <span className="text-mute">
                      {filtering
                        ? filteredTotal !== null
                          ? `第 ${filePage} / ${totalFilePages} 页 · 筛出 ${formatCount(filteredTotal)} 个`
                          : `第 ${filePage} 页`
                        : storedTotal !== null
                          ? `第 ${filePage} / ${totalFilePages} 页 · 共 ${formatCount(knownStored)} 个文件`
                          : `第 ${filePage} 页`}
                    </span>
                    <button
                      type="button"
                      className={pagerBtnCls}
                      disabled={
                        (filtering
                          ? filteredTotal !== null
                          : storedTotal !== null) && filePage >= totalFilePages
                      }
                      onClick={() => loadFilePage(filePage + 1)}
                    >
                      下一页 →
                    </button>
                  </div>
                )}
              {!filtering && storedTotal !== null && totalFiles > storedTotal && (
                <p className="pt-2 text-xs text-mute">
                  种子共 {formatCount(totalFiles)} 个文件，收录的文件列表为前{" "}
                  {formatCount(storedTotal)} 个
                </p>
              )}
            </>
          ) : (
            <p className="text-xs text-mute">暂无文件列表信息</p>
          )}
          <div className="mt-3 flex items-start gap-2 border-t border-line-2 pt-2">
            <p
              className="line-clamp-2 min-w-0 flex-1 font-mono text-[11px] leading-relaxed break-all text-faint"
              title={result.magnet}
            >
              {result.magnet}
            </p>
            <CopyMagnetButton magnet={result.magnet} />
          </div>

          {trackers.length > 0 && (
            <div className="mt-3 border-t border-line-2 pt-2">
              <p className="text-[11px] font-medium text-mute">
                Tracker（{trackers.length}，已写入磁力链接）
              </p>
              <ul className="mt-1 max-h-28 space-y-1 overflow-y-auto font-mono text-[11px] break-all text-faint">
                {trackers.map((tr) => (
                  <li key={tr}>{tr}</li>
                ))}
              </ul>
            </div>
          )}

          <div className="mt-3 border-t border-line-2 pt-2">
            <p className="text-[11px] font-medium text-mute">相似资源</p>
            {similarLoading ? (
              <p className="mt-1 text-xs text-faint">正在查找相似资源…</p>
            ) : similar && similar.length > 0 ? (
              <ul className="mt-1 space-y-2">
                {similar.map((s) => (
                  <li key={s.info_hash} className="flex items-baseline justify-between gap-3 text-xs">
                    <a
                      href={`/search?q=${encodeURIComponent(s.name)}`}
                      className="min-w-0 wrap-anywhere text-accent-ink hover:underline"
                    >
                      {highlightText(s.name || "(未命名)", query)}
                    </a>
                    <span className="shrink-0 tabular-nums text-mute">
                      {formatSize(s.total_size)}
                    </span>
                  </li>
                ))}
              </ul>
            ) : (
              <p className="mt-1 text-xs text-faint">暂无相似资源</p>
            )}
          </div>

          <ReportForm infoHash={result.info_hash} name={result.name} />
        </div>
      )}
    </div>
  );
}
