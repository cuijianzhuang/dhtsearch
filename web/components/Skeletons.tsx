// Loading skeletons for the main lists. Each placeholder mirrors the
// shape of the content it stands in for (same paddings, row heights
// and borders) so the swap to real content does not shift the layout:
// a card-shaped block for a result card, table-shaped rows for the
// admin list. Pure CSS pulse (animate-pulse) over the soft surface
// token — no extra requests, no JS.

function Bar({ className = "" }: { className?: string }) {
  return <div className={`rounded-md bg-soft ${className}`} aria-hidden />;
}

/** One search-result card placeholder (title lines + meta row). */
export function ResultCardSkeleton() {
  return (
    <div className="rounded-card border border-line bg-surface/60 p-4 shadow-card">
      <div className="flex items-start gap-3">
        <Bar className="mt-1 h-5 w-5 shrink-0" />
        <div className="min-w-0 flex-1 animate-pulse">
          <Bar className="h-4 w-3/4" />
          <Bar className="mt-2 h-4 w-1/2" />
          <div className="mt-2.5 flex items-center gap-3">
            <Bar className="h-5 w-10" />
            <Bar className="h-3 w-14" />
            <Bar className="h-3 w-16" />
            <Bar className="h-3 w-20" />
          </div>
        </div>
        <Bar className="h-11 w-11 shrink-0" />
      </div>
    </div>
  );
}

export function ResultListSkeleton({ count = 5 }: { count?: number }) {
  return (
    <div className="mt-3 space-y-3" aria-label="正在加载" role="status">
      {Array.from({ length: count }, (_, i) => (
        <ResultCardSkeleton key={i} />
      ))}
    </div>
  );
}

/** Admin torrent list placeholder: a filter-bar strip plus
 *  table-shaped rows (the desktop form of the list). */
export function AdminListSkeleton({ rows = 8 }: { rows?: number }) {
  return (
    <div aria-label="正在加载" role="status">
      <div className="flex flex-wrap items-center gap-2">
        <Bar className="min-h-11 flex-1 animate-pulse sm:flex-none sm:basis-72" />
        <Bar className="min-h-11 w-24 animate-pulse" />
        <Bar className="h-4 w-16 animate-pulse" />
      </div>
      <div className="mt-4 overflow-hidden rounded-card border border-line bg-surface shadow-card">
        <div className="animate-pulse">
          {Array.from({ length: rows }, (_, i) => (
            <div
              key={i}
              className="flex items-center gap-3 border-b border-line-2 px-3 py-3 last:border-0"
            >
              <Bar className="h-5 w-5 shrink-0" />
              <div className="min-w-0 flex-1">
                <Bar className="h-4 w-2/3" />
                <Bar className="mt-1.5 h-3 w-1/3" />
              </div>
              <Bar className="hidden h-4 w-14 shrink-0 sm:block" />
              <Bar className="hidden h-6 w-24 shrink-0 md:block" />
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}
