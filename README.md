# DHTSearch

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

> Co-authored by [Kimi K3](https://www.kimi.com/).

一个自建索引的磁力链接搜索网站：Go 后端通过 DHT 网络爬虫实时采集磁力链接元数据，自动过滤成人内容与垃圾信息，Next.js 前端提供干净的搜索界面。

## 架构

```mermaid
flowchart TB
    DHT["Public BitTorrent DHT nodes"]
    Crawler["DHT crawler<br/>passive listen + BEP-51 sampling"]
    Scraper["Tracker scrape ranking<br/>BEP 15 batch scrape"]
    Dedup{"Already indexed<br/>or blocked?"}
    Skipped["Skip before any network work<br/>fetch slot goes to the next hash"]
    Fetcher["Metadata fetch<br/>BEP-9 workers<br/>timeout scaled by seeders"]
    Filter{"Filter engine<br/>keywords + heuristics + size"}
    Discard["Drop and count<br/>adult / spam / too small"]
    DB[("SQLite")]
    Web["Next.js frontend"]
    Mod["LLM moderation<br/>OpenRouter free model"]
    Blocked[("blocked table")]

    Crawler <-->|UDP| DHT
    Crawler -->|new infohashes| Scraper
    Scraper -->|ranked by seeders| Dedup
    Dedup -->|yes| Skipped
    Dedup -->|no| Fetcher
    Fetcher --> Filter
    Filter -->|hit| Discard
    Filter -->|pass| DB
    Web <-->|REST API| DB
    DB -->|hourly incremental review| Mod
    Mod -->|adult/spam: delete| Blocked
    Mod -->|title ad-trim: clean_name| DB
    Blocked -.->|rejected on insert, no resurrection| DB
```

- `server/` — Go 后端：DHT 爬虫（anacrolix/dht/v2）、BEP-9 元数据获取（anacrolix/torrent）、过滤引擎（中英文成人词表 + 垃圾启发式）、SQLite 存储（modernc.org/sqlite，无 cgo，FTS5 trigram 全文索引）、REST API（标准库 net/http）
- `web/` — Next.js 前端（App Router + Tailwind，服务端渲染搜索结果）

### 发现速度

infohash 来自两条路径，**主动的那条决定吞吐**：

- **被动**：别的节点发来的 `get_peers` / `announce_peer`。只在公网可达时才有量，NAT/VPN 后面基本为零。
- **主动（BEP-51）**：`DHT_SAMPLERS` 个 worker 并发对路由表里的节点发 `sample_infohashes`，每个节点一次能返回几十个 infohash。查询带随机 `target`，回包里的节点又会入队，于是采样本身就是一次 ID 空间游走；每个节点按它自己声明的 `interval`（钳在 1 分钟～2 小时）重采。
- 路由表由 `TableMaintainer` 维护 + 定时随机 `find_node` 拓宽——**路由表空了发现就会停**，`/api/stats` 的 `crawler.nodes` 就是看这个的。

`crawler.harvested / crawler.sampled` 是每次采样的平均产出；`crawler.nodes` 归零就说明发现要停。

**发现从来不是瓶颈，元数据获取才是。** 8 个 sampler 每分钟就能挖出约 3000 个 infohash，而获取端每分钟只消化得掉一百多个——99% 的发现结果直接被丢弃。三个参数互相抢带宽，实测（2 vCPU / 4 GB VPS，指标是每分钟入库数）：

| 改动 | 结果 |
| --- | --- |
| `META_WORKERS` 64 → 128 | 20/min → 33/min |
| `META_WORKERS` 128 → 256 | 33/min → 27/min（争用反噬） |
| `DHT_SAMPLERS` 32 → 8 | 25/min → 33/min（成功率 10% → 20%） |
| `META_TIMEOUT` 45s → 20s | 33/min → **2.4/min**（成功率塌到 0.35%） |

最后一条最反直觉：找 peer 本身就占掉大部分等待时间，**能成功的获取本来就是慢的那些**，缩超时等于把它们全砍掉。换硬件请照着 `/api/stats` 重新测，别照抄。

不过「一刀切地缩超时」之所以是灾难，是因为它分不清哪些等待值得。scraper 拿到的
seeder 数正好提供了这个区分，所以 `META_TIMEOUT` 现在只是基准值，实际预算按 swarm
大小缩放：0 seeder（几个大 tracker 都没听说过）拿 1/3，1–4 个拿 2/3，5 个以上拿
4/3。没经过 scrape 的 infohash（`SCRAPE_ENABLED=false`）一律用原值，不做任何缩减。

另外，取元数据前会先查一次库：爬虫的内存去重环只有 26 万条，按上面的发现速度约
1.5 小时就轮空一遍，之后已入库的热门 hash 会被当成新发现重走一遍完整流水线；被
moderation 删掉的 hash 更是每轮都要重新花 45 秒取一次，最后才在 `Upsert` 被
`blocked` 表拒绝。一次主键查询就能把这些 worker 槽位换回吞吐，命中数见
`/api/stats` 的 `fetch.skipped`。

### Tracker 刮削排序

既然 99% 的发现结果注定被丢弃，**丢谁**就决定了索引长成什么样。默认开启的
scraper（`SCRAPE_ENABLED`）在爬虫和获取端之间插了一层：把新发现的 infohash 攒成
批（一个 UDP 包最多问 ~70 个），向几个大型公共 tracker 发 BEP 15 scrape 拿到
seeder 数，然后按 seeder 数从高到低喂给获取端——热门资源（多为影视内容）优先，
0 seeder 的只是排队靠后，不会被直接扔掉；队列满了先踢 seeder 最少的，等价于把
原来的「随机丢」换成「有依据地丢」。热门资源 peer 多，获取成功率也更高，排序本
身就在提升吞吐。

同一份 `TRACKERS` 列表还会以 `&tr=` 附在获取端的磁力链接上：tracker 一次往返就
能拿到 peer 列表，省掉吃掉大半 `META_TIMEOUT` 的 DHT 找 peer 游走。

看 `/api/stats` 的 `scraper` 段：`seeded/scraped` 是命中率（有 seeder 的占比），
`queue` 是排队深度，`evicted` 是被挤掉的低优先级 hash，`scrape_errors` 持续增长
说明某个 tracker 挂了（自动重连，也可换 `TRACKERS`）。

## 快速开始

### 后端

```bash
cp env.example .env           # 填入 OPENAI_API_KEY（.env 已 gitignore）

cd server

# 完整模式：启动 DHT 爬虫（需要 UDP 出站，索引随时间增长）
go run ./cmd/server

# 演示模式：不爬 DHT，插入演示数据，便于本地验证 API/前端
CRAWL_ENABLED=false go run ./cmd/server --seed-demo
```

默认监听 `:8080`。配置项（环境变量或同名 flag）：

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `HTTP_ADDR` | `:8080` | HTTP 监听地址 |
| `DB_PATH` | `./dhtsearch.db` | SQLite 路径 |
| `CRAWL_ENABLED` | `true` | 是否启动 DHT 爬虫 |
| `DHT_PORT` | `0`（随机） | DHT UDP 端口 |
| `DHT_SAMPLERS` | `8` | BEP-51 并发采样 worker 数（调大反而降低入库速度，见上） |
| `META_WORKERS` | `128` | 元数据并发 worker 数（入库速度的主要旋钮） |
| `META_TIMEOUT` | `45s` | 元数据获取超时基准值（按 seeder 数缩放，见上） |
| `FETCH_METADATA` | `true` | 是否获取元数据（false 时只收 infohash） |
| `MIN_TORRENT_SIZE` | `104857600`（100 MiB） | 低于此总体积的种子不入库 |
| `FILTER_ADULT` | `true` | 是否过滤成人内容（`false` 时入库，且 LLM 审核也不再删除，见下） |
| `ENV_FILE` | `.env` | .env 文件路径（相对工作目录） |
| `RATE_LIMIT_RPS` | `3` | 每客户端 IP 的持续请求速率（令牌桶，0 = 关闭限流） |
| `RATE_LIMIT_BURST` | `30` | 令牌桶突发容量 |
| `SEARCH_MAX_INFLIGHT` | `16` | 并发搜索上限，超出直接 503 甩负载 |
| `SEARCH_TIMEOUT` | `10s` | 单次搜索的数据库时间预算 |
| `ADMIN_PASSWORD` | 无 | 后台控制台口令，留空则整个控制台不挂载 |
| `ADMIN_SECURE` | `true` | 会话 cookie 是否标记 Secure（仅在内网走 HTTP 时才关） |

LLM 审核相关（见下文「LLM 二次审核」）：

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `OPENAI_API_KEY` | 无 | **必填**，未设置则审核不启动 |
| `OPENAI_BASE_URL` | `https://openrouter.ai/api/v1` | 任意 OpenAI 兼容端点 |
| `OPENAI_MODEL` | `nvidia/nemotron-3-super-120b-a12b:free` | 审核模型（`:free` 后缀为 OpenRouter 免费模型） |
| `MODERATION_ENABLED` | `true` | 总开关 |
| `MODERATION_INTERVAL` | `1h` | 审核周期 |
| `MODERATION_BATCH_SIZE` | `100` | 每次请求送审的标题数 |
| `MODERATION_MAX_BATCHES` | `20` | 每轮最多几批（0 = 不限），用于封顶开销 |
| `MODERATION_DRY_RUN` | `false` | 只打日志不删除 |
| `MODERATION_TRIM_TITLES` | `true` | 顺带清理标题里的广告文字 |

### 前端

```bash
cd web
cp .env.example .env.local   # NEXT_PUBLIC_API_BASE=http://localhost:8080
npm install
npm run dev                  # 开发
# 或 npm run build && npm run start   # 生产
```

## 后台控制台

设了 `ADMIN_PASSWORD` 才会挂载，访问 `/admin`。**留空时连路由都不注册**——没有口令还能进的后台，比没有后台更糟。

页面由 Go 二进制自己伺服（`go:embed` 的单文件），不进 Next.js 打包。这不是偷懒：
公开 API 对所有响应发 `Access-Control-Allow-Origin: *`，而通配符和携带 cookie 的
请求互斥，浏览器会直接拒绝发送会话 cookie。同源伺服把这个问题整个绕开了。

能做的事：

- **看板**：收录量、发现/取元数据/超时/跳过、各类过滤计数、DHT 节点数与采样成功率、
  刮削命中率与队列深度、审核进度与黑名单规模。数据直接复用 `/api/stats` 的响应体，
  所以看板和公开接口永远不会对不上。
- **改运行时配置**：成人内容开关、最小种子体积、审核总开关/dry-run/标题清理。
  **改动写进数据库并立刻生效**，重启后仍在——环境变量只在数据库从未存过该键时充当
  初值。反过来说，改完环境变量重启是不会覆盖控制台里设过的值的，要改回去请在控制台改。
- **写操作**：按 infohash 删除（可同时拉黑）、解封单个 hash、按原因批量解封、
  立即跑一轮 LLM 审核、重置全库审核标记。
- 面板里还会列出 `META_WORKERS`、`DHT_SAMPLERS` 等**只读**项。它们在组件构建时读取
  一次，做成可改的控件只会显示一个管道并没有在用的值，所以这里只展示、不提供修改。

安全设计：

- 口令用常数时间比较，**连错 5 次锁 15 分钟**（锁定期间正确口令也不放行，否则拦不住
  在线爆破）。计数按客户端 IP，且只有来自回环/内网的对端才采信 `X-Forwarded-For`。
- 会话 cookie 是 `HttpOnly` + `SameSite=Strict` + `Secure`，服务端保存，退出即失效。
- 所有写操作额外要求 `X-Admin-CSRF` 头（双提交令牌）。`SameSite=Strict` 是主防线，
  这一层挡的是忽略 SameSite 的浏览器——跨站表单提交设不了自定义头。
- 页面带 `default-src 'none'` 的 CSP 和 `frame-ancestors 'none'`，且所有服务端数据
  都用 `textContent` 注入。种子名是攻击者可控的，这个页面不能是最终把它当成标记渲染的地方。
- **每一次写操作都记审计日志**，含来源 IP：`admin[1.2.3.4]: set filter_adult = false`。

反向代理要把两个前缀转给 Go 服务（并传 `X-Forwarded-For`，否则锁定会算在代理头上）：

```nginx
location /admin      { proxy_pass http://127.0.0.1:8081; proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for; }
location /api/admin/ { proxy_pass http://127.0.0.1:8081; proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for; }
```

口令是暴露在公网上的单一共享密钥，建议用 `openssl rand -base64 24` 生成，并放进
systemd 的 `EnvironmentFile`（`deploy.sh` 不会上传 `.env`）。

## API

- `GET /api/search?q=xx&page=1&page_size=20` — 搜索（q 为空返回最新收录），结果含拼好的 magnet 链接。`total_capped` 为真时 `total` 是下界；每条结果最多带 10 个文件条目，真实数量看 `file_count`
- `GET /api/stats` — 收录数、成人/垃圾/体积过滤计数、LLM 审核统计、爬虫状态
- `GET /api/healthz` — 健康检查

## 搜索索引

关键词搜索走 SQLite 的 FTS5 全文索引（`torrents_fts`，外部内容表，不复制正文），
分词器用 **trigram**——它是唯一保住原有语义的选项：匹配标题里任意位置的子串，中文
不需要分词。数据库首次打开时自动建表回填，三个触发器保证之后的增删改同步；建不出
来（构建里没有 FTS5）就退回原来的扫表路径，结果完全一致，只是慢。

两个要点：

- **索引只负责缩小候选集，不负责定义什么算命中**。LIKE 条件照样叠在 MATCH 上执行，
  所以有没有索引返回的结果逐条相同——这条有测试覆盖（`TestSearchFTSAgreesWithScan`）。
- **trigram 索引 3 个字符的序列，看不见更短的词**，而且是静默返回 0 条而非报错。
  「三体」「沙丘」「4K」这类两字查询在这里是家常便饭，所以短词不进 MATCH，交给
  LIKE 过滤；全部关键词都短于 3 字符时整体退回扫表。

排序用 `ORDER BY rowid DESC` 而不是 `created_at DESC`：rowid 按插入顺序递增而
`created_at` 是插入时打的戳，两者本来就同序，但走 rowid 能让扫描填满一页就停，而不
是把所有命中都物化出来再排序。代价是增量同步（`sync-to-remote.sh`）会把 `created_at`
较早的行插在后面，让两个顺序略微脱节，所以取回的一页会在 Go 里按 `created_at` 重排
一次——页内顺序精确，页边界仍是近似的，这本来也是 `created_at` 排序唯一承诺过的。

300k 行实测（同机取三次最好成绩）：

| 场景 | 扫表 | FTS5 + rowid DESC |
| --- | ---: | ---: |
| 常见词翻页（81k 命中） | 88µs | 702µs |
| 罕见词（1 条命中，在最老的行） | 292ms | 347µs |
| 零命中（拼错的片名） | 282ms | 237µs |

扫表在命中密集时靠早退出赢，但**最坏情况是罕见词和零命中**——必须扫完全表才能确定
「没有」，而且随索引线性增长。那恰好是攻击者会打的路径，也正是索引把它压到亚毫秒的
理由。索引本身约占表体积的 79%。

结果总数也不再精确统计：`COUNT(*)` 配 LIKE 在 300k 行要 141ms，而翻页深度上限只有
10000 行，数到更远也没人看得到。现在数到 10100 就停，响应里的 `total_capped` 告诉
前端这是下界，界面显示成「10,100+」。

## DoS 防护

搜索是无鉴权接口，所以 API 层内置了四道闸门，全部可用环境变量调整（见上表）：

- **每 IP 限流**：令牌桶（默认持续 3 req/s、突发 30），超出返回 429 + `Retry-After`。真实客户端 IP 只信任来自回环/内网地址（反向代理或同机 Next SSR 进程）的 `X-Forwarded-For`，公网直连时伪造头无效；IPv6 按 /64 计桶，防止单机用整段地址刷新桶。`/api/healthz` 不限流，监控和部署健康检查不会被洪水挤掉。
- **搜索准入**：并发搜索上限（默认 16），等不到槽位（1 秒）直接 503 甩负载。读走独立的连接池（WAL 下读不阻塞写），写仍然是单连接，排队再深也只是白占内存。
- **单查询预算**：每次搜索默认 10s 超时，客户端断开即取消，慢查询不会继续占着数据库；关键词最多取前 8 个（每个关键词都让每行多算两次 LIKE），翻页深度上限 10000 行（`OFFSET` 越深扫描越贵，页号在相乘前就钳住，避免溢出成负数绕过检查），查询串按 UTF-8 边界截断到 200 字节，结果总数最多数到 10100。
- **HTTP 层**：Read/Write/Idle 超时加 16 KB 请求头上限，挡 slowloris 和超大头攻击。

另外 `/api/stats` 和首页总数走 10 秒 TTL 的单飞缓存（并发未命中只有一个请求去查库），聚合扫描每周期最多一次；`created_at` 索引让空查询（首页默认请求）从全表排序变成走索引取前 20 行。

前端 SSR 请求会把入站的 `X-Forwarded-For` / `X-Real-IP` 透传给后端，限流按真实访客计——注意反向代理需设置这两个头（nginx: `proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;`）。

**站点在 Cloudflare 后面时必须多配一步**：此时 nginx 的 `$remote_addr` 是 CF 边缘节点的公网 IP，会被后端当成「真实客户端」，限流就打在了被众多访客共享的 CF 节点上。用 realip 模块从 `CF-Connecting-IP` 还原访客 IP（只信任 [Cloudflare 官方网段](https://www.cloudflare.com/ips/)）：

```nginx
# /etc/nginx/conf.d/cloudflare-real-ip.conf
# 对 cloudflare.com/ips-v4 与 ips-v6 里的每个网段各写一行：
set_real_ip_from 173.245.48.0/20;
# ...
real_ip_header CF-Connecting-IP;
```

直连绕过 CF 的流量不在信任网段内，`CF-Connecting-IP` 伪造无效，仍按对端真实地址限流。CF 网段偶有更新，建议定期比对官方列表。

### Cloudflare Bot Challenge

生产站点可在 Cloudflare WAF Custom Rules 中对公开搜索页启用
**Managed Challenge**，让可疑客户端在请求到达 Next.js 和 Go 后端前完成验证：

```text
http.host eq "search.example.com" and
http.request.uri.path eq "/search" and
not cf.client.bot
```

把这条规则放在任何 `skip` 规则之前。搜索表单和翻页链接使用完整文档导航，
而不是 Next.js 客户端路由；Cloudflare 的 interstitial challenge 是 HTML 页面，
若它落进 RSC/fetch 请求，浏览器无法正常展示。验证通过后 Cloudflare 用
`cf_clearance` cookie 放行后续搜索。若 Go API 也直接暴露在公网，还应把公开
API 搜索路径加入同一防护规则；仅监听回环地址时则不需要。

## 过滤策略

### 第一道：入库前的静态过滤

- **成人内容**：内置 230+ 中英文关键词（短词用词边界正则防误伤，如 Avatar/Avengers 不会被 "av" 误拦），对资源名和文件名匹配；JAV 番号等弱信号需叠加其他信号才判定
- **垃圾信息**：SEO 关键词堆叠、纯符号/超长名称、零大小或异常大小、超大文件数、随机字符串文件名占比等启发式规则
- **体积下限**：总体积小于 `MIN_TORRENT_SIZE`（默认 100 MiB）的种子直接丢弃，滤掉假种、单图、纯链接/说明文件等垃圾

命中任一即丢弃并计入统计（`adult_filtered` / `spam_filtered` / `size_filtered`）。规则见 `server/internal/filter/`。

### 成人内容开关（`FILTER_ADULT`）

置 `false` 后成人内容照常入库。**这个开关必须同时作用于两层**——静态词表不再拒绝，
LLM 审核也不再删除。少改一层的后果不是「没生效」而是更糟：爬虫整天往里索引，审核
每小时删一遍，还把每个 infohash 写进 `blocked` 表永久拉黑。垃圾内容和体积过小的种子
不受影响，照样丢弃。

几个需要知道的边界：

- **只对以后生效**。已经被丢弃的内容不会回来，索引得按每分钟约 33 条重新积累。
- **存量黑名单不动**。之前被 LLM 删掉的成人 hash 仍在 `blocked` 表里、仍会被拒。
  该表存了 `reason`，所以可以只解封成人那部分：`DELETE FROM blocked WHERE reason
  = 'adult'`。但注意每条解封的 hash 被重新发现后都要重新占用一个 fetch 槽位，而管道
  每分钟只消化得掉三十几条，解封量大会明显拖慢新内容入库。
- **没有分级，也没有访客开关**。结果不打标记，成人内容会直接混在普通搜索结果里。
- **前端文案自动跟随**，不需要手改。开关状态通过 `/api/stats` 的 `filter_adult`
  暴露，首页副标题、页脚统计和搜索页横幅都据此措辞——一个环境变量同时管住行为和
  说法，不会出现「页面说已过滤、实际没过滤」。后端连不上时一律按「未过滤」显示：
  验证不了的承诺就不做。
- 看 `/api/stats`：`adult_filtered` 是拒绝数，`adult_indexed` 是这个开关放行的数量。

### 第二道：LLM 二次审核（每小时）

静态词表只能拦住明写关键词的内容。后台每小时调用一次 OpenAI 兼容 API，对**尚未审核过**的库内记录做批量复审，判定为成人或垃圾的直接删除。

- **增量**：每行有 `reviewed_at` 标记，只为没审过的行付费，不会每小时重扫全库
- **不会复活**：删除的 infohash 写入 `blocked` 表，爬虫与增量同步都不会再把它加回来
- **失败安全**：API 报错时该批不标记已审，下轮重试；模型返回无法解析、标签未知或漏项一律按 `ok` 处理，绝不因不确定而删除
- **开销可控**：`MODERATION_MAX_BATCHES × MODERATION_BATCH_SIZE` 即每小时上限（默认 2000 条／小时）
- **提示词**：明确要求「盗版本身不算垃圾」，否则模型会把整个库都判成垃圾；见 `server/internal/moderator/moderator.go` 的 `systemPromptFor`

首次开启建议先跑一轮 dry-run 看日志，确认判定符合预期再放开删除：

```bash
MODERATION_DRY_RUN=true go run ./cmd/server
```

审核统计通过 `GET /api/stats` 的 `moderation` 字段暴露（`reviewed` / `adult_removed` / `spam_removed` / `errors` / `pending` / `blocked`）。

### 顺带：标题去广告（`MODERATION_TRIM_TITLES`）

同一次审核请求里让模型多返回一个 `clean` 字段，把标题里的站点横幅、推广域名、
「地址发布页 / 收藏不迷路」之类的招徕语和上传者广告尾巴删掉。共用同一次调用，
只多花输出 token，不会多一轮请求。

- **原名不动**：清理后的标题写进 `clean_name` 列，`name` 永远保留原文，随时可回退
- **搜索不丢结果**：两列都参与匹配，删掉的广告文字照样能搜到；被广告切断的词组
  反而因为 `clean_name` 变得可搜
- **只删不改**：模型只许删字符。结果必须是原标题的**子序列**，否则丢弃——这一条
  校验就挡掉了改写、翻译、重排和凭空捏造；另有长度下限（≥4 字符且不少于原长 25%）
- **超长标题不动**：标题超过 200 字符时送审的是截断版，模型没看过尾巴，直接跳过
- **审计**：每条改动都打印 `moderator: trim <hash> "原名" -> "新名"`，
  计数进 `llm_titles_trimmed`

实测（deepseek-v4-flash）：`www.UIndex.org - `、`【高清剧集网发布 www.BPHDTV.com】`、
`[TGx]`、`[EZTVx.to]`、`[ WebToolTip.com ]` 都能正确删除；而
`【推しの子】`（作品名）、`iDOLM@STER`、`[Erai-raws]`、`-FraMeSToR`
（字幕组／压制组）都会原样保留。

已经审核过的旧记录不会自动重审。要给存量数据补跑一遍（会重新计费）：

```sql
UPDATE torrents SET reviewed_at = 0;
```

## 部署

`deploy.sh` 一键构建并部署到远程服务器（不包含任何主机/域名/凭证，全部走环境变量）：

```bash
DEPLOY_HOST=user@example.com ./deploy.sh
```

可选变量：`DEPLOY_DIR`（默认 `/opt/dhtsearch`）、`API_PORT`（默认 `8081`）、`GOARCH`（默认 `amd64`）。服务器上需已配置好 `dhtsearch-api` / `dhtsearch-web` systemd 服务和反向代理。

> `deploy.sh` **不会上传 `.env`**（密钥不进部署管道）。服务器上的 API key 需自行放置一次：把 `.env` 放到 `DEPLOY_DIR`（systemd 的 `WorkingDirectory`），或用 systemd 的 `EnvironmentFile=` 指向一个 root-only 的文件。缺少 key 时服务照常运行，只是 LLM 审核不启动。

### 每小时备份到 Cloudflare R2

`scripts/backup-r2.sh` 每小时把数据库快照上传到 R2（S3 兼容 API，走 rclone），
**只保留最新一份**：固定对象名，每小时覆盖。S3 的对象替换是原子的——上传失败
或中断时旧快照原样保留，不存在「没有备份」的窗口。直接拷贝 WAL 模式下的库文件
可能拷到写了一半的状态，所以快照用 `VACUUM INTO`（SQLite 自己的锁保证一致性，
顺带压实），先 `PRAGMA quick_check` 验证再 zstd 压缩上传。

一次性安装（服务器已有 rclone/sqlite3/zstd）：

```bash
# 1. 凭证：Cloudflare 仪表盘 -> R2 -> Manage R2 API Tokens 建一个
#    仅限该桶的 Object Read & Write token，填进 backup.env.example
#    并放到 /opt/dhtsearch/.backup.env（chmod 600）
# 2. 单元文件：
cp scripts/systemd/dhtsearch-backup.{service,timer} /etc/systemd/system/
systemctl daemon-reload && systemctl enable --now dhtsearch-backup.timer
# 3. 手动跑一次验证：
systemctl start dhtsearch-backup && journalctl -u dhtsearch-backup -n 5
```

恢复：下载对象、`zstd -d` 解压、停 `dhtsearch-api`、替换
`data/dhtsearch.db`、重启即可（脚本头部有完整命令）。

## 本地爬虫 + 增量同步（可选）

除了在服务器上爬，还可以在本地跑第二个爬虫实例，定时把增量索引合并到服务器：

```bash
echo 'DEPLOY_HOST=user@example.com' > .deploy.env   # gitignored
./scripts/sync-to-remote.sh                          # 手动同步一次
```

原理：本地实例把数据写到 `local.db`，脚本按水位线（`last_sync_ts`）导出新增行到小 delta 文件，scp 到服务器后 `INSERT OR IGNORE` 合并（按 info_hash 去重），只传输增量。

macOS 上可用 launchd 常驻/定时（安装后本地爬虫 API 在 `127.0.0.1:8089`，同步每 30 分钟一次）：

```bash
./scripts/launchd/install.sh   # 构建二进制、安装脚本、加载两个 agent
```

拉取新代码后重新执行同一条命令即可更新（会重新编译并 reload agent）。

运行时文件（二进制、数据库、日志、脚本副本）装在 checkout **之外**：

| 路径 | 内容 |
| --- | --- |
| `~/Library/Application Support/dhtsearch/` | `bin/`、`local.db`、`last_sync_ts`、`.deploy.env`（可用 `DHTSEARCH_STATE_DIR` 覆盖） |
| `~/Library/Logs/dhtsearch/` | `crawler.log`、`sync.log` |

> 这不是洁癖：launchd agent 对非系统卷（`/Volumes/...`）没有 TCC 权限，仓库放在外置盘时 agent **读写都会失败**——spawn 直接以 `EX_CONFIG (78)` 退出，或每次文件操作报 `Operation not permitted`。`$HOME` 下的路径不需要任何授权。

本地爬虫固定使用 UDP `46881`（plist 里的 `DHT_PORT`），方便在路由器上做端口转发。索引全靠**入站** UDP：别的节点要能把 `get_peers` 查询发到这台机器，BEP-51 的响应也要回得来。家用 NAT 后面不转发这个端口，或者流量走了 VPN 隧道，`seen` 会一直是 0——进程活着、也在发包，但收不到任何东西。

## 注意

- DHT 爬虫从零开始积累索引需要时间（数小时到数天），建议长期运行
- 请遵守所在地区的法律法规，仅用于合法用途
