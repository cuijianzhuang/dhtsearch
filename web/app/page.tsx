import Link from "next/link";
import { fetchStats, fetchTrending, fetchTrendingTerms } from "@/lib/api";
import { fetchSiteConfig } from "@/lib/site";
import { formatCount } from "@/lib/format";
import SearchBox from "@/components/SearchBox";
import SiteLogo from "@/components/SiteLogo";
import AppearanceSwitcher from "@/components/AppearanceSwitcher";

function TrendingRow({ label, titles }: { label: string; titles: string[] }) {
  return (
    // Wrapped and centered, label inline at the start of the group:
    // every title the API returns is on the page — no sideways
    // scrolling, no fade mask, no hidden tail. The cost, accepted
    // deliberately: the section's height is data-dependent again, so
    // the old one-viewport guarantee no longer holds on small
    // screens (the page may scroll at phone heights; desktop sizes
    // still fit). max-w-full + min-w-0 on a chip lets an over-long
    // title wrap inside its own pill instead of pushing the page
    // wider than the screen.
    <div className="mt-3 flex flex-wrap items-center justify-center gap-2 first:mt-0">
      <span className="shrink-0 text-xs text-mute">{label}</span>
      {titles.map((title) => (
        <Link
          key={title}
          href={`/search?q=${encodeURIComponent(title)}`}
          className="inline-flex min-h-11 min-w-0 max-w-full items-center rounded-full border border-line bg-surface px-3 text-center text-xs text-ink-2 transition-colors hover:border-accent hover:text-accent-ink"
        >
          {title}
        </Link>
      ))}
    </div>
  );
}

export default async function Home() {
  const [stats, trending, trendingTerms, site] = await Promise.all([
    fetchStats(),
    fetchTrending(),
    fetchTrendingTerms(),
    fetchSiteConfig(),
  ]);
  // The site's own hot searches (anonymous aggregate counts from
  // /api/trending-terms), capped so the row stays one tidy block.
  // Empty on a fresh install — the row then simply does not render.
  const hotTerms = (trendingTerms?.terms ?? []).slice(0, 12).map((t) => t.term);
  const rows = [
    { label: "热门电影", titles: trending?.movies ?? [] },
    { label: "热门美剧", titles: trending?.tv ?? [] },
    { label: "热门日剧", titles: trending?.tv_jp ?? [] },
    { label: "热门韩剧", titles: trending?.tv_kr ?? [] },
  ].filter((r) => r.titles.length > 0);

  return (
    <main className="relative flex flex-1 flex-col items-center justify-center px-4 py-10 sm:py-12">
      <div className="absolute right-4 top-4">
        <AppearanceSwitcher defaultAppearance={site.themeAppearance} />
      </div>
      {/* The measure widens at xl: at 672px the block was a pinched
          island on wide monitors. 54rem gives the search box and the
          trending rows room to breathe; below xl the geometry is
          exactly what the compact layout was tuned with. The hero
          type steps up with it (the logo is em-sized, so it
          follows). */}
      <div className="w-full max-w-2xl xl:max-w-[54rem]">
        <h1 className="text-center text-4xl font-bold tracking-tight sm:text-5xl xl:text-6xl">
          <SiteLogo name={site.name} />
        </h1>
        <p className="mt-3 text-center text-sm text-mute sm:text-base xl:text-lg">
          无广告的磁力链接搜索引擎
          <br className="sm:hidden" />
          <span className="hidden sm:inline"> · </span>
          基于 DHT 网络实时收录，索引持续更新
        </p>

        <div className="mt-8 xl:mt-10">
          <SearchBox large />
        </div>

        <p className="mt-3 text-center text-xs text-mute">
          直接输入关键词搜索，留空则浏览最新收录的资源
        </p>

        {/* Index stats sit directly under the search box, not at the
            page bottom: on a phone the bottom placement put them
            below the fold behind the whole trending block, and the
            compact one-viewport layout wants the status line where
            it is seen. The filtered counts are the backend's own
            cumulative counters, shown as reported. */}
        <p className="mt-3 text-center text-xs text-mute">
          {stats ? (
            <>
              已收录 {formatCount(stats.torrents)} 条 · 已过滤成人内容{" "}
              {formatCount(stats.adult_filtered)} 条 · 垃圾信息{" "}
              {formatCount(stats.spam_filtered)} 条
              {stats.crawler_running === false && (
                <span className="ml-2 text-warn">（爬虫暂停中）</span>
              )}
            </>
          ) : (
            "索引统计暂时不可用（后端未连接）"
          )}
        </p>

        {(hotTerms.length > 0 || rows.length > 0) && (
          <div id="trending" className="mt-6 scroll-mt-6 xl:mt-8">
            {hotTerms.length > 0 && (
              <TrendingRow label="热门搜索" titles={hotTerms} />
            )}
            {rows.map((r) => (
              <TrendingRow key={r.label} label={r.label} titles={r.titles} />
            ))}
            {rows.length > 0 && (
              <p className="mt-3 text-center text-xs text-faint">
                热门影视数据来自豆瓣
              </p>
            )}
          </div>
        )}
      </div>
    </main>
  );
}
