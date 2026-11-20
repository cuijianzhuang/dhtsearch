import Link from "next/link";

/**
 * The page window around the current page: always the first and last
 * page plus current ±2, with "…" markers where the range is elided.
 * Entries far from the current page (±2) are flagged so the caller
 * can hide them on small screens, where the full window would crowd
 * out the first/last/jump controls.
 */
type WindowEntry =
  | { kind: "page"; value: number; far: boolean }
  | { kind: "gap" };

function pageWindow(page: number, totalPages: number): WindowEntry[] {
  const wanted = new Set(
    [1, totalPages, page - 2, page - 1, page, page + 1, page + 2].filter(
      (p) => p >= 1 && p <= totalPages
    )
  );
  const sorted = [...wanted].sort((a, b) => a - b);
  const out: WindowEntry[] = [];
  let prev = 0;
  for (const p of sorted) {
    if (prev && p - prev > 1) out.push({ kind: "gap" });
    out.push({ kind: "page", value: p, far: Math.abs(p - page) === 2 });
    prev = p;
  }
  return out;
}

/**
 * Full pagination for the admin lists: first/last shortcuts, prev/
 * next, a numbered window around the current page, and a jump-to-page
 * form. The jump form is a plain GET form — it resubmits the list's
 * own query params (carried as hidden inputs) plus page=N, exactly
 * the URL the links build, so no client JS is involved and every
 * consumer keeps its existing contract. Shared by the torrents list
 * (and review queue), blocked, reports, and the torrent-detail file
 * list.
 */
export default function AdminPagination({
  basePath,
  params,
  page,
  total,
  pageSize,
}: {
  basePath: string;
  params: Record<string, string>;
  page: number;
  total: number;
  pageSize: number;
}) {
  const totalPages = Math.max(1, Math.ceil(total / pageSize));
  if (totalPages <= 1) return null;
  const href = (p: number) => {
    const qs = new URLSearchParams({ ...params, page: String(p) });
    return `${basePath}?${qs.toString()}`;
  };
  const btnCls =
    "inline-flex min-h-11 items-center rounded-lg border border-input bg-soft px-4 text-sm text-ink-2 transition-colors hover:border-accent hover:text-accent-ink";
  const disabledCls =
    "inline-flex min-h-11 cursor-not-allowed items-center rounded-lg border border-line bg-surface px-4 text-sm text-faint";
  const numCls =
    "inline-flex min-h-11 min-w-11 items-center justify-center rounded-lg border border-input bg-soft px-2 text-sm tabular-nums text-ink-2 transition-colors hover:border-accent hover:text-accent-ink";
  const curCls =
    "inline-flex min-h-11 min-w-11 items-center justify-center rounded-lg bg-accent px-2 text-sm font-medium tabular-nums text-on-accent";
  const entries = pageWindow(page, totalPages);

  const stepLink = (
    target: number,
    enabled: boolean,
    label: string,
    aria: string
  ) =>
    enabled ? (
      <Link href={href(target)} className={btnCls} prefetch={false} aria-label={aria}>
        {label}
      </Link>
    ) : (
      <span className={disabledCls} aria-label={aria} aria-disabled="true">
        {label}
      </span>
    );

  return (
    <div className="mt-6 flex flex-wrap items-center justify-center gap-x-4 gap-y-3">
      <nav className="flex flex-wrap items-center justify-center gap-2" aria-label="分页">
        {stepLink(1, page > 1, "首页", "第一页")}
        {stepLink(page - 1, page > 1, "← 上一页", "上一页")}
        {entries.map((e, i) =>
          e.kind === "gap" ? (
            <span key={`gap-${i}`} className="px-0.5 text-faint" aria-hidden>
              …
            </span>
          ) : e.value === page ? (
            <span key={e.value} className={curCls} aria-current="page">
              {e.value}
            </span>
          ) : (
            <Link
              key={e.value}
              href={href(e.value)}
              className={`${numCls}${e.far ? " max-sm:hidden" : ""}`}
              prefetch={false}
              aria-label={`第 ${e.value} 页`}
            >
              {e.value}
            </Link>
          )
        )}
        {stepLink(page + 1, page < totalPages, "下一页 →", "下一页")}
        {stepLink(totalPages, page < totalPages, "末页", "最后一页")}
      </nav>
      {/* Jump to an arbitrary page. min/max clamp the input to the
          valid range; the list pages also clamp server-side. */}
      <form method="get" action={basePath} className="flex items-center gap-2">
        {Object.entries(params)
          .filter(([k]) => k !== "page")
          .map(([k, v]) => (
            <input key={k} type="hidden" name={k} value={v} />
          ))}
        <label className="flex items-center gap-2 text-sm text-mute">
          跳至
          <input
            type="number"
            name="page"
            min={1}
            max={totalPages}
            required
            inputMode="numeric"
            placeholder={String(page)}
            aria-label="跳转到页码"
            className="min-h-11 w-20 rounded-lg border border-input bg-surface px-2 text-center text-sm tabular-nums text-ink focus:border-accent focus:outline-none"
          />
          <span className="whitespace-nowrap tabular-nums">/ {totalPages} 页</span>
        </label>
        <button type="submit" className={btnCls}>
          前往
        </button>
      </form>
    </div>
  );
}
