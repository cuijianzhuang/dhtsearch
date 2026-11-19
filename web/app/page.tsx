import Link from "next/link";
import { fetchStats, fetchTrending } from "@/lib/api";
import { fetchSiteConfig } from "@/lib/site";
import { formatCount } from "@/lib/format";
import SearchBox from "@/components/SearchBox";
import SiteLogo from "@/components/SiteLogo";
import AppearanceSwitcher from "@/components/AppearanceSwitcher";

function TrendingRow({ label, titles }: { label: string; titles: string[] }) {
  return (
    <div className="mt-3 flex flex-wrap items-center justify-center gap-2">
      <span className="text-xs text-mute">{label}</span>
      {titles.map((title) => (
        <Link
          key={title}
          href={`/search?q=${encodeURIComponent(title)}`}
          className="inline-flex min-h-8 items-center rounded-full border border-line bg-surface px-3 text-xs text-ink-2 transition-colors hover:border-accent hover:text-accent-ink"
        >
          {title}
        </Link>
      ))}
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
    <main className="relative flex flex-1 flex-col items-center justify-center px-4 py-16">
      <div className="absolute right-4 top-4">
        <AppearanceSwitcher defaultAppearance={site.themeAppearance} />
      </div>
      <div className="w-full max-w-2xl">
        <h1 className="text-center text-4xl font-bold tracking-tight sm:text-5xl">
          <SiteLogo name={site.name} />
        </h1>
        <p className="mt-4 text-center text-sm text-mute sm:text-base">
          干净、无广告的磁力链接搜索引擎
          <br className="sm:hidden" />
          <span className="hidden sm:inline"> · </span>
          基于 DHT 网络实时收录，自动过滤成人内容与垃圾信息
        </p>

        <div className="mt-10">
          <SearchBox large />
        </div>

        <p className="mt-4 text-center text-xs text-mute">
          直接输入关键词搜索，留空则浏览最新收录的资源
        </p>

        {rows.length > 0 && (
          <div className="mt-8">
            {rows.map((r) => (
              <TrendingRow key={r.label} label={r.label} titles={r.titles} />
            ))}
            <p className="mt-2 text-center text-xs text-faint">
              热门影视数据来自豆瓣
            </p>
          </div>
        )}
      </div>

      <footer className="mt-16 text-center text-xs text-mute">
        {stats ? (
          <p>
            已收录 {formatCount(stats.torrents)} 条 · 已过滤成人内容{" "}
            {formatCount(stats.adult_filtered)} 条 · 垃圾信息{" "}
            {formatCount(stats.spam_filtered)} 条
            {stats.crawler_running === false && (
              <span className="ml-2 text-warn">（爬虫暂停中）</span>
            )}
          </p>
        ) : (
          <p>索引统计暂时不可用（后端未连接）</p>
        )}
      </footer>
    </main>
  );
}
