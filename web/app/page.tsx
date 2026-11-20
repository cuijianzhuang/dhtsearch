import Link from "next/link";
import { fetchStats, fetchTrending } from "@/lib/api";
import { fetchSiteConfig } from "@/lib/site";
import { formatCount } from "@/lib/format";
import SearchBox from "@/components/SearchBox";
import SiteLogo from "@/components/SiteLogo";
import AppearanceSwitcher from "@/components/AppearanceSwitcher";

function TrendingRow({ label, titles }: { label: string; titles: string[] }) {
  return (
    <div className="mt-3 flex items-center gap-2 first:mt-0">
      <span className="shrink-0 text-xs text-mute">{label}</span>
      {/* One line per category, always: the row's height must not
          depend on how many titles Douban returned or how long they
          are, or the homepage's total height becomes data-dependent
          and the one-viewport fit is lost. Overflow scrolls sideways
          inside the row (fade at the right edge hints at it); every
          title stays reachable. min-w-0 on the wrapper is what lets
          the rail shrink instead of pushing the page wider. */}
      <div className="min-w-0 flex-1">
        <div className="flex gap-2 overflow-x-auto [scrollbar-width:none] [mask-image:linear-gradient(to_right,black_calc(100%-2rem),transparent)] [&::-webkit-scrollbar]:hidden">
          {titles.map((title) => (
            <Link
              key={title}
              href={`/search?q=${encodeURIComponent(title)}`}
              className="inline-flex min-h-11 flex-none items-center rounded-full border border-line bg-surface px-3 text-xs text-ink-2 transition-colors hover:border-accent hover:text-accent-ink"
            >
              {title}
            </Link>
          ))}
        </div>
      </div>
    </div>
  );
}

export default async function Home() {
  const [stats, trending, site] = await Promise.all([
    fetchStats(),
    fetchTrending(),
    fetchSiteConfig(),
  ]);
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
          island on wide monitors (rails faded mid-title despite the
          empty screen around them). 54rem gives the search box and
          rails room to breathe; below xl the geometry is exactly
          what the one-viewport layout was tuned with. The hero type
          steps up with it (the logo is em-sized, so it follows). */}
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

        {rows.length > 0 && (
          <div id="trending" className="mt-6 scroll-mt-6 xl:mt-8">
            {rows.map((r) => (
              <TrendingRow key={r.label} label={r.label} titles={r.titles} />
            ))}
            <p className="mt-3 text-center text-xs text-faint">
              热门影视数据来自豆瓣
            </p>
          </div>
        )}
      </div>
    </main>
  );
}
