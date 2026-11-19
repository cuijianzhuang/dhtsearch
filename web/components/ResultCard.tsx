"use client";

import { useState } from "react";
import type { SearchResult, SimilarItem } from "@/lib/api";
import {
  commonDirPrefix,
  formatCount,
  formatRelativeTime,
  formatSize,
  shortenDir,
} from "@/lib/format";
import CopyMagnetButton from "./CopyMagnetButton";
import ReportForm from "./ReportForm";

const MAX_FILES_SHOWN = 10;

export default function ResultCard({
  result,
  trackers,
  selected,
  onToggleSelect,
}: {
  result: SearchResult;
  /** Announce URLs stamped into this result's magnet (site-wide list
   *  from the search response), shown in the detail area. */
  trackers: string[];
  selected: boolean;
  onToggleSelect: () => void;
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
  const files = result.files ?? [];
  const shownFiles = files.slice(0, MAX_FILES_SHOWN);
  // The crawler stores at most 50 file entries but records the real count, so
  // count what's hidden against file_count — otherwise a 3136-file torrent
  // claims only 40 files are missing.
  const totalFiles = Math.max(result.file_count ?? 0, files.length);
  const hiddenFiles = totalFiles - shownFiles.length;
  // Computed over every file, not just the shown ones, so the prefix doesn't
  // shift when the "+N more" tail is hidden.
  const root = commonDirPrefix(files.map((f) => f.path));

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
            {result.name || "(未命名)"}
          </h3>
          {/* The publisher's title, shown when it differs from the torrent
              name — usually the Chinese name a romaji release was found by. */}
          {result.alias && result.alias !== result.name && (
            <p
              className={`mt-1 text-xs wrap-anywhere text-mute ${
                expanded ? "" : "line-clamp-1"
              }`}
            >
              {result.alias}
            </p>
          )}
          <p className="mt-1 flex flex-wrap gap-x-3 gap-y-1 text-xs text-mute">
            <span>{formatSize(result.total_size)}</span>
            <span>{formatCount(totalFiles)} 个文件</span>
            <span>收录于 {formatRelativeTime(result.created_at)}</span>
          </p>
        </button>
        <CopyMagnetButton magnet={result.magnet} />
      </div>

      {expanded && (
        <div className="mt-3 rounded-lg border border-line bg-canvas/60 p-3">
          {files.length > 0 ? (
            <>
              {root && (
                <p
                  className="mb-2 line-clamp-2 border-b border-line-2 pb-2 font-mono text-[11px] wrap-anywhere text-mute"
                  title={root}
                >
                  📁 {root}
                </p>
              )}
              <ul className="space-y-2 text-xs text-mute">
                {shownFiles.map((f, i) => {
                  const rest = f.path.slice(root.length);
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
                {hiddenFiles > 0 && (
                  <li className="pt-2 text-mute">
                    … 其余 {formatCount(hiddenFiles)} 个文件未显示
                  </li>
                )}
              </ul>
            </>
          ) : (
            <p className="text-xs text-mute">暂无文件列表信息</p>
          )}
          <p
            className="mt-3 line-clamp-2 border-t border-line-2 pt-2 font-mono text-[11px] leading-relaxed break-all text-faint"
            title={result.magnet}
          >
            {result.magnet}
          </p>

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
                      {s.name || "(未命名)"}
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
