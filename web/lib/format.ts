// Format bytes into a human-readable size string, e.g. 2.0 GB
export function formatSize(bytes: number | undefined | null): string {
  if (bytes === undefined || bytes === null || !Number.isFinite(bytes) || bytes < 0) {
    return "-";
  }
  if (bytes === 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB", "PB"];
  const i = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
  const value = bytes / Math.pow(1024, i);
  return `${value >= 100 ? Math.round(value) : value.toFixed(1)} ${units[i]}`;
}

// Format a unix timestamp (seconds) as YYYY-MM-DD
export function formatDate(ts: number | undefined | null): string {
  if (!ts || !Number.isFinite(ts)) return "-";
  const d = new Date(ts * 1000);
  if (Number.isNaN(d.getTime())) return "-";
  const y = d.getFullYear();
  const m = String(d.getMonth() + 1).padStart(2, "0");
  const day = String(d.getDate()).padStart(2, "0");
  return `${y}-${m}-${day}`;
}

// Format a unix timestamp (seconds) as relative time in Chinese, e.g. 3 天前
export function formatRelativeTime(ts: number | undefined | null): string {
  if (!ts || !Number.isFinite(ts)) return "-";
  const diff = Date.now() - ts * 1000;
  if (diff < 0) return formatDate(ts);
  const minutes = Math.floor(diff / 60000);
  if (minutes < 1) return "刚刚";
  if (minutes < 60) return `${minutes} 分钟前`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours} 小时前`;
  const days = Math.floor(hours / 24);
  if (days < 30) return `${days} 天前`;
  return formatDate(ts);
}

// Whether a unix timestamp is older than the given age — used to
// grey out swarm snapshots past their freshness window.
export function isOlderThan(tsSeconds: number, maxAgeSeconds: number): boolean {
  return Date.now() / 1000 - tsSeconds > maxAgeSeconds;
}

// Format a duration in seconds as a coarse Chinese span, e.g.
// "3 天" / "26 小时" / "5 分钟" — for evidence phrases like the
// dead-torrent basis ("已持续 3 天"), where day granularity is the
// honest precision.
export function formatSpan(seconds: number | undefined | null): string {
  if (seconds === undefined || seconds === null || !Number.isFinite(seconds) || seconds < 0) {
    return "-";
  }
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${Math.max(1, minutes)} 分钟`;
  const hours = Math.floor(minutes / 60);
  if (hours < 48) return `${hours} 小时`;
  return `${Math.floor(hours / 24)} 天`;
}

// Format a count exactly, with thousands separators. Used where the
// precise figure is the point: prose that reconciles counters (the
// pipeline footnote) and the title attribute behind an abbreviated
// big-slot number.
export function formatExactCount(n: number | undefined | null): string {
  if (n === undefined || n === null || !Number.isFinite(n)) return "-";
  return n.toLocaleString("zh-CN");
}

// Format a count for display slots. Below a million the digits stay
// exact (grouped) — the operator reconciles totals at that scale, so
// precision is a feature. From a million up, the value abbreviates
// to 万 (one decimal, trailing .0 trimmed); from a hundred million,
// to 亿 (up to two decimals, trimmed). A seven-digit pipeline count
// rendered in full overflows its ring column on a phone; the exact
// figure stays one hover away via formatExactCount in a title.
export function formatCount(n: number | undefined | null): string {
  if (n === undefined || n === null || !Number.isFinite(n)) return "-";
  const abs = Math.abs(n);
  if (abs < 1_000_000) return n.toLocaleString("zh-CN");
  if (abs < 100_000_000) {
    const wan = (Math.round((n / 10_000) * 10) / 10).toFixed(1);
    return `${wan.endsWith(".0") ? wan.slice(0, -2) : wan}万`;
  }
  const yi = (Math.round((n / 100_000_000) * 100) / 100).toFixed(2);
  return `${yi.replace(/\.?0+$/, "")}亿`;
}

// Longest directory prefix (ending in "/") shared by every path.
//
// Torrents almost always wrap their contents in one top-level folder named
// after the torrent itself. Rendering that prefix on every row pushes the only
// distinguishing part — the file name — past the right edge, making unrelated
// files look identical. Callers strip the prefix and show it once instead.
export function commonDirPrefix(paths: string[]): string {
  if (paths.length < 2) return "";
  let prefix = paths[0].slice(0, paths[0].lastIndexOf("/") + 1);
  for (const p of paths.slice(1)) {
    while (prefix && !p.startsWith(prefix)) {
      // Drop the trailing "/" then back up to the previous separator.
      prefix = prefix.slice(0, prefix.lastIndexOf("/", prefix.length - 2) + 1);
    }
    if (!prefix) return "";
  }
  return prefix;
}

// Shorten a directory path from the left, e.g. "A.Very.Long.Release/Subs/" ->
// ".../Subs/". The deepest segment says what the file is for, while the release
// folder at the front is both the longest and the least informative, so keep
// trailing segments while they fit in the budget and elide the rest. A single
// trailing segment is kept whatever its length — CSS truncates the overflow.
export function shortenDir(dir: string, maxChars = 48): string {
  if (dir.length <= maxChars) return dir;
  const segments = dir.split("/").filter(Boolean);
  const kept: string[] = [];
  let used = 0;
  for (let i = segments.length - 1; i >= 0; i--) {
    const cost = segments[i].length + 1;
    if (kept.length > 0 && used + cost > maxChars) break;
    kept.unshift(segments[i]);
    used += cost;
  }
  const elided = kept.length < segments.length ? "…/" : "";
  return `${elided}${kept.join("/")}/`;
}
