import type { Metadata } from "next";
import type { ReactNode } from "react";
import Link from "next/link";
import {
  CATEGORIES,
  fetchSearch,
  SearchError,
  type SearchErrorKind,
  fetchTrending,
  type SearchResponse,
  type SearchSort,
} from "@/lib/api";
import { fetchSiteConfig } from "@/lib/site";
import { formatCount } from "@/lib/format";
import SearchBox from "@/components/SearchBox";
import ResultList from "@/components/ResultList";
import SearchScrollRegion from "@/components/SearchScrollRegion";
import Pagination from "@/components/Pagination";
import SortSelect from "@/components/SortSelect";
import HideDeadToggle from "@/components/HideDeadToggle";
import SiteLogo from "@/components/SiteLogo";
import AppearanceSwitcher from "@/components/AppearanceSwitcher";

interface PageProps {
  searchParams: Promise<{
    q?: string;
    page?: string;
    category?: string;
    sort?: string;
    hide_dead?: string;
  }>;
}

const VALID_CATEGORIES = new Set(CATEGORIES.map((c) => c.key));
const VALID_SORTS = new Set(["relevance", "newest", "size", "seeders"]);

/** Builds a /search href preserving q, sort and the dead filter,
 *  switching category. */
function categoryHref(
  q: string,
  sort: SearchSort,
  category: string,
  hideDead: boolean
): string {
  const params = new URLSearchParams();
  if (q) params.set("q", q);
  if (category) params.set("category", category);
  if (sort !== "relevance") params.set("sort", sort);
  if (hideDead) params.set("hide_dead", "1");
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
      ? `在 ${site.name} 中搜索「${query}」的磁力链接。`
      : "浏览 DHT 网络最新收录的磁力链接。",
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
  const hideDead = params.hide_dead === "1" || params.hide_dead === "true";

  let data: SearchResponse | null = null;
  let error: string | null = null;
  let errorKind: SearchErrorKind = "unavailable";
  try {
    data = await fetchSearch(q, page, pageSize, category, sort, hideDead);
  } catch (e) {
    if (e instanceof SearchError) {
      errorKind = e.kind;
    }
    error = e instanceof Error ? e.message : "unknown error";
  }
  const site = await fetchSiteConfig();
  const counts = data?.categories ?? {};
  // Popular terms for the empty state: this week's trending titles
  // when the backend has them, a small static set otherwise.
  const emptyTerms = await (async (): Promise<string[]> => {
    if (!data || (data.results?.length ?? 0) > 0) return [];
    const trending = await fetchTrending();
    const terms = Array.from(
      new Set([
        ...(trending?.movies ?? []),
        ...(trending?.tv ?? []),
        ...(trending?.tv_jp ?? []),
        ...(trending?.tv_kr ?? []),
      ])
    ).slice(0, 10);
    return terms.length > 0
      ? terms
      : ["纪录片", "动漫", "古典音乐", "有声书", "开源软件", "电子书"];
  })();
  const rssHref = (() => {
    const p = new URLSearchParams();
    if (q) p.set("q", q);
    if (category) p.set("category", category);
    const qs = p.toString();
    return qs ? `/rss?${qs}` : "/rss";
  })();

  // Identity of the current result set: the scroll region starts a
  // new set at the top of the list (see SearchScrollRegion).
  const resetKey = [q, page, category, sort, hideDead ? "1" : "0"].join("|");

  return (
    // App shell: data-search-shell caps the document at 100dvh (see
    // globals.css), so the header, notice, controls and category
    // chips below stay pinned and only the results region scrolls.
    // pt-6 only — the region carries the bottom padding. The column
    // is max-w-3xl up to xl, then widens to 1120px: on a large
    // monitor the 768px measure left the whole shell pinched in the
    // middle with dead space on both sides, while result cards (long
    // release names, right-anchored copy button) use the extra width
    // without any layout change. Everything in the shell — header,
    // notice, controls, chips, scroll region, footer line — shares
    // this one measure, so the single class widens them together.
    <main
      data-search-shell
      className="mx-auto flex w-full max-w-3xl min-h-0 flex-1 flex-col px-4 pt-6 xl:max-w-[1120px]"
    >
      {/* Header: logo + search box + appearance switcher. On phones
          the three no longer share one row: logo + switcher keep the
          first row and the search box wraps to its own full-width row
          — squeezed into the leftover ~110px it collapsed to a 41px
          input that showed a single character of the query. */}
      <header className="flex flex-wrap items-center gap-x-4 gap-y-3">
        <Link
          href="/"
          className="inline-flex min-h-11 shrink-0 items-center text-xl font-bold tracking-tight hover:text-accent-ink"
        >
          <SiteLogo name={site.name} />
        </Link>
        {/* min-w-0 lets this flex child shrink below the input's intrinsic
            width; without it the row overflows narrow viewports and pushes
            the submit button off-screen. */}
        <div className="order-3 min-w-0 w-full sm:order-none sm:w-auto sm:flex-1">
          <SearchBox initialQuery={q} />
        </div>
        <div className="ml-auto shrink-0 sm:ml-0">
          <AppearanceSwitcher defaultAppearance={site.themeAppearance} />
        </div>
      </header>

      <div className="mt-4 flex items-center gap-2 rounded-lg border border-accent-line bg-accent-soft px-4 py-3 text-xs text-accent-ink">
        <svg
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          strokeWidth="2"
          strokeLinecap="round"
          strokeLinejoin="round"
          className="h-4 w-4 shrink-0"
          aria-hidden
        >
          <path d="M20 13c0 5-3.5 7.5-7.66 8.95a1 1 0 0 1-.67-.01C7.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.24-2.72a1.17 1.17 0 0 1 1.52 0C14.51 3.81 17 5 19 5a1 1 0 0 1 1 1z" />
          <path d="m9 12 2 2 4-4" />
        </svg>
        收录来自 DHT 网络、实时更新，请注意甄别来源不明的资源。
      </div>

      {/* Pinned controls: result count, RSS, dead filter, sort and
          the category chips stay with the header; only the results
          below them scroll. */}
      {!error && data && (
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
                <svg
                  viewBox="0 0 24 24"
                  fill="none"
                  stroke="currentColor"
                  strokeWidth="2"
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  className="h-4 w-4"
                  aria-hidden
                >
                  <path d="M4 11a9 9 0 0 1 9 9" />
                  <path d="M4 4a16 16 0 0 1 16 16" />
                  <line x1="4" x2="4.01" y1="20" y2="20" />
                </svg>{" "}
                RSS 订阅
              </a>
              <HideDeadToggle q={q} category={category} sort={sort} hideDead={hideDead} />
              <SortSelect q={q} category={category} sort={sort} hideDead={hideDead} />
            </span>
          </div>

          <nav aria-label="按分类筛选" className="mt-3 flex flex-wrap gap-2">
            <Link
              href={categoryHref(q, sort, "", hideDead)}
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
                href={categoryHref(q, sort, c.key, hideDead)}
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
        </>
      )}

      <SearchScrollRegion resetKey={resetKey}>
        {error ? (
          <BackendError q={q} kind={errorKind} detail={error} />
        ) : data ? (
          data.results && data.results.length > 0 ? (
            <>
              <ResultList results={data.results} trackers={data.trackers ?? []} query={q} />
              <Pagination
                q={q}
                page={data.page ?? page}
                total={data.total ?? 0}
                pageSize={data.page_size ?? pageSize}
                category={category}
                sort={sort}
                hideDead={hideDead}
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
              {hideDead && (
                <p className="mt-2 text-sm text-mute">
                  当前已隐藏疑似死种，
                  <Link
                    href={categoryHref(q, sort, category, false)}
                    prefetch={false}
                    className="text-accent-ink hover:underline"
                  >
                    点此显示全部结果
                  </Link>
                </p>
              )}
              {(category || q) && (
                <div className="mt-6 flex flex-wrap items-center justify-center gap-2">
                  {category && (
                    <Link
                      href={categoryHref(q, sort, "", hideDead)}
                      prefetch={false}
                      className="inline-flex min-h-11 items-center rounded-lg bg-accent px-4 text-sm font-medium text-on-accent transition-colors hover:bg-accent-hover"
                    >
                      清除分类筛选
                    </Link>
                  )}
                  {q && (
                    <Link
                      href="/search"
                      prefetch={false}
                      className="inline-flex min-h-11 items-center rounded-lg border border-input bg-soft px-4 text-sm text-ink-2 transition-colors hover:border-accent hover:text-accent-ink"
                    >
                      浏览最新收录
                    </Link>
                  )}
                </div>
              )}
              {emptyTerms.length > 0 && (
                <div className="mt-8">
                  <p className="text-xs text-faint">试试热门搜索</p>
                  <div className="mt-3 flex flex-wrap items-center justify-center gap-2">
                    {emptyTerms.map((term) => (
                      <Link
                        key={term}
                        href={`/search?q=${encodeURIComponent(term)}`}
                        prefetch={false}
                        className="inline-flex min-h-9 items-center rounded-full border border-line bg-surface px-3 text-xs text-ink-2 transition-colors hover:border-accent hover:text-accent-ink"
                      >
                        {term}
                      </Link>
                    ))}
                  </div>
                </div>
              )}
            </div>
          )
        ) : null}

        {/* The page's footer line ends the scroll region: mt-auto
            rests it at the region's bottom when the results are
            short, and after the pagination when they are long. The
            layout's PublicFooter （管理 entry) stays pinned below
            the shell. Pagination likewise stays at the list end —
            reached by scrolling the list, never the chrome. */}
        <footer className="mt-auto pt-10 text-center text-xs text-faint">
          {site.name} · 无广告的磁力搜索
        </footer>
      </SearchScrollRegion>
    </main>
  );
}

function BackendError({
  q,
  kind,
  detail,
}: {
  q: string;
  kind: SearchErrorKind;
  detail: string;
}) {
  // Copy follows the actual failure (see SearchErrorKind in lib/api):
  // blaming a stopped backend for a 401 sent real operators chasing
  // the wrong thing when the token gate locked the site itself out.
  const copy: Record<SearchErrorKind, { title: string; body: ReactNode }> = {
    auth: {
      title: "搜索被令牌验证拦下了",
      body: (
        <>
          本站已开启公开 API 令牌强制（
          <code className="rounded-md bg-soft px-2 py-1 font-mono text-xs">PUBLIC_API_REQUIRE_TOKEN</code>
          ）。站点自身的搜索请求本应通过内部第一方通道自动放行——出现这个提示，说明内部令牌（
          <code className="rounded-md bg-soft px-2 py-1 font-mono text-xs">INTERNAL_API_TOKEN</code>
          ）没有在 API 与 web 两侧配齐，请管理员检查安装配置（运行 install.sh 升级可自动生成并同步）。
        </>
      ),
    },
    rate: {
      title: "请求过于频繁",
      body: <>搜索请求触发了限流，请稍等片刻再重试。</>,
    },
    disabled: {
      title: "搜索服务已关闭",
      body: (
        <>
          管理员已关闭公开 API（
          <code className="rounded-md bg-soft px-2 py-1 font-mono text-xs">PUBLIC_API_ENABLED</code>
          ），前台搜索随之停用，请稍后再来。
        </>
      ),
    },
    unavailable: {
      title: "无法连接到搜索服务",
      body: (
        <>
          后端服务似乎未启动或暂时不可达，请确认 Go 后端已在运行
          （默认 <code className="rounded-md bg-soft px-2 py-1 font-mono text-xs">http://localhost:8080</code>
          ，可通过环境变量 <code className="rounded-md bg-soft px-2 py-1 font-mono text-xs">API_BASE</code> 修改）。
        </>
      ),
    },
  };
  const { title, body } = copy[kind];
  return (
    <div className="mt-16 text-center">
      <p className="text-lg text-ink-2">{title}</p>
      <p className="mt-2 text-sm text-mute">{body}</p>
      {kind === "unavailable" ? (
        <p className="mt-1 font-mono text-xs text-faint">{detail}</p>
      ) : null}
      <Link
        href={q ? `/search?q=${encodeURIComponent(q)}` : "/search"}
        className="mt-6 inline-flex min-h-11 items-center rounded-lg border border-input bg-soft px-4 text-sm text-ink-2 hover:border-accent hover:text-accent-ink"
      >
        重试
      </Link>
    </div>
  );
}
