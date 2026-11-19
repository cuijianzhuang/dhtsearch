import Link from "next/link";

/** Prev/next pagination that preserves the admin list's query params. */
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
  return (
    <nav className="mt-6 flex items-center justify-center gap-4">
      {page > 1 ? (
        <Link href={href(page - 1)} className={btnCls} prefetch={false}>
          ← 上一页
        </Link>
      ) : (
        <span className={disabledCls}>← 上一页</span>
      )}
      <span className="text-sm text-mute">
        第 {page} / {totalPages} 页
      </span>
      {page < totalPages ? (
        <Link href={href(page + 1)} className={btnCls} prefetch={false}>
          下一页 →
        </Link>
      ) : (
        <span className={disabledCls}>下一页 →</span>
      )}
    </nav>
  );
}
