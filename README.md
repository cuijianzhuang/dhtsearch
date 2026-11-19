# DHTSearch

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

> Co-authored by [Kimi K3](https://www.kimi.com/).

一个自建索引的磁力链接搜索网站：Go 后端通过 DHT 网络爬虫实时采集磁力链接元数据，自动过滤成人内容与垃圾信息，Next.js 前端提供干净的搜索界面。

> 本仓库是 [madeye/dhtsearch](https://github.com/madeye/dhtsearch) 的 fork，在上游基础上改造：存储层支持 Postgres（默认仍为 SQLite）、新增令牌鉴权的管理端、前端样式统一、一键部署脚本与自动化发版。感谢上游作者的工作，许可证保持 MIT（见 [LICENSE](LICENSE)）。

功能清单：

- **DHT 爬虫**：被动监听 + BEP 51 `sample_infohashes` 主动采样发现 infohash，路由表自动维护
- **元数据获取**：BEP 9 并发获取，获取前先用 BEP 15 tracker scrape 按 seeder 数排序，热门优先
- **过滤与审核**：入库前静态过滤（成人词表 + 垃圾启发式 + 体积下限），入库后可每小时用 LLM 二次审核并顺带清理标题广告
- **发布站 RSS**：定时拉动漫花园 / Nyaa 发布 RSS，发布页标题存为别名，罗马音种子也能按中文名搜到
- **热门榜单**：每小时拉豆瓣热门榜单，首页提供快捷搜索词
- **搜索前端**：Next.js 服务端渲染，匹配种子名、清理后标题与别名三列
- **管理端**：`/admin` 概览、种子管理、未审核队列、黑名单、全量配置设置页（含主题），`ADMIN_TOKEN` 鉴权
- **双数据库**：默认 SQLite 单文件零运维；设 `DATABASE_URL` 可切 Postgres
- **部署与发版**：一键安装脚本（systemd 双服务）、合 main 自动发版、每小时备份到 Cloudflare R2（脚本自备）

## 架构

```mermaid
flowchart TB
    DHT["Public BitTorrent DHT nodes"]
    Crawler["DHT crawler<br/>passive listen + BEP-51 sampling"]
    Scraper["Tracker scrape ranking<br/>BEP 15 batch scrape"]
    Fetcher["Metadata fetch<br/>BEP-9 workers"]
    Filter{"Filter engine<br/>keywords + heuristics + size"}
    Discard["Drop and count<br/>adult / spam / too small"]
    DB[("SQLite / Postgres")]
    Web["Next.js frontend"]
    Mod["LLM moderation<br/>OpenRouter free model"]
    Blocked[("blocked table")]

    Crawler <-->|UDP| DHT
    Crawler -->|new infohashes| Scraper
    Scraper -->|ranked by seeders| Fetcher
    Fetcher --> Filter
    Filter -->|hit| Discard
    Filter -->|pass| DB
    Web <-->|REST API| DB
    DB -->|hourly incremental review| Mod
    Mod -->|adult/spam: delete| Blocked
    Mod -->|title ad-trim: clean_name| DB
    Blocked -.->|rejected on insert, no resurrection| DB
```

- `server/` — Go 后端：DHT 爬虫（anacrolix/dht/v2）、BEP-9 元数据获取（anacrolix/torrent）、过滤引擎（中英文成人词表 + 垃圾启发式）、存储层（默认 SQLite / modernc.org/sqlite，无 cgo；可选 Postgres / pgx，见 `DATABASE_URL`）、REST API（标准库 net/http）
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

### Tracker 刮削排序

既然 99% 的发现结果注定被丢弃，**丢谁**就决定了索引长成什么样。默认开启的
scraper（`SCRAPE_ENABLED`）在爬虫和获取端之间插了一层：把新发现的 infohash 攒成
批（一个 UDP 包最多问 ~70 个），向几个大型公共 tracker 发 BEP 15 scrape 拿到
seeder 数，然后按 seeder 数从高到低喂给获取端——热门资源（多为影视内容）优先，
0 seeder 的只是排队靠后，不会被直接扔掉；队列满了先踢 seeder 最少的，等价于把
原来的「随机丢」换成「有依据地丢」。热门资源 peer 多，获取成功率也更高，排序本
身就在提升吞吐。

同一份 `TRACKERS` 列表还会以 `&tr=` 附在获取端的磁力链接上：tracker 一次往返就
能拿到 peer 列表，省掉吃掉大半 `META_TIMEOUT` 的 DHT 找 peer 游走。默认列表分
两类：UDP 的大型 open tracker（opentrackr、demonii 等，外加美剧/韩剧/日剧发布
——EZTV 及各类 TV 资源——常挂的 open.stealth.si、tracker.dler.org、tracker.qu.ax、
tracker-udp.gbitt.info），既参与 scrape 排序也作 peer 提示；以及中国字幕组发布
（动漫花园 / 蜜柑 / Nyaa 上的桜都、喵萌、北宇治等）常用的 HTTP tracker
（nyaa.tracker.wf、t.nyaatracker.com、opentracker.acgnx.se）——HTTP 不能参与
BEP 15 scrape，只作 peer 提示，但字幕组种子的 peer 往往只在这些 tracker 上，
加了才能在超时内拿到 metadata。日剧字幕组的发布（动漫花园日剧区、Nyaa
真人区）另外覆盖了动漫花园自家的 tracker（udp://104.143.10.61:8000，日剧区
每个种子都带）和 t.overflow.biz、tracker.skynetcloud.site（均为 UDP，可
scrape），以及只作 peer 提示的 tracker1.itzmx.com、tracker.renfei.net。

看 `/api/stats` 的 `scraper` 段：`seeded/scraped` 是命中率（有 seeder 的占比），
`queue` 是排队深度，`evicted` 是被挤掉的低优先级 hash，`scrape_errors` 持续增长
说明某个 tracker 挂了（自动重连，也可换 `TRACKERS`）。

### 发布站 RSS（`FEEDS_ENABLED`）

DHT 抽样碰到某个具体字幕组合集的概率很低；而且日剧种子名大多是罗马音
（`[MagicStar] Jigoku ni Ochiru Wa Yo`），中文名（地狱占星师）只出现在发布页
标题里，就算收录了也搜不到。所以服务端每 30 分钟拉一次发布站 RSS（默认动漫
花园日剧区 + Nyaa 真人区），每条的 infohash 交给一个专用的小获取端（默认 16 并发、90 秒超时，不跟 DHT
积压抢主获取端——那里大部分是死种，活跃的字幕组种子在里面也会超时），发布页标题存为该种子的 `alias`——搜索同时匹配种子名和 alias，结果卡片
在种子名下方显示 alias。首页日剧/韩剧热门词还会逐个拿去动漫花园关键词 RSS 搜一
遍，补上已经滚出 RSS 的老发布。已收录的种子只补 alias，不重复获取；获取超时的
6 小时后重试，最多 3 次。`/api/stats` 的 `feeds` 段：`queued` 是送去获取的数量，`fetched`/`timed_out` 是专用获取端的结果，
`aliased` 是补上 alias 的已收录种子，`errors` 是拉 RSS 失败的次数。

## 快速开始

前置：Go 版本以 `server/go.mod` 为准（当前 1.25.6）；前端开发用 Node 22（与 CI 一致）。

### 后端

```bash
cd server
cp ../env.example .env         # 填入 OPENAI_API_KEY（.env 已 gitignore）

# 完整模式：启动 DHT 爬虫（需要 UDP 出站，索引随时间增长）
go run ./cmd/server

# 演示模式：不爬 DHT，插入演示数据，便于本地验证 API/前端
CRAWL_ENABLED=false go run ./cmd/server --seed-demo
```

注意 `.env` 按进程工作目录读取（可用 `ENV_FILE` 改路径）：上面的顺序把 `.env` 放在 `server/` 下，不要放在仓库根目录——根目录的那份不会被读到，缺文件也不报错，只会静默用默认值。真实环境变量优先于 `.env` 文件。

默认监听 `:8080`。配置项（环境变量或同名 flag）按组列在本节各表里。运行期还可在管理端设置页改：数据库里保存的覆盖值优先于环境变量（数据库 > 环境变量 > 默认值），见「管理端」的设置说明。核心项：

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `HTTP_ADDR` | `:8080` | HTTP 监听地址 |
| `DB_PATH` | `./dhtsearch.db` | SQLite 路径（设置 `DATABASE_URL` 后忽略） |
| `DATABASE_URL` | 无 | 设为 `postgres://` / `postgresql://` 连接串时改用 Postgres（pgx 驱动），见下文「Postgres」；不设则默认 SQLite |
| `CRAWL_ENABLED` | `true` | 是否启动 DHT 爬虫 |
| `DHT_PORT` | `0`（随机） | DHT UDP 端口 |
| `DHT_SAMPLERS` | `8` | BEP-51 并发采样 worker 数（调大反而降低入库速度，见上） |
| `META_WORKERS` | `128` | 元数据并发 worker 数（入库速度的主要旋钮） |
| `META_TIMEOUT` | `45s` | 单个元数据获取超时 |
| `META_PRESCREEN_ENABLED` | `true` | 元数据早筛：窗口内无任何 peer 连上的 infohash 提前放弃，不占满 `META_TIMEOUT` |
| `META_PRESCREEN_TIMEOUT` | `10s` | 早筛窗口（包含在 `META_TIMEOUT` 之内，不额外叠加） |
| `META_PRIORITY_ENABLED` | `true` | 抓取优先级队列：做种数 > 来源 > 新鲜度（关 = 先进先出） |
| `FETCH_METADATA` | `true` | 是否获取元数据（false 时只收 infohash） |
| `SCRAPE_ENABLED` | `true` | 是否启用 tracker scrape 排序（见「Tracker 刮削排序」） |
| `TRACKERS` | 内置列表 | 逗号分隔的 tracker announce URL，既作 scrape 目标也作获取的 peer 提示；完整默认列表见 `env.example` |
| `MIN_TORRENT_SIZE` | `104857600`（100 MiB） | 低于此总体积的种子不入库 |
| `ENV_FILE` | `.env` | .env 文件路径（相对工作目录） |
| `RATE_LIMIT_RPS` | `3` | 每客户端 IP 的持续请求速率（令牌桶，0 = 关闭限流） |
| `RATE_LIMIT_BURST` | `30` | 令牌桶突发容量 |
| `SEARCH_MAX_INFLIGHT` | `16` | 并发搜索上限，超出直接 503 甩负载 |
| `SEARCH_TIMEOUT` | `10s` | 单次搜索的数据库时间预算 |
| `ADMIN_TOKEN` | 无 | 管理端令牌，未设置则 `/api/admin/*` 整体禁用（见下文「管理端」） |
| `SITE_NAME` | `DHTSearch` | 站点名称（标题、页头、OG 默认标题） |
| `THEME_COLOR` | `teal` | 主色预设：`teal` / `blue` / `violet` / `green` / `orange` / `red` |
| `THEME_APPEARANCE` | `system` | 默认外观：`system` / `light` / `dark`（访客本地选择优先） |
| `FILTER_BLOCK_WORDS` | 空 | 逗号分隔的屏蔽词，命中种子名或文件路径即按垃圾过滤 |

### Postgres（可选，`DATABASE_URL`）

默认 SQLite 单文件、零运维。需要多写并发、把库和服务拆开部署，或者表长到
LIKE 全表扫描扛不住时，可以切 Postgres，行为和 API 不变：

```bash
DATABASE_URL='postgres://user:pass@127.0.0.1:5432/dhtsearch?sslmode=disable' go run ./cmd/server
```

- 驱动是 `jackc/pgx/v5`（stdlib 模式）；方言差异全部收敛在
  `server/internal/store/dialect.go`：占位符 `?`→`$n`、`BLOB`→`BYTEA`、
  整型→`BIGINT`、`INSERT OR IGNORE`→`ON CONFLICT DO NOTHING`、
  `IIF`→`CASE WHEN`、搜索 `LIKE`→`ILIKE`（保持与 SQLite 一致的大小写
  不敏感语义）。
- 启动时自动建表/补列（与 SQLite 同一套迁移逻辑）；若当前角色能建扩展，
  还会建 `pg_trgm` 扩展和 name/clean_name/alias 三列的 GIN 三元组索引，
  子串搜索可走索引；建不了扩展时自动退化为顺序扫描，不阻塞启动。
- 连接池默认 10（SQLite 是单连接）。
- 数据迁移用服务二进制自带的 `migrate` 子命令（双向都支持，两端各是
  SQLite 文件路径或 `postgres://` URL）：

  ```bash
  dhtsearch-server migrate --from /var/lib/dhtsearch/dhtsearch.db \
      --to postgres://user:password@127.0.0.1:5432/dhtsearch?sslmode=disable
  ```

  按表批量拷贝 `torrents` / `stats` / `blocked`（表结构同构，字节原样
  搬运）；目标里已有同 `info_hash`（或同 `key`）的行**跳过不覆盖**，所以
  重复执行安全，也能合并进一个已有数据的库；迁完逐表核对行数、并逐个
  校验源端主键在目标端存在，缺任何一行都报错且退出码非 0。迁移前请先
  停服务——迁移中源库新增的行不会被带走。一键部署的机器也可以直接用
  `dhtsearch` 菜单里的「数据库切换」，它把停服务、迁移、改配置、重启和
  健康检查串成了一条流程（见下文「一键部署」）。
- 集成测试：`DHTSEARCH_TEST_POSTGRES_URL=postgres://localhost/dhtsearch_test?sslmode=disable go test ./internal/store/ -run TestPostgresStore -v`（未设置该变量时自动跳过）。
- 备份：SQLite 是单文件，停服拷贝或用 `VACUUM INTO` 做一致性快照即可，生产环境可直接用下文「每小时备份到 Cloudflare R2」的脚本（仅适用于 SQLite）；Postgres 用 `pg_dump`。

LLM 审核相关（见下文「LLM 二次审核」）：

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `OPENAI_API_KEY` | 无 | **必填**，未设置则审核不启动 |
| `OPENAI_BASE_URL` | `https://openrouter.ai/api/v1` | 任意 OpenAI 兼容端点 |
| `OPENAI_MODEL` | `nvidia/nemotron-3-super-120b-a12b:free` | 审核模型（`:free` 后缀为 OpenRouter 免费模型） |
| `MODERATION_ENABLED` | `true` | 总开关 |
| `MODERATION_INTERVAL` | `1h` | 审核周期 |
| `MODERATION_TIMEOUT` | `2m` | 每次审核请求超时；临时故障最多尝试 3 次 |
| `MODERATION_BATCH_SIZE` | `100` | 每次请求送审的标题数 |
| `MODERATION_MAX_BATCHES` | `20` | 每轮最多几批（0 = 不限），用于封顶开销 |
| `MODERATION_DRY_RUN` | `false` | 只打日志不删除 |
| `MODERATION_TRIM_TITLES` | `true` | 顺带清理标题里的广告文字 |

热门榜单（豆瓣，见 `server/internal/trending/`）：

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `TRENDING_ENABLED` | `true` | 是否拉取豆瓣热门榜单（首页快捷搜索词） |
| `TRENDING_INTERVAL` | `1h` | 拉取周期 |
| `TRENDING_LIMIT` | `10` | 每类榜单条数 |

发布站 RSS（见上文「发布站 RSS」）：

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `FEEDS_ENABLED` | `true` | 是否启用 RSS 拉取 |
| `FEEDS` | 动漫花园日剧区 + Nyaa 真人区 | 逗号分隔的 RSS 地址，完整默认列表见 `env.example` |
| `FEEDS_KEYWORD_URL` | 动漫花园关键词 RSS | 一个含 `%s` 的 URL，用热门榜单里的日剧/韩剧名逐个检索补老发布 |
| `FEEDS_INTERVAL` | `30m` | 拉取周期 |
| `FEEDS_WORKERS` | `16` | RSS 专用获取端并发（不与主获取端共用） |
| `FEEDS_META_TIMEOUT` | `90s` | RSS 专用获取端单次超时 |

以上全部变量均为可选（不配则用默认值）。只有两个例外是「配了才启用」：`OPENAI_API_KEY` 不配则 LLM 审核不启动，`ADMIN_TOKEN` 不配则管理端禁用。`env.example`（仓库根目录）是带注释的完整模板，服务端的每个变量在里面都有说明。

### 前端

```bash
cd web
cp env.example .env.local    # NEXT_PUBLIC_API_BASE=http://localhost:8080
npm install
npm run dev                  # 开发，访问 http://localhost:3000
# 或 npm run build && npm run start   # 生产
```

前端环境变量（写在 `web/.env.local`，构建时内联）：

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `NEXT_PUBLIC_API_BASE` | 无 | 后端 API 地址，如 `http://localhost:8080` |
| `NEXT_PUBLIC_GA_ID` | 无 | Google Analytics 衡量 ID（`G-XXXXXXXXXX`），留空则不加载任何分析脚本 |

## API

公开接口（无鉴权，有限流，见「DoS 防护」）：

| 方法与路径 | 说明 |
| --- | --- |
| `GET /api/search?q=xx&page=1&page_size=20` | 搜索（q 为空返回最新收录），结果含拼好的 magnet 链接 |
| `GET /api/stats` | 收录数、成人/垃圾/体积过滤计数、LLM 审核统计、爬虫/刮削/RSS 状态 |
| `GET /api/trending` | 豆瓣热门榜单（欧美电影、美剧、日剧、韩剧四类标题 + 更新时间），供首页快捷搜索词 |
| `GET /api/healthz` | 健康检查，返回 `{"ok":true}`，不限流 |
| `GET /api/site` | 站点名称与主题（主色预设、默认外观），供前端渲染，无需鉴权 |

管理接口（全部需要 `Authorization: Bearer <ADMIN_TOKEN>`，未配置令牌时整体 503，见「管理端」）：

| 方法与路径 | 说明 |
| --- | --- |
| `POST /api/admin/login` | 校验令牌（单独限流，每客户端 6 次/分钟） |
| `GET /api/admin/overview` | 概览：总数/今日新增/未审核/已审核/已拉黑、管线与审核计数、爬虫/刮削/榜单状态、数据库后端 |
| `GET /api/admin/torrents` | 种子列表（分页 + 关键词 + 审核状态过滤） |
| `GET /api/admin/torrents/{hash}` | 种子详情（含文件列表） |
| `POST /api/admin/torrents/{hash}/approve` | 标记已审核 |
| `POST /api/admin/torrents/{hash}/block` | 拉黑（删除并记入黑名单，不再收录） |
| `DELETE /api/admin/torrents/{hash}` | 只删记录（可被重新收录） |
| `POST /api/admin/torrents/batch` | 批量通过/拉黑（单次 ≤100 条） |
| `GET /api/admin/blocked` | 黑名单列表 |
| `DELETE /api/admin/blocked/{hash}` | 取消拉黑 |
| `GET /api/admin/settings` | 全量配置：分组、每项生效值、来源（数据库/环境变量/默认）、敏感项脱敏 |
| `PUT /api/admin/settings` | 批量保存运行配置：整批类型/范围校验，任一项非法整批拒绝并逐项报错 |

## 管理端

入口：前端 `/admin`（概览 `/admin`、种子管理 `/admin/torrents`、未审核队列
`/admin/review`、黑名单 `/admin/blocked`、设置 `/admin/settings`）。默认关闭：后端设置环境变量
`ADMIN_TOKEN`（足够长的随机串，如 `openssl rand -hex 32`）并重启后启用；
未设置时后端在启动日志里打印 `admin: disabled`，所有 `/api/admin/*`
返回 503，前端显示「管理端未启用」提示。

功能：

- **概览**：种子总数 / 今日新增 / 未审核 / 已审核 / 已拉黑五卡，外加收录
  管线计数、LLM 审核统计、爬虫 / 刮削 / 热门榜单状态、数据库后端。
- **种子管理**：分页列表（关键词 + 审核状态过滤）、详情（文件列表），
  单条通过（标记已审核）/ 拉黑（删除并记入黑名单，不再收录）/ 删除
  （只删记录，可被重新收录）。
- **未审核队列**：只看未审核，支持多选批量通过 / 批量拉黑。
- **黑名单**：查看 blocked 表，逐条取消拉黑。
- **设置**：全部配置可视化。启动配置（监听地址、数据库路径/连接串、
  管理令牌）只读展示，改 env 文件后重启生效；运行配置（爬虫、元数据、
  刮削、审核与过滤、热门榜单、订阅源、限流）在线可改，存数据库覆盖层，
  优先级 **数据库 > 环境变量 > 默认值**，多数即时热生效，结构性参数
  （worker 数、端口等）明确标注「需重启」。密钥类脱敏展示、永不回显
  明文。同页含「外观 / 主题」分组：站点名称、主色预设（六种，全站
  CSS 变量切换）、默认外观；前台与管理端右上角另有外观切换
  （浅 / 深 / 跟随系统），访客选择存 localStorage 并优先于默认外观。

鉴权：所有 `/api/admin/*` 都要 `Authorization: Bearer <ADMIN_TOKEN>`
（常量时间比较）。登录走 `POST /api/admin/login` 校验，单独限流
（每客户端 6 次/分钟），令牌只从环境变量读，不落库、不打日志。前端
校验通过后把令牌存进 httpOnly cookie（24 小时），页面脚本读不到，
浏览器端操作经 Next 的 `/api/admin-action` 代理转发。

## DoS 防护

搜索是无鉴权接口，而每次关键词查询都是两遍全表扫描（SQLite 单连接），所以 API 层内置了四道闸门，全部可用环境变量调整（见上表）：

- **每 IP 限流**：令牌桶（默认持续 3 req/s、突发 30），超出返回 429 + `Retry-After`。真实客户端 IP 只信任来自回环/内网地址（反向代理或同机 Next SSR 进程）的 `X-Forwarded-For`，公网直连时伪造头无效；IPv6 按 /64 计桶，防止单机用整段地址刷新桶。`/api/healthz` 不限流，监控和部署健康检查不会被洪水挤掉。
- **搜索准入**：并发搜索上限（默认 16），等不到槽位（1 秒）直接 503 甩负载——SQLite 只有一个连接，排队再深也只是白占内存。
- **单查询预算**：每次搜索默认 10s 超时，客户端断开即取消，慢查询不会继续占着数据库；关键词最多取前 8 个（每个关键词都让每行多算两次 LIKE），翻页深度上限 10000 行（`OFFSET` 越深扫描越贵），查询串按 UTF-8 边界截断到 200 字节。
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

### 一键部署（`scripts/install.sh`）

单机新装推荐这条路径：从 GitHub Release 下载预编译包（Go 二进制 + Next.js standalone 产物），**目标机不需要 Go / npm 工具链**，只需 systemd 和一个 Node.js 运行时（>= 18，跑前端 standalone 服务）：

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/cuijianzhuang/dhtsearch/main/scripts/install.sh) install
```

子命令：`install` / `upgrade` / `status` / `uninstall`（无参在终端里运行给交互菜单）。常用选项：`--version vX.Y.Z` 指定版本（默认最新 Release）、`--file <tar.gz>` 用本地包安装、`--from-source` 无 Release 产物时从源码构建兜底（需要 git + Go + Node）、`uninstall --purge` 连配置和数据一起删（默认保留）。

自定义端口：`install --api-port 18080 --web-port 13000`（也可预先设环境变量 `API_PORT` / `WEB_PORT`；优先级为 参数 > 环境变量 > 已有配置 > 默认 8080/3000）。API 端口写进配置文件的 `HTTP_ADDR`，同时生成给前端 unit 的 `API_BASE=http://127.0.0.1:<api-port>`——前端在服务端启动时读这个运行时变量（不写死在构建产物里），所以改后端端口不用重打 Release 包，前端会自动跟过去。upgrade 不带端口参数时保留已有端口不回退默认。启动服务前脚本会先检查两个端口：被其他进程占用会直接报出占用进程并退出，不会等到 120 秒健康检查超时（被本服务自己占着属正常，不误报）。

数据库选择：`install --db sqlite|postgres-local|postgres-url`（交互安装时在端口之后也会问）。默认 `sqlite`；`postgres-local` 在本机自动安装并配置 PostgreSQL（Debian/Ubuntu 走 apt，其他发行版会停下来给手动指引）：检测到已有可用的本机 PG 就复用，否则自动安装，建 `dhtsearch` 角色和同名库、生成随机密码，把 `DATABASE_URL` 写进配置文件，安装汇总里把完整连接地址（含密码）只打印一次请保存；`postgres-url --database-url postgres://...` 用已有 PG，写配置前先实测连通性（用发布包里的二进制跑一次空迁移），连不上直接中止、不写配置。已有配置的机器重复 install 时后端保持不变（除非显式给 `--db`），upgrade 永远不动数据库选择。配置文件因为可能带数据库密码，权限收紧为 600（root 专属；systemd 以 root 读 EnvironmentFile 后注入进程环境，服务不受影响）。

装后切换数据库用 `dhtsearch` 菜单的「数据库切换」：显示当前后端（SQLite 给文件路径，PG 给脱敏后的 URL），选目标（切 PG 同样有本机自动装 / 已有地址两条路），确认后严格按 停服务 → `dhtsearch-server migrate` 迁数据并核对行数 → 改 `DATABASE_URL` → 启服务 → 健康检查 的顺序执行；迁移失败或行数对不上就不改配置、按原配置把服务拉回来，新后端健康检查不过同样回滚。切回 SQLite 时数据从 PG 迁回 `DB_PATH` 文件（文件里已有的同 hash 旧行不覆盖）。

装完的布局：

| 路径 | 内容 |
| --- | --- |
| `dhtsearch.service` / `dhtsearch-web.service` | API+爬虫 / 前端两个 systemd 单元，`enable --now` |
| `/usr/local/bin/dhtsearch-server`、`/opt/dhtsearch/web/` | 二进制与前端产物 |
| `/usr/local/bin/dhtsearch` | 管理脚本入口（install 时由 install.sh 自身复制），无参运行开交互菜单：安装 / 升级 / 状态 / 重启 / 日志 / 修改端口 / 数据库切换 / 卸载；与服务二进制 `dhtsearch-server` 是两个文件 |
| `/etc/dhtsearch/dhtsearch.env` | 配置（端口、数据库：默认 SQLite 的 `DB_PATH`，或 `--db` 写入的 `DATABASE_URL`），权限 600，重复 install / upgrade 不覆盖 |
| `/var/lib/dhtsearch/` | 数据库、`VERSION`、upgrade 的备份（`backups/`，健康检查失败自动回滚） |

交互菜单里选「安装」会依次询问 API 端口和 Web 端口（带默认值、校验 1-65535），新装时还会问数据库（SQLite / PostgreSQL 本机自动装 / PostgreSQL 已有地址），打印汇总确认后才执行；「修改端口」显示当前端口、询问新端口，保存后重启两个服务并跑健康检查；「数据库切换」显示当前后端、选目标、二次确认并提示短暂停机后执行带数据迁移的切换；升级 / 重启 / 改端口 / 切库 / 卸载在菜单里执行前都要二次确认，卸载可选择保留数据或 `--purge` 全删。

API 默认只听 `127.0.0.1:8080`、前端听 `127.0.0.1:3000`，公网访问请在前面加反向代理。日志：`journalctl -u dhtsearch -f`（前端 `-u dhtsearch-web`）；健康检查：`curl http://127.0.0.1:8080/api/healthz`（install/upgrade 会等它最多 120 秒，失败打印日志尾巴；端口改过的话换成实际端口）。

Release 产物由 `.github/workflows/release.yml` 构建：`dhtsearch-<version>-linux-<amd64|arm64>.tar.gz` + 对应 `.sha256`，install 时先验 sha256 再安装。本地等价打包用 `./scripts/package-release.sh --version vX.Y.Z --arch amd64`。

发版分自动和手动两条路：

- **自动发版**（`.github/workflows/auto-release.yml`）：push 到 `main` 时，只有改动包含业务代码才发版，版本号在现有最大 `vX.Y.Z` tag 基础上 patch +1（仓库还没有 tag 时首个版本为 `v0.1.0`）。业务代码白名单由 `scripts/release-scope.sh` 判定（后端 `server/` 的 Go 代码与 go.mod/go.sum、前端 `web/app|components|lib|public` 与 web 构建配置），只改文档、`scripts/`、`.github/` 不发版；业务代码与文档混在一起照常发。HEAD 已有版本 tag、或提交信息含 `[skip release]` 时跳过。连续多次 push 只发最新一次（旧的运行会被取消）。判断与版本号计算可用 `./scripts/test-release-tools.sh` 本地单测。
- **手动发版**：照旧——自己推 `v*` tag 走 `release.yml` 直接发版；或在 Actions 里手动 `workflow_dispatch` 跑 `release.yml`：默认只构建产物（`version` 不填时产物名为 `dev-<短 sha>`，不挂 Release），勾选 `publish` 后同时创建 GitHub Release（`version` 不填时自动取下一版本，即最大 `vX.Y.Z` tag 的 patch +1）。

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

## 开发

目录结构：

| 路径 | 内容 |
| --- | --- |
| `server/cmd/server/` | 服务入口（flag/env 解析、各模块装配） |
| `server/internal/crawler/` | DHT 爬虫（被动监听 + BEP 51 采样） |
| `server/internal/scraper/` | BEP 15 tracker scrape 排序 |
| `server/internal/metadata/` | BEP 9 元数据获取 |
| `server/internal/filter/` | 入库前静态过滤（词表 + 启发式） |
| `server/internal/moderator/` | LLM 二次审核与标题去广告 |
| `server/internal/feeds/` | 发布站 RSS 拉取 |
| `server/internal/trending/` | 豆瓣热门榜单 |
| `server/internal/store/` | 存储层（SQLite / Postgres 双后端，方言在 `dialect.go`） |
| `server/internal/api/` | 公开 API 与管理端 API |
| `web/` | Next.js 前端（`app/` 页面含 `/admin`，`components/`、`lib/`） |
| `scripts/` | 一键部署、打包、发版判定、R2 备份、本地同步脚本 |

构建与测试（与 CI `.github/workflows/ci.yml` 一致，两个 job：Go server + Next.js web）：

```bash
cd server
gofmt -l .          # 应无输出
go vet ./...
go test -race ./...

cd ../web
npm ci
npm run lint
npm run build
```

发版工具脚本的单测：`./scripts/test-release-tools.sh`。贡献流程：从 `main` 拉分支改，测试全过后开 PR，合并由维护者完成；合入 `main` 的业务代码会自动发版（见上文「发版」）。

## 许可

MIT，见 [LICENSE](LICENSE)。本仓库 fork 自 [madeye/dhtsearch](https://github.com/madeye/dhtsearch)，版权与许可沿用上游。

## 注意

- DHT 爬虫从零开始积累索引需要时间（数小时到数天），建议长期运行
- 请遵守所在地区的法律法规，仅用于合法用途
