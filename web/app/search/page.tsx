import type { Metadata } from "next";
import Link from "next/link";
import { fetchSearch, type SearchResponse } from "@/lib/api";
import { formatCount } from "@/lib/format";
import SearchBox from "@/components/SearchBox";
import ResultList from "@/components/ResultList";
import Pagination from "@/components/Pagination";
import DigitalOceanBadge from "@/components/DigitalOceanBadge";

interface PageProps {
  searchParams: Promise<{ q?: string; page?: string }>;
}

export async function generateMetadata({ searchParams }: PageProps): Promise<Metadata> {
  const { q } = await searchParams;
  const query = (q ?? "").trim();
  return {
    title: query ? `${query} 的搜索结果` : "最新收录",
    description: query
      ? `在 DHTSearch 中搜索「${query}」的磁力链接，结果已过滤成人内容与垃圾信息。`
      : "浏览 DHT 网络最新收录的磁力链接，已过滤成人内容与垃圾信息。",
  };
}

export default async function SearchPage({ searchParams }: PageProps) {
  const params = await searchParams;
  const q = (params.q ?? "").trim();
  const page = Math.max(1, parseInt(params.page ?? "1", 10) || 1);
  const pageSize = 20;

  let data: SearchResponse | null = null;
  let error: string | null = null;
  try {
    data = await fetchSearch(q, page, pageSize);
  } catch (e) {
    error = e instanceof Error ? e.message : "unknown error";
  }

  return (
    <main className="mx-auto flex w-full max-w-3xl flex-1 flex-col px-4 py-6">
      {/* Header: logo + search box */}
      <header className="flex items-center gap-4">
        <Link
          href="/"
          className="inline-flex min-h-11 shrink-0 items-center text-xl font-bold tracking-tight hover:text-accent-ink"
        >
          DHT<span className="text-accent-ink">Search</span>
        </Link>
        {/* min-w-0 lets this flex child shrink below the input's intrinsic
            width; without it the row overflows narrow viewports and pushes
            the submit button off-screen. */}
        <div className="min-w-0 flex-1">
          <SearchBox initialQuery={q} />
        </div>
        <DigitalOceanBadge className="hidden shrink-0 sm:inline-block" />
      </header>

      <div className="mt-4 rounded-lg border border-accent-line bg-accent-soft px-4 py-3 text-xs text-accent-ink">
        🛡️ 本站结果已自动过滤成人内容与垃圾信息，请放心使用。
      </div>

      {error ? (
        <BackendError q={q} detail={error} />
      ) : data && data.results && data.results.length > 0 ? (
        <>
          <p className="mt-4 text-sm text-mute">
            {q ? (
              <>
                「<span className="text-ink">{q}</span>」共找到{" "}
                {formatCount(data.total)} 条结果
              </>
            ) : (
              <>最新收录 · 共 {formatCount(data.total)} 条</>
            )}
          </p>
          <ResultList results={data.results} />
          <Pagination q={q} page={data.page ?? page} total={data.total ?? 0} pageSize={data.page_size ?? pageSize} />
        </>
      ) : (
        <div className="mt-16 text-center">
          <p className="text-lg text-ink-2">没有找到相关结果</p>
          <p className="mt-2 text-sm text-mute">
            {q
              ? "换个关键词试试，或者稍后再来——爬虫正在持续收录新资源。"
              : "索引暂时为空，爬虫正在收录中，请稍后再来。"}
          </p>
        </div>
      )}

      <footer className="mt-auto pt-10 text-center text-xs text-faint">
        DHTSearch · 干净、无广告的磁力搜索
      </footer>
    </main>
  );
}

function BackendError({ q, detail }: { q: string; detail: string }) {
  return (
    <div className="mt-16 text-center">
      <p className="text-lg text-ink-2">无法连接到搜索服务</p>
      <p className="mt-2 text-sm text-mute">
        后端服务似乎未启动或暂时不可达，请确认 Go 后端已在运行
        （默认 <code className="rounded-md bg-soft px-2 py-1 font-mono text-xs">http://localhost:8080</code>
        ，可通过环境变量 <code className="rounded-md bg-soft px-2 py-1 font-mono text-xs">API_BASE</code> 修改）。
      </p>
      <p className="mt-1 font-mono text-xs text-faint">{detail}</p>
      <Link
        href={q ? `/search?q=${encodeURIComponent(q)}` : "/search"}
        className="mt-6 inline-flex min-h-11 items-center rounded-lg border border-input bg-soft px-4 text-sm text-ink-2 hover:border-accent hover:text-accent-ink"
      >
        重试
      </Link>
    </div>
  );
}
