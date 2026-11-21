import type { Metadata } from "next";
import Link from "next/link";
import { fetchSiteConfig } from "@/lib/site";
import SiteLogo from "@/components/SiteLogo";
import AppearanceSwitcher from "@/components/AppearanceSwitcher";

// Public API documentation. Every endpoint, parameter, default and
// error string on this page was inventoried from the server
// implementation (server/internal/api) and verified against a running
// instance; the machine-readable twin of this page is /openapi.yaml.
// The internal first-party channel the site's own frontend uses is
// deliberately NOT documented here — it is not public surface.

export const metadata: Metadata = {
  title: "API 文档",
  description:
    "DHTSearch 公开 API：搜索、文件列表、相似推荐、统计、RSS 与举报接口的鉴权、参数与错误说明。",
};

const API = "https://api.example.com";

function Code({ children }: { children: string }) {
  return (
    <pre className="overflow-x-auto rounded-lg border border-line bg-soft p-3 font-mono text-xs leading-relaxed text-ink-2">
      {children}
    </pre>
  );
}

function InlineCode({ children }: { children: string }) {
  return (
    <code className="rounded-md bg-soft px-1.5 py-0.5 font-mono text-xs">
      {children}
    </code>
  );
}

function Section({
  id,
  title,
  children,
}: {
  id: string;
  title: string;
  children: React.ReactNode;
}) {
  return (
    <section id={id} className="mt-10 first:mt-0">
      <h2 className="text-lg font-semibold tracking-tight">{title}</h2>
      <div className="mt-3 space-y-3 text-sm leading-relaxed text-ink-2">
        {children}
      </div>
    </section>
  );
}

function Endpoint({
  method,
  path,
  gated,
  children,
}: {
  method: string;
  path: string;
  gated?: boolean;
  children: React.ReactNode;
}) {
  return (
    <div className="mt-6 first:mt-0">
      <div className="flex flex-wrap items-center gap-2">
        <span className="rounded-md bg-accent px-2 py-1 font-mono text-xs font-semibold text-white">
          {method}
        </span>
        <code className="font-mono text-sm font-medium text-ink">{path}</code>
        {gated ? (
          <span className="rounded-full border border-line px-2 py-0.5 text-xs text-mute">
            受令牌门控
          </span>
        ) : (
          <span className="rounded-full border border-line px-2 py-0.5 text-xs text-mute">
            无需令牌
          </span>
        )}
      </div>
      <div className="mt-3 space-y-3 text-sm leading-relaxed text-ink-2">
        {children}
      </div>
    </div>
  );
}

function ParamTable({
  rows,
}: {
  rows: { name: string; type: string; def: string; desc: string }[];
}) {
  return (
    <div className="overflow-x-auto rounded-lg border border-line">
      <table className="w-full min-w-[540px] text-left text-xs leading-relaxed">
        <thead>
          <tr className="border-b border-line bg-soft text-mute">
            <th className="px-3 py-2 font-medium">参数</th>
            <th className="px-3 py-2 font-medium">类型</th>
            <th className="px-3 py-2 font-medium">默认</th>
            <th className="px-3 py-2 font-medium">说明</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r.name} className="border-b border-line last:border-0">
              <td className="px-3 py-2 font-mono text-ink">{r.name}</td>
              <td className="px-3 py-2">{r.type}</td>
              <td className="px-3 py-2 font-mono">{r.def}</td>
              <td className="px-3 py-2">{r.desc}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

export default async function ApiDocsPage() {
  const site = await fetchSiteConfig();
  return (
    <main className="mx-auto w-full max-w-3xl px-4 py-8 sm:py-10">
      <header className="flex items-center justify-between gap-4">
        <Link
          href="/"
          className="inline-flex min-h-11 min-w-0 items-center text-xl font-bold tracking-tight hover:text-accent-ink"
        >
          <SiteLogo name={site.name} />
        </Link>
        <AppearanceSwitcher defaultAppearance={site.themeAppearance} />
      </header>

      <h1 className="mt-8 text-2xl font-bold tracking-tight sm:text-3xl">
        API 文档
      </h1>
      <p className="mt-3 text-sm leading-relaxed text-ink-2">
        {site.name}
        的公开只读 API：搜索本站基于 DHT 网络实时收录的磁力链接，读取种子文件列表与相似推荐，订阅
        RSS，或提交游客举报。接口返回 JSON（RSS 除外），支持跨域（
        <InlineCode>Access-Control-Allow-Origin: *</InlineCode>）。
        下文地址以 <InlineCode>{API}</InlineCode>{" "}
        代指部署的 API 服务地址，替换为实际地址即可。机器可读的
        OpenAPI 3 描述文件在{" "}
        <a
          href="/openapi.yaml"
          className="text-accent-ink underline decoration-accent-line underline-offset-2 hover:decoration-accent"
        >
          /openapi.yaml
        </a>
        。
      </p>

      <Section id="auth" title="鉴权与限流">
        <p>
          是否需要令牌由部署方设置（
          <InlineCode>PUBLIC_API_REQUIRE_TOKEN</InlineCode>
          ，默认关）。关闭时三个门控接口可匿名调用；开启后必须携带令牌。无论开关如何，
          <strong>令牌门控只作用于</strong>{" "}
          <InlineCode>/api/search</InlineCode>、<InlineCode>/api/stats</InlineCode>、
          <InlineCode>/api/trending</InlineCode>{" "}
          三个接口；文件列表、相似推荐、RSS、站点信息、健康检查与举报接口不要求令牌。
        </p>
        <p>
          <strong>获取令牌</strong>：由站点管理员在管理端「API 令牌」页创建。令牌形如{" "}
          <InlineCode>dht_</InlineCode> 加 64 位十六进制字符（共 68
          字符），明文只在创建时显示一次，之后只能看前缀。每个令牌有独立的每分钟配额（创建时默认
          600 次/分钟，管理员可逐个修改，0 = 不限）。
        </p>
        <p>
          <strong>携带方式</strong>（二选一，不支持查询参数传令牌）：
        </p>
        <Code>{`Authorization: Bearer <redacted>
# 或
X-API-Key: dht_xxxxxxxx...`}</Code>
        <p>
          鉴权失败按原因区分（响应体均为{" "}
          <InlineCode>{'{"error": "..."}'}</InlineCode>）：
        </p>
        <ParamTable
          rows={[
            { name: "401", type: "未带令牌", def: "api token required", desc: "门控开启且请求未携带令牌；同时返回 WWW-Authenticate 头" },
            { name: "401", type: "令牌无效", def: "invalid api token", desc: "令牌不存在或已删除" },
            { name: "403", type: "令牌被禁用", def: "api token disabled", desc: "令牌存在但已被管理员禁用" },
            { name: "429", type: "令牌配额用尽", def: "api token rate limit exceeded", desc: "超过该令牌的每分钟配额；响应带 Retry-After" },
            { name: "503", type: "公开 API 被关闭", def: "public api disabled", desc: "部署方关闭了公开 API 总开关，三个门控接口对所有人返回 503" },
          ]}
        />
        <p>
          <strong>全局限流</strong>：除令牌配额外，所有公开接口共享按客户端
          IP 的限流（默认每秒 3 次、突发 30 次，部署方可调），超限返回 429{" "}
          <InlineCode>rate limit exceeded</InlineCode> 并带{" "}
          <InlineCode>Retry-After: 1</InlineCode>
          。带令牌不豁免全局限流。搜索类接口在服务器饱和时可能返回 503{" "}
          <InlineCode>server busy</InlineCode> 或{" "}
          <InlineCode>search timed out</InlineCode>，稍后重试即可。
        </p>
      </Section>

      <Section id="endpoints" title="接口参考">
        <Endpoint method="GET" path="/api/search" gated>
          <p>
            关键词搜索。无关键词时为浏览最新收录。没有独立的种子详情接口——搜索结果条目即完整记录（名称、大小、文件预览、磁力链接、做种数据），文件全量列表与相似推荐见下方两个接口。
          </p>
          <ParamTable
            rows={[
              { name: "q", type: "string", def: "（空）", desc: "关键词，最长 200 字符（超出截断）；空 = 浏览最新" },
              { name: "page", type: "int", def: "1", desc: "页码，从 1 起；可达深度上限为偏移 10000 条，超出自动钳制" },
              { name: "page_size", type: "int", def: "20", desc: "每页条数，最大 100" },
              { name: "category", type: "string", def: "（全部）", desc: "video / audio / image / doc / software / archive / other；非法值 400" },
              { name: "sort", type: "string", def: "relevance", desc: "relevance（相关性）/ newest（最新）/ size（体积）/ seeders（做种数）；非法值 400" },
              { name: "hide_dead", type: "string", def: "0", desc: "1 或 true 时隐藏 health=likely_dead 的结果；其他取值 400" },
            ]}
          />
          <Code>{`curl "${API}/api/search?q=shawshank&sort=seeders&page_size=2"`}</Code>
          <Code>{`{
  "total": 2,
  "page": 1,
  "page_size": 2,
  "category": "",
  "sort": "seeders",
  "hide_dead": false,
  "categories": { "video": 2, "audio": 0, "image": 0, "doc": 0,
                  "software": 0, "archive": 0, "other": 0 },
  "trackers": ["udp://tracker.opentrackr.org:1337/announce", "..."],
  "results": [
    {
      "info_hash": "4cd02e56c0237fcd5190dc9e9430592040431aaa",
      "name": "The Shawshank Redemption 1994 2160p UHD BluRay REMUX",
      "total_size": 58000000000,
      "file_count": 1,
      "files": [
        { "path": "The.Shawshank.Redemption.1994.2160p.UHD.BluRay.REMUX.mkv",
          "size": 58000000000 }
      ],
      "magnet": "magnet:?xt=urn:btih:4cd0...&dn=...&tr=...（已截短）",
      "created_at": 1795236227,
      "category": "video",
      "seeders": 41, "leechers": 9, "completed": 233,
      "scraped_at": 1795235327, "zero_since": 0, "zero_count": 0,
      "health": "alive"
    }
  ]
}`}</Code>
          <p>
            结果条目字段：<InlineCode>files</InlineCode> 只内嵌前 100
            个文件作预览（全量走文件列表接口）；<InlineCode>alias</InlineCode>{" "}
            为订阅源发布的别名标题，非空时才出现；
            <InlineCode>seeders</InlineCode> / <InlineCode>leechers</InlineCode> /{" "}
            <InlineCode>completed</InlineCode> / <InlineCode>scraped_at</InlineCode> /{" "}
            <InlineCode>zero_since</InlineCode> / <InlineCode>zero_count</InlineCode>{" "}
            是最近一次 tracker 刮削快照，从未被刮削过的种子这些字段为{" "}
            <InlineCode>null</InlineCode>（与真实的 0 严格区分）；
            <InlineCode>health</InlineCode> 是由快照推导的健康度：{" "}
            <InlineCode>alive</InlineCode>（有做种）/{" "}
            <InlineCode>no_seed</InlineCode>（无做种但有人下载）/{" "}
            <InlineCode>likely_dead</InlineCode>（连续多次全零、跨度 ≥48 小时）/{" "}
            <InlineCode>suspect</InlineCode>（全零但证据不足）/{" "}
            <InlineCode>unknown</InlineCode>（无刮削数据）。
            <InlineCode>created_at</InlineCode> 为收录时间（Unix 秒）。
          </p>
        </Endpoint>

        <Endpoint method="GET" path="/api/torrents/{hash}/files">
          <p>分页读取一个种子的完整文件列表。</p>
          <ParamTable
            rows={[
              { name: "page", type: "int", def: "1", desc: "页码，从 1 起" },
              { name: "page_size", type: "int", def: "100", desc: "每页条数，最大 500" },
              { name: "q", type: "string", def: "（空）", desc: "按文件路径子串过滤；total 统计过滤后的条数" },
            ]}
          />
          <Code>{`curl "${API}/api/torrents/0f09bec639b50b57e0c817048a1eef0bfda69e2e/files?page=1&page_size=2"`}</Code>
          <Code>{`{
  "info_hash": "0f09bec639b50b57e0c817048a1eef0bfda69e2e",
  "total": 3,
  "total_all": 3,
  "page": 1,
  "page_size": 2,
  "total_pages": 2,
  "files": [
    { "path": "The.Shawshank.Redemption.1994.1080p.BluRay.x264.mkv", "size": 2140000000 },
    { "path": "The.Shawshank.Redemption.1994.1080p.BluRay.x264.srt", "size": 103456 }
  ]
}`}</Code>
          <p>
            <InlineCode>total_all</InlineCode>{" "}
            是该种子的文件总数（未过滤）；哈希不存在返回 404。
          </p>
        </Endpoint>

        <Endpoint method="GET" path="/api/torrents/{hash}/similar">
          <p>
            与指定种子名称相近的推荐，结果条目结构与搜索一致，但{" "}
            <InlineCode>files</InlineCode> 为 <InlineCode>null</InlineCode>。
          </p>
          <ParamTable
            rows={[
              { name: "limit", type: "int", def: "6", desc: "返回条数，钳制在 1..12" },
            ]}
          />
          <Code>{`curl "${API}/api/torrents/0f09bec639b50b57e0c817048a1eef0bfda69e2e/similar"`}</Code>
          <Code>{`{
  "results": [
    {
      "info_hash": "4cd02e56c0237fcd5190dc9e9430592040431aaa",
      "name": "The Shawshank Redemption 1994 2160p UHD BluRay REMUX",
      "total_size": 58000000000,
      "file_count": 1,
      "files": null,
      "magnet": "magnet:?xt=urn:btih:4cd0...&dn=...&tr=...（已截短）",
      "created_at": 1795236227,
      "category": "video",
      "seeders": 41, "leechers": 9, "completed": 233,
      "scraped_at": 1795235327, "zero_since": 0, "zero_count": 0,
      "health": "alive"
    }
  ]
}`}</Code>
          <p>哈希不存在返回 404。</p>
        </Endpoint>

        <Endpoint method="GET" path="/api/stats" gated>
          <p>
            索引与管线统计（服务端缓存约 10
            秒）。顶层为累计计数：收录总数、已发现、已抓取与各过滤类别计数；
            <InlineCode>fetch</InlineCode> 是元数据抓取结果（含按来源{" "}
            <InlineCode>by_source</InlineCode> 拆分与实时队列深度）；
            <InlineCode>moderation</InlineCode> 是审核计数。部署启用了对应子系统时还会出现{" "}
            <InlineCode>crawler</InlineCode> / <InlineCode>scraper</InlineCode> /{" "}
            <InlineCode>feeds</InlineCode> 段。
          </p>
          <Code>{`curl "${API}/api/stats"`}</Code>
          <Code>{`{
  "torrents": 3,
  "seen": 0, "fetched": 0,
  "adult_filtered": 0, "spam_filtered": 0,
  "size_filtered": 0, "blocked_filtered": 0,
  "fetch": {
    "fetched": 0, "timed_out": 0, "failed": 0, "prescreen_dropped": 0,
    "skipped_stored": 0, "skipped_backoff": 0,
    "peer_hint_items": 0, "peer_hint_fetched": 0,
    "by_source": [
      { "source": "dht", "attempts": 0, "fetched": 0, "timed_out": 0,
        "failed": 0, "prescreen_dropped": 0, "success_rate": 0 }
    ],
    "queue_depth": 0, "backend": "anacrolix"
  },
  "moderation": { "reviewed": 0, "adult_removed": 0, "spam_removed": 0,
                  "errors": 0, "pending": 3, "blocked": 0 }
}`}</Code>
        </Endpoint>

        <Endpoint method="GET" path="/api/trending" gated>
          <p>
            首页榜单数据：豆瓣热门影视标题（英文名为主，日韩剧为中文名）与更新时间（Unix 秒）。
          </p>
          <Code>{`curl "${API}/api/trending"`}</Code>
          <Code>{`{
  "movies": ["The Odyssey", "..."],
  "tv": ["VisionQuest", "..."],
  "tv_jp": ["白色巨塔", "..."],
  "tv_kr": ["百日谎言", "..."],
  "updated_at": 1795200000
}`}</Code>
        </Endpoint>

        <Endpoint method="GET" path="/api/site">
          <p>站点展示配置：名称、主题色与默认外观。</p>
          <Code>{`curl "${API}/api/site"`}</Code>
          <Code>{`{ "name": "DHTSearch", "theme_color": "teal", "theme_appearance": "system" }`}</Code>
        </Endpoint>

        <Endpoint method="GET" path="/api/healthz">
          <p>健康检查，不受任何门控与限流影响。</p>
          <Code>{`curl "${API}/api/healthz"
# { "ok": true }`}</Code>
        </Endpoint>

        <Endpoint method="GET" path="/rss">
          <p>
            RSS 2.0 订阅：某关键词的最新收录（最多 50 条，按最新排序），无{" "}
            <InlineCode>q</InlineCode> 时为全站最新。每条的链接即磁力链接，{" "}
            <InlineCode>guid</InlineCode> 为 infohash。
          </p>
          <ParamTable
            rows={[
              { name: "q", type: "string", def: "（空）", desc: "关键词，最长 200 字符" },
              { name: "category", type: "string", def: "（全部）", desc: "同 /api/search 的分类枚举；非法值 400" },
            ]}
          />
          <Code>{`curl "${API}/rss?q=ubuntu"`}</Code>
          <Code>{`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel>
  <title>DHTSearch 搜索：ubuntu</title>
  <link>https://api.example.com/</link>
  <description>基于 DHT 网络实时收录的磁力链接，已过滤成人内容与垃圾信息。</description>
  <item>
    <title>Ubuntu 24.04 LTS Desktop amd64</title>
    <link>magnet:?xt=urn:btih:a99dfe...&amp;dn=...&amp;tr=...（已截短）</link>
    <guid>a99dfe6400d029b848d18a9b6ff1611af03aadf8</guid>
    <pubDate>Fri, 21 Nov 2026 05:23:47 +0000</pubDate>
    <description>大小 5880.00 MB · 1 个文件</description>
    <category>software</category>
  </item>
</channel></rss>`}</Code>
        </Endpoint>

        <Endpoint method="POST" path="/api/report">
          <p>
            游客举报一个已收录种子，进入管理员的举报队列。同一客户端对同一种子的举报有频率限制（约
            3 次/分钟），超限 429。
          </p>
          <ParamTable
            rows={[
              { name: "info_hash", type: "string", def: "（必填）", desc: "40 位十六进制 infohash" },
              { name: "reason", type: "string", def: "（必填）", desc: "adult（成人内容）/ spam（垃圾信息）/ infringement（侵权）/ other（其他）" },
              { name: "note", type: "string", def: "（空）", desc: "补充说明，最长 500 字符" },
            ]}
          />
          <Code>{`curl -X POST "${API}/api/report" \\
  -H "Content-Type: application/json" \\
  -d '{"info_hash":"0f09bec639b50b57e0c817048a1eef0bfda69e2e","reason":"spam","note":"重复垃圾"}'
# 201 { "ok": true }`}</Code>
        </Endpoint>
      </Section>

      <Section id="errors" title="错误模型">
        <p>
          所有 JSON 错误响应体统一为{" "}
          <InlineCode>{'{"error": "消息"}'}</InlineCode>
          ，状态码与常见消息如下：
        </p>
        <ParamTable
          rows={[
            { name: "400", type: "参数错误", def: "invalid category / invalid sort / invalid hide_dead / invalid info_hash / invalid reason / bad request", desc: "参数不合法，按消息修正即可" },
            { name: "401", type: "未鉴权", def: "api token required / invalid api token", desc: "见「鉴权与限流」" },
            { name: "403", type: "令牌被禁用", def: "api token disabled", desc: "联系站点管理员" },
            { name: "404", type: "不存在", def: "not found", desc: "哈希对应的种子不存在" },
            { name: "429", type: "限流", def: "rate limit exceeded / api token rate limit exceeded / too many reports for this torrent", desc: "按 Retry-After 等待后重试" },
            { name: "500", type: "服务器错误", def: "search failed / stats failed / report failed / feed failed", desc: "服务端内部错误" },
            { name: "503", type: "暂不可用", def: "public api disabled / server busy / search timed out", desc: "公开 API 被关、服务器饱和或查询超时，稍后重试" },
          ]}
        />
      </Section>
    </main>
  );
}
