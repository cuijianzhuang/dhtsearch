import type { Metadata } from "next";
import Link from "next/link";
import {
  CATEGORIES,
  fetchSearch,
  type SearchResponse,
  type SearchSort,
} from "@/lib/api";
import { fetchSiteConfig } from "@/lib/site";
import { formatCount } from "@/lib/format";
import SearchBox from "@/components/SearchBox";
import ResultList from "@/components/ResultList";
import Pagination from "@/components/Pagination";
import SortSelect from "@/components/SortSelect";
import SiteLogo from "@/components/SiteLogo";
import AppearanceSwitcher from "@/components/AppearanceSwitcher";

interface PageProps {
  searchParams: Promise<{
    q?: string;
    page?: string;
    category?: string;
    sort?: string;
  }>;
}

const VALID_CATEGORIES = new Set(CATEGORIES.map((c) => c.key));
const VALID_SORTS = new Set(["relevance", "newest", "size"]);

/** Builds a /search href preserving q and sort, switching category. */
function categoryHref(q: string, sort: SearchSort, category: string): string {
  const params = new URLSearchParams();
  if (q) params.set("q", q);
  if (category) params.set("category", category);
  if (sort !== "relevance") params.set("sort", sort);
  const qs = params.toString();
  return qs ? `/search?${qs}` : "/search";
}

export async function generateMetadata({ searchParams }: PageProps): Promise<Metadata> {
  const { q } = await searchParams;
  const query = (q ?? "").trim();
  const site = await fetchSiteConfig();
  return {
    title: query ? `${query} 的搜索结果` : "最新收录",
    description: query
      ? `在 ${site.name} 中搜索「${query}」的磁力链接，结果已过滤成人内容与垃圾信息。`
      : "浏览 DHT 网络最新收录的磁力链接，已过滤成人内容与垃圾信息。",
  };
}

export default async function SearchPage({ searchParams }: PageProps) {
  const params = await searchParams;
  const q = (params.q ?? "").trim();
  const page = Math.max(1, parseInt(params.page ?? "1", 10) || 1);
  const pageSize = 20;
  const category = VALID_CATEGORIES.has(params.category ?? "")
    ? (params.category as string)
    : "";
  const sort: SearchSort = VALID_SORTS.has(params.sort ?? "")
    ? (params.sort as SearchSort)
    : "relevance";

  let data: SearchResponse | null = null;
  let error: string | null = null;
  try {
    data = await fetchSearch(q, page, pageSize, category, sort);
  } catch (e) {
    error = e instanceof Error ? e.message : "unknown error";
  }
  const site = await fetchSiteConfig();
  const counts = data?.categories ?? {};
  const rssHref = (() => {
    const p = new URLSearchParams();
    if (q) p.set("q", q);
    if (category) p.set("category", category);
    const qs = p.toString();
    return qs ? `/rss?${qs}` : "/rss";
  })();

  return (
    <main className="mx-auto flex w-full max-w-3xl flex-1 flex-col px-4 py-6">
      {/* Header: logo + search box + appearance switcher */}
      <header className="flex items-center gap-4">
        <Link
          href="/"
          className="inline-flex min-h-11 shrink-0 items-center text-xl font-bold tracking-tight hover:text-accent-ink"
        >
          <SiteLogo name={site.name} />
        </Link>
        {/* min-w-0 lets this flex child shrink below the input's intrinsic
            width; without it the row overflows narrow viewports and pushes
            the submit button off-screen. */}
        <div className="min-w-0 flex-1">
          <SearchBox initialQuery={q} />
        </div>
        <div className="shrink-0">
          <AppearanceSwitcher defaultAppearance={site.themeAppearance} />
        </div>
      </header>

      <div className="mt-4 rounded-lg border border-accent-line bg-accent-soft px-4 py-3 text-xs text-accent-ink">
        🛡️ 本站结果已自动过滤成人内容与垃圾信息，请放心使用。
      </div>

      {error ? (
        <BackendError q={q} detail={error} />
      ) : data ? (
        <>
          <div className="mt-4 flex flex-wrap items-center gap-x-4 gap-y-2">
            <p className="text-sm text-mute">
              {q ? (
                <>
                  「<span className="text-ink">{q}</span>」共找到{" "}
                  {formatCount(data.total ?? 0)} 条结果
                </>
              ) : (
                <>最新收录 · 共 {formatCount(data.total ?? 0)} 条</>
              )}
            </p>
            <span className="ml-auto inline-flex items-center gap-3">
              <a
                href={rssHref}
                className="inline-flex min-h-11 items-center gap-1 text-sm text-mute transition-colors hover:text-accent-ink"
                title="订阅当前搜索的 RSS 更新"
              >
                <span aria-hidden>📡</span> RSS 订阅
              </a>
              <SortSelect q={q} category={category} sort={sort} />
            </span>
          </div>

          <nav aria-label="按分类筛选" className="mt-3 flex flex-wrap gap-2">
            <Link
              href={categoryHref(q, sort, "")}
              prefetch={false}
              className={`inline-flex min-h-11 items-center rounded-lg border px-3 text-sm transition-colors ${
                category === ""
                  ? "border-accent bg-accent-soft font-medium text-accent-ink"
                  : "border-input bg-soft text-ink-2 hover:border-accent hover:text-accent-ink"
              }`}
            >
              全部
              <span className="ml-1 tabular-nums text-xs opacity-70">
                {formatCount(
                  CATEGORIES.reduce((sum, c) => sum + (counts[c.key] ?? 0), 0)
                )}
              </span>
            </Link>
            {CATEGORIES.map((c) => (
              <Link
                key={c.key}
                href={categoryHref(q, sort, c.key)}
                prefetch={false}
                className={`inline-flex min-h-11 items-center rounded-lg border px-3 text-sm transition-colors ${
                  category === c.key
                    ? "border-accent bg-accent-soft font-medium text-accent-ink"
                    : "border-input bg-soft text-ink-2 hover:border-accent hover:text-accent-ink"
                }`}
              >
                {c.label}
                <span className="ml-1 tabular-nums text-xs opacity-70">
                  {formatCount(counts[c.key] ?? 0)}
                </span>
              </Link>
            ))}
          </nav>

          {data.results && data.results.length > 0 ? (
            <>
              <ResultList results={data.results} trackers={data.trackers ?? []} />
              <Pagination
                q={q}
                page={data.page ?? page}
                total={data.total ?? 0}
                pageSize={data.page_size ?? pageSize}
                category={category}
                sort={sort}
              />
            </>
          ) : (
            <div className="mt-16 text-center">
              <p className="text-lg text-ink-2">
                {category ? "这个分类下没有找到相关结果" : "没有找到相关结果"}
              </p>
              <p className="mt-2 text-sm text-mute">
                {q
                  ? "换个关键词试试，或者稍后再来——爬虫正在持续收录新资源。"
                  : "索引暂时为空，爬虫正在收录中，请稍后再来。"}
              </p>
            </div>
          )}
        </>
      ) : null}

      <footer className="mt-auto pt-10 text-center text-xs text-faint">
        {site.name} · 干净、无广告的磁力搜索
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
