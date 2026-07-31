"use client";

import Link from "next/link";
import type { MouseEvent } from "react";

interface PaginationProps {
  q: string;
  page: number;
  total: number;
  // total stopped at the backend's counting cap, so totalPages is a floor and
  // there may be more pages than the label claims.
  capped?: boolean;
  pageSize: number;
}

export default function Pagination({ q, page, total, capped, pageSize }: PaginationProps) {
  const totalPages = Math.max(1, Math.ceil(total / pageSize));
  const mkHref = (p: number) =>
    `/search?q=${encodeURIComponent(q)}&page=${p}`;

  const btnCls =
    "rounded-md border border-zinc-700 bg-zinc-800 px-4 py-2 text-sm text-zinc-300 transition-colors hover:border-emerald-600 hover:text-emerald-400";
  const disabledCls =
    "cursor-not-allowed rounded-md border border-zinc-800 bg-zinc-900 px-4 py-2 text-sm text-zinc-600";
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
      <span className="text-sm text-zinc-400">
        第 {page} / {totalPages}{capped ? "+" : ""} 页
      </span>
      {page < totalPages || capped ? (
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
