import type { Metadata } from "next";
import AdminShell, {
  AdminBackendErrorNotice,
  AdminDisabledNotice,
} from "@/components/admin/AdminShell";
import { fetchAdminOverview } from "@/lib/admin";
import { adminPageFetch } from "@/lib/admin-page";
import { formatCount, formatDate } from "@/lib/format";

export const metadata: Metadata = { title: "管理端概览" };

export default async function AdminOverviewPage() {
  const outcome = await adminPageFetch((token) => fetchAdminOverview(token));
  if (!outcome.ok) {
    return outcome.kind === "disabled" ? (
      <AdminDisabledNotice />
    ) : (
      <AdminBackendErrorNotice detail={outcome.detail} />
    );
  }
  const o = outcome.data;
  const s = o.stats ?? {};

  const cards = [
    { label: "种子总数", value: o.torrents },
    { label: "今日新增", value: o.today },
    { label: "未审核", value: o.pending_review },
    { label: "已审核", value: o.reviewed },
    { label: "已拉黑", value: o.blocked },
  ];

  return (
    <AdminShell active="overview">
      {/* Five stat cards, the same shape as the Polaris dashboard row. */}
      <section className="grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-5">
        {cards.map((c) => (
          <div
            key={c.label}
            className="rounded-xl border border-line bg-surface p-4 shadow-card"
          >
            <p className="text-xs text-mute">{c.label}</p>
            <p className="mt-2 text-2xl font-semibold tabular-nums text-ink">
              {formatCount(c.value)}
            </p>
          </div>
        ))}
        {/* Sixth cell on md (3-col) keeps the grid rectangular: DB info. */}
        <div className="rounded-xl border border-line bg-surface p-4 shadow-card md:block xl:hidden">
          <p className="text-xs text-mute">数据库</p>
          <p className="mt-2 text-2xl font-semibold text-ink">{o.dialect}</p>
        </div>
      </section>

      <div className="mt-6 grid gap-4 lg:grid-cols-2">
        <Section title="收录管线">
          <Row label="发现 infohash（seen）" value={s.seen} />
          <Row label="成功入库（fetched）" value={s.fetched} />
          <Row label="成人过滤" value={s.adult_filtered} />
          <Row label="垃圾过滤" value={s.spam_filtered} />
          <Row label="体积过滤" value={s.size_filtered} />
          <Row label="元数据超时" value={o.fetch?.timed_out} />
          <Row label="元数据失败" value={o.fetch?.failed} />
        </Section>

        <Section title="LLM 审核">
          <Row label="已审核" value={o.moderation?.reviewed} />
          <Row label="成人删除" value={o.moderation?.adult_removed} />
          <Row label="垃圾删除" value={o.moderation?.spam_removed} />
          <Row label="审核出错" value={o.moderation?.errors} />
          <Row label="待审核" value={o.moderation?.pending} />
          <Row label="黑名单总数" value={o.moderation?.blocked} />
        </Section>

        {o.crawler && (
          <Section title="爬虫状态">
            <div className="flex items-baseline justify-between gap-3 py-2">
              <span className="text-sm text-mute">状态</span>
              <span className="text-sm font-medium text-ink-2">
                {o.crawler.enabled ? "运行中" : "已禁用"}
              </span>
            </div>
            <Row label="已见 infohash" value={o.crawler.seen_infohashes} />
            <Row label="路由表节点" value={o.crawler.nodes} />
            <Row label="排队节点" value={o.crawler.queued_nodes} />
            <Row label="采样次数" value={o.crawler.sampled} />
            <Row label="采样出错" value={o.crawler.sample_errors} />
            <Row label="采样收获" value={o.crawler.harvested} />
          </Section>
        )}

        {o.scraper && (
          <Section title="Tracker 刮削排序">
            <Row label="队列深度" value={o.scraper.queue} />
            <Row label="已刮削" value={o.scraper.scraped} />
            <Row label="有 seeder" value={o.scraper.seeded} />
            <Row label="刮削出错" value={o.scraper.scrape_errors} />
            <Row label="丢弃" value={o.scraper.dropped} />
            <Row label="被挤出" value={o.scraper.evicted} />
          </Section>
        )}

        {o.trending && (
          <Section title="热门榜单（条数）">
            <Row label="欧美电影" value={o.trending.movies} />
            <Row label="美剧" value={o.trending.tv} />
            <Row label="日剧" value={o.trending.tv_jp} />
            <Row label="韩剧" value={o.trending.tv_kr} />
            <div className="flex items-baseline justify-between gap-3 py-2">
              <span className="text-sm text-mute">更新时间</span>
              <span className="text-sm tabular-nums text-ink-2">
                {o.trending.updated_at ? formatDate(o.trending.updated_at) : "-"}
              </span>
            </div>
          </Section>
        )}

        <Section title="数据库">
          <div className="flex items-baseline justify-between gap-3 py-2">
            <span className="text-sm text-mute">后端</span>
            <span className="text-sm text-ink-2">{o.dialect}</span>
          </div>
          <Row label="种子总数" value={o.torrents} />
          <Row label="今日新增" value={o.today} />
        </Section>
      </div>
    </AdminShell>
  );
}

function Section({
  title,
  children,
}: {
  title: string;
  children: React.ReactNode;
}) {
  return (
    <section className="rounded-xl border border-line bg-surface p-4 shadow-card">
      <h2 className="text-sm font-semibold text-ink">{title}</h2>
      <div className="mt-2 divide-y divide-line-2">{children}</div>
    </section>
  );
}

function Row({ label, value }: { label: string; value?: number }) {
  return (
    <div className="flex items-baseline justify-between gap-3 py-2">
      <span className="text-sm text-mute">{label}</span>
      <span className="text-sm font-medium tabular-nums text-ink-2">
        {formatCount(value)}
      </span>
    </div>
  );
}
