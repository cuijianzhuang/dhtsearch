"use client";

import Link from "next/link";
import type { MouseEvent } from "react";

interface PaginationProps {
  q: string;
  page: number;
  total: number;
  pageSize: number;
}

export default function Pagination({ q, page, total, pageSize }: PaginationProps) {
  const totalPages = Math.max(1, Math.ceil(total / pageSize));
  const mkHref = (p: number) =>
    `/search?q=${encodeURIComponent(q)}&page=${p}`;

  const btnCls =
    "inline-flex min-h-11 items-center rounded-lg border border-input bg-soft px-4 text-sm text-ink-2 transition-colors hover:border-accent hover:text-accent-ink";
  const disabledCls =
    "inline-flex min-h-11 cursor-not-allowed items-center rounded-lg border border-line bg-surface px-4 text-sm text-faint";
  const navigate = (event: MouseEvent<HTMLAnchorElement>, href: string) => {
    event.preventDefault();
    window.location.assign(href);
  };

  return (
    <nav className="mt-6 flex items-center justify-center gap-4">
      {page > 1 ? (
        <Link
          href={mkHref(page - 1)}
          className={btnCls}
          prefetch={false}
          onClick={(event) => navigate(event, mkHref(page - 1))}
        >
          ← 上一页
        </Link>
      ) : (
        <span className={disabledCls}>← 上一页</span>
      )}
      <span className="text-sm text-mute">
        第 {page} / {totalPages} 页
      </span>
      {page < totalPages ? (
        <Link
          href={mkHref(page + 1)}
          className={btnCls}
          prefetch={false}
          onClick={(event) => navigate(event, mkHref(page + 1))}
        >
          下一页 →
        </Link>
      ) : (
        <span className={disabledCls}>下一页 →</span>
      )}
    </nav>
  );
}
