<p align="center">
  <img src="docs/assets/dhtsearch-icon.png" width="112" alt="DHTSearch 图标">
</p>

<h1 align="center">DHTSearch</h1>

<p align="center">自建索引的磁力链接搜索网站 · DHT 网络实时收录 · 自动过滤成人内容与垃圾信息</p>

<p align="center">
  <a href="https://github.com/cuijianzhuang/dhtsearch/releases/latest"><img src="https://img.shields.io/github/v/release/cuijianzhuang/dhtsearch?label=%E6%9C%80%E6%96%B0%E7%89%88%E6%9C%AC&color=0E7490" alt="最新版本"></a>
  <img src="https://img.shields.io/badge/Go-1.25.6-00ADD8?logo=go&logoColor=white" alt="Go 1.25.6">
  <a href="https://github.com/cuijianzhuang/dhtsearch/actions/workflows/ci.yml"><img src="https://img.shields.io/github/actions/workflow/status/cuijianzhuang/dhtsearch/ci.yml?branch=main&label=CI&logo=githubactions&logoColor=white" alt="CI"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-MIT-yellow.svg" alt="License: MIT"></a>
</p>

<p align="center">
  <a href="#快速开始">快速开始</a> ·
  <a href="#功能亮点">功能亮点</a> ·
  <a href="#api">API</a> ·
  <a href="#管理端">管理端</a> ·
  <a href="#部署">部署</a> ·
  <a href="#开发">开发</a> ·
  <a href="https://github.com/cuijianzhuang/dhtsearch/releases">版本下载</a> ·
  <a href="https://github.com/cuijianzhuang/dhtsearch/issues">问题反馈</a>
</p>

Go 后端通过 DHT 网络爬虫实时采集磁力链接元数据，自动过滤成人内容与垃圾信息，Next.js 前端提供干净的搜索界面。

> 本仓库是 [madeye/dhtsearch](https://github.com/madeye/dhtsearch) 的 fork。改造点：存储层双后端（SQLite / Postgres，安装时可选、装后可带数据切换）、令牌鉴权的管理端与全量配置设置页（含主题）、多身份 DHT 采样与可切换的元数据抓取底座、自定义端口与交互菜单的一键部署、合 main 自动发版。感谢上游作者的工作，许可证保持 MIT（见 [LICENSE](LICENSE)）。
>
> Co-authored by [Kimi K3](https://www.kimi.com/).

## 目录

- [功能亮点](#功能亮点)
- [架构](#架构)
- [快速开始](#快速开始)
- [API](#api)
- [管理端](#管理端)
- [DoS 防护](#dos-防护)
- [过滤策略](#过滤策略)
- [部署](#部署)
- [本地爬虫 + 增量同步（可选）](#本地爬虫--增量同步可选)
- [开发](#开发)
- [许可](#许可)

## 功能亮点

| 功能 | 说明 |
| --- | --- |
| DHT 爬虫 | 被动监听 + BEP 51 `sample_infohashes` 主动采样发现 infohash；单进程可开多个 DHT 身份（`DHT_IDENTITIES`）拓宽采样覆盖；节点按成功率/延迟打分、优选回填路由表，优质节点落库持久化，重启不从零攒 |
| 元数据获取 | BEP 9 并发获取；获取前先用 BEP 15 tracker scrape 按 seeder 数排序，热门优先；抓取队列按做种数/来源/新鲜度排优先级，死哈希经早筛窗口提前放弃、不占满超时；抓取器按哈希分片到多个客户端（`META_SHARDS`）摊薄单客户端全局锁；底座可切换（`META_FETCHER`：anacrolix 客户端 / 自研薄抓取器 native，二者统计口径一致、可 A/B 对比） |
| 抓取管线防护 | 准入负缓存：已入库的哈希不再重抓，失败哈希指数退避（1 小时起翻倍、封顶可调）期内不占队列；DHT 侧顺手带回的 peer 地址（announce_peer / get_peers 应答）直达抓取器作热启动；停转看门狗在发现还在动、入库却停滞时打全量协程栈自诊断，持续无进展则退出由 systemd 拉起；停滞与恢复可经 Webhook / Telegram 外发告警 |
| 做种信息 | 每轮 scrape 的做种/下载/完成数落库（`swarm_stats`，多 tracker 取最大值、无数据不写 0），搜索结果带真实做种数与五级健康度（alive / no_seed / suspect / likely_dead / unknown），支持 `sort=seeders` 与「隐藏疑似死种」 |
| 过滤与审核 | 入库前静态过滤（成人词表 + 垃圾启发式 + 体积下限），三类过滤各有开关、阈值可调；入库后可每小时用 LLM 二次审核，并顺带清理标题广告 |
| 发布站 RSS | 定时拉动漫花园 / Nyaa 发布 RSS，发布页标题存为别名，罗马音种子也能按中文名搜到 |
| 热门榜单 | 每小时拉豆瓣热门榜单，首页提供快捷搜索词；榜单落库持久化，重启不丢、抓取失败指数退避重试并在后台提示；另有本站「热门搜索」行：成功搜索的匿名聚合计数（无任何访客级记录），有数据才显示 |
| 搜索前端 | Next.js 服务端渲染，匹配种子名、清理后标题与别名三列；支持分类筛选（视频/音频/图片/文档/软件/压缩包）、四种排序（相关性加权评分 / 最新 / 大小 / 做种数）、关键词高亮与搜索历史、关键词 RSS 订阅与访客举报 |
| 管理端 | `/admin` 概览（真实增量驱动的五段管线动画、入库趋势、实时轮询）、种子管理、未审核队列、黑名单、举报队列、LLM 审核专页、API 令牌、账号管理；设置总览 + 分组子页可视化全部 90 项配置（16 组，优先级：数据库覆盖 > 环境变量 > 默认值）与主题（站点名、六种主色、三态外观）；面板可发起升级并显示真实进度，版本无更新时不走升级流程；登录支持用户名密码、GitHub、OIDC SSO 与 `ADMIN_TOKEN` 恢复令牌 |
| 公开 API 令牌 | 可选的令牌鉴权：管理端签发 `dht_` 令牌（只存哈希、明文只显示一次），逐令牌每分钟配额，可要求公开接口强制带令牌 |
| 双数据库 | 默认 SQLite 单文件零运维；可切 Postgres：安装时 `--db` 选库、装后菜单带数据切换，服务二进制自带 `migrate` 子命令双向迁移 |
| 部署与发版 | 一键安装脚本（自定义端口、交互菜单、systemd 双服务）、合 main 自动发版、每小时备份到 Cloudflare R2（脚本自备） |

## 界面截图

<!-- 仓库暂未收录截图。将截图文件放入 docs/screenshots/ 后，取消下面几行的注释即可显示：
![搜索首页](docs/screenshots/home.png)
![管理端设置页](docs/screenshots/admin-settings.png)
-->

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

- `server/` — Go 后端：DHT 爬虫（anacrolix/dht/v2，多身份 + 节点质量评分）、BEP-9 元数据获取（anacrolix/torrent 分片客户端，或自研薄抓取器 native——只做 tracker announce 取 peer + BEP 9 拼装 + SHA-1 校验，无全局锁；`META_FETCHER` 切换）、过滤引擎（中英文成人词表 + 垃圾启发式）、存储层（默认 SQLite / modernc.org/sqlite，无 cgo；可选 Postgres / pgx，见 `DATABASE_URL`）、REST API（标准库 net/http）
- `web/` — Next.js 前端（App Router + Tailwind，服务端渲染搜索结果）

### 发现速度

infohash 来自两条路径，**主动的那条决定吞吐**：

- **被动**：别的节点发来的 `get_peers` / `announce_peer`。只在公网可达时才有量，NAT/VPN 后面基本为零。
- **主动（BEP-51）**：`DHT_SAMPLERS` 个 worker 并发对路由表里的节点发 `sample_infohashes`，每个节点一次能返回几十个 infohash。查询带随机 `target`，回包里的节点又会入队，于是采样本身就是一次 ID 空间游走；每个节点按它自己声明的 `interval`（钳在 1 分钟～2 小时）重采。
- 路由表由 `TableMaintainer` 维护 + 定时随机 `find_node` 拓宽——**路由表空了发现就会停**，`/api/stats` 的 `crawler.nodes` 就是看这个的。
- **多身份**：单个 DHT 身份只能采样到自己路由表邻域的 ID 空间。`DHT_IDENTITIES > 1` 时同进程起多个身份（各自随机 node ID、路由表与节点质量跟踪，监听 `DHT_PORT..DHT_PORT+N-1`），采样面按身份数拓宽；`DHT_SAMPLERS` 是每身份的 worker 数。节点质量（衰减加权成功率 + 延迟）决定回填优先序与重采间隔，连续失败的节点冷却禁用；优质节点快照落库（`dht_nodes`），重启时与公共路由节点一起作种子恢复。
- **peer 顺手带回**：爬虫处理入站 `announce_peer` 与自己 `get_peers` 的应答时，把 (哈希, peer 地址) 记进有界提示缓存（每哈希 ≤8、TTL 30 分钟）；哈希入队抓取时提示一并带上，抓取器开局就直连这些 peer，不必全靠 tracker/DHT 找人。

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

scrape 结果不只是排序依据：每轮的做种（seeders）/ 下载中（leechers）/ 已完成
（completed）数按哈希落库（`swarm_stats`，同一哈希多 tracker 取各字段最大值；
某轮全部 tracker 失败时**不写**——没数据和真 0 严格区分，搜索结果里无数据是
`null`、真 0 是 `0`）。健康度在读取时由快照推导，五级：`alive`（做种 > 0）、
`no_seed`（无人做种但有人在下）、`likely_dead`（0/0 且连续多轮跨度 ≥48 小时）、
`suspect`（0/0 但证据还不够）、`unknown`（无数据）；前台文案带判定依据，
`sort=seeders` 按做种数排（无数据垫底），`hide_dead=1` 可隐藏疑似死种。

**定期复查**：快照只在入库时写一次，之后靠后台复查保持新鲜。
`RESCRAPE_ENABLED`（默认**开**）开启后，服务每分钟挑快照最陈旧的已入库
种子重新 scrape 一轮——从未刮过的最优先，快照新于 `RESCRAPE_MIN_AGE_H`
（默认 48h）的不碰，每分钟最多 `RESCRAPE_PER_MIN`（默认 60）个。复查与
入库走同一套 tracker 合并与落库路径（多 tracker 取最大值、全失败不写），
所以口径完全一致；它只查 tracker、永不触发元数据抓取，元数据队列满时
整轮跳过，不与入库抢资源。每轮日志一行 `rescrape: selected N, updated M
(zero Z)`，计数落 `rescrape_rounds` / `rescrape_updated` /
`rescrape_zero_rounds`。复查也是下面保留清理的证据来源：全零轮次持续
累积，死种才会攒够 `likely_dead` 所需的轮次与跨度。

**数据保留清理**：健康度同时是索引的退出通道。设置组「数据保留」
（`RETENTION_ENABLED`，默认**关**）开启后，服务按 `RETENTION_INTERVAL_H`
（默认 24h）周期性删除同时满足两条的种子：健康度为 `likely_dead`
（与前台同一套推导，不另立标准），且入库超过 `RETENTION_MIN_AGE_DAYS`
（默认 30 天）。删除连同文件列表、`swarm_stats` 快照与 `fetch_attempts`
退避行一并移除，**黑名单永不触碰**——拉黑过的哈希清理后仍是拉黑状态，
而被清理的普通哈希日后若被重新发现，会以全新状态重新入库。删除不可
恢复，所以默认关：设置页的清理卡会先显示当前口径下的可清理数量、累计
已清理数与上次清理记录，确认口径无误后再开启；也可以在卡上手动触发
一轮（手动触发不受自动开关限制，但同样受最短保留天数约束）。每轮日志
一行 `retention: deleted N (eligible M)`。注意覆盖面：`likely_dead`
要求同一哈希被重复刮削至少 3 轮且跨度 ≥48h，证据来自入库刮削与上面的
定期复查——复查默认开着，所以覆盖面随时间自然扩大；关掉复查时，只被
刮过一两轮的种子永远到不了这一级。

### 抓取管线：准入、底座与看门狗

哈希进抓取队列前先过**准入闸**（`internal/admission`）：已在库里的直接跳过
（此前重启或内存去重淘汰后，已入库哈希会被重新发现、重新抓一遍空转）；
抓取失败过的按连续失败次数指数退避（1h → 2h → 4h…，上限
`META_NEG_CACHE_MAX_BACKOFF_H`，默认 24h），退避期内跳过，一次成功清零。
刮削刚报出做种数的哈希与订阅源哈希豁免退避（新鲜证据优先），但不豁免
已入库检查。退避状态落库（`fetch_attempts`，随数据库迁移一起搬）。

抓取底座由 `META_FETCHER` 选：`anacrolix`（默认，torrent 客户端按哈希首字节
分片到 `META_SHARDS` 个客户端摊薄全局锁）或 `native`（自研薄抓取器：只做
tracker announce 取 peer、TCP/uTP 直连、BEP 9 分片拼装，拼全后 SHA-1 必须
等于目标 infohash 才算成功；没有客户端级全局锁，卡死的 peer 只损失自己
的协程）。两底座同一队列、同一 45 秒预算与早筛、同一套
fetched/超时/失败/早筛丢弃计数，翻设置重启即可用管线日志 A/B 对比；
订阅源专用抓取器固定 anacrolix。

第三个底座 `libtorrent` 跑在独立的边车进程里（libtorrent-rasterbar
2.0.11，Go 侧只经长度前缀 JSON 帧协议与之通信，主二进制保持纯 Go
静态构建）。边车以预编译发布资产的形式随版本发布
（`dhtsearch-lt-sidecar-linux-<arch>`，非主包的一部分）：安装/升级时
install.sh 会自动从同一发布里下载它、校验 sha256 并安装到服务端
二进制旁边（`/usr/local/bin/dhtsearch-lt-sidecar`），切
`META_FETCHER=libtorrent` 不需要任何手工配置。装没装上是明账：
安装/升级结尾的汇总与 `dhtsearch status` 都有一行
「libtorrent 边车：已安装 … / 未安装（原因）」；下载失败会重试一次
并在汇总里大声警告，但不会让安装/升级整体失败（边车是可选组件）。
早于边车的旧发布不带这个资产（汇总里会写明），选了 libtorrent 又
没有边车时，服务端按设计在启动时大声报错退出、不会静默降级。

**停转看门狗**（`CRAWLER_WATCHDOG_ENABLED`，默认开）：入库管线有两类坏法
——全冻（抓取路径被锁焊死，`fetched` 纹丝不动）和爬行（没冻死但吞吐塌到
基线的零头）。看门狗每分钟采样：发现还在动而入库 15 分钟零增长 → 打
ERROR + 全量协程栈；30 分钟仍零增长 → 再打一份栈后退出进程，靠 systemd
`Restart=on-failure` 拉起。爬行判据（发现动、队列不降、10 分钟 fetched
增长在 1–29 之间）同样两窗触发。发现与入库同时冻结（断网形状）只警告
不退出。每 10 分钟另有一行固定格式的管线汇总日志
（`pipeline: +N seen +N fetched queue_depth N …`），是历次事故定位的
主要依据。

**停滞外发告警**（设置组「告警通知」，`ALERT_ENABLED` 默认关）：看门狗
只打日志的那次事故里，停转跑了约 9 小时才被发现——告警把看门狗的判定
送出机器。检测仍归看门狗独占（不另设一套会打架的判据）：它判定停滞、
爬行或冻结时经事件钩子通知告警服务，一轮停滞只发一条（附已停滞分钟数
与当时的 seen/fetched/队列深度），恢复时再发一条；升级为退出自愈前，
未发完的停滞告警会先同步刷送（限时 2 秒），并补一条退出通知。通道两
种，可叠加：通用 Webhook（POST JSON `{title, text, ts}`，钉钉/飞书/
企业微信等群机器人地址直接填，地址按密钥脱敏）与 Telegram（Bot API
`sendMessage`，token + 会话 ID）。发送异步排队、单次 5 秒超时、最多
重试一次，通道故障只记日志、绝不拖住看门狗。停滞阈值
`ALERT_STALL_MINUTES`（默认 15，钳 5–120）就是看门狗的警告阈值本身，
退出阈值为其两倍，启动时装配、重启生效；开关与通道热生效。配完先用
`POST /api/admin/alerts/test`（仅管理员）逐通道验通：未配任何通道返回
400，各通道成败逐项回显。

### 爬虫节点模式（多机分工）

单机之外，同一二进制可以拆成「一个中心 + 多个爬虫节点」：元数据抓取是
整条管线里最吃带宽与连接数的一环，适合横向扩到多台机器；而过滤、准入、
去重这些策略必须只在一个点上做决定，否则多台机器各存一份、各滤一套，
索引必然漂移。所以分工是：**节点只负责发现与抓取，中心负责一切策略与
存储**。

- **中心节点**（就是平时的单机部署多开一个开关）：设置页「节点接入」组
  打开 `NODE_INGEST_ENABLED` 并设置 `NODE_TOKEN`（共享密钥，只显示是否
  已设置）。中心多出两个内部接口：`POST /api/node/check` 回答某批哈希
  「已入库 / 失败退避中」、`POST /api/node/ingest` 接收节点推来的记录与
  刮削观测——入库前先过中心自己的内容过滤，计数与单机抓取完全同口径
  （含按来源的 `fetch_<源>_*`），另按节点名累计 `node_ingest_<节点名>`
  计数；成功入库会顺带清掉该哈希的负缓存行。单批上限 500 条记录 /
  10MB，超限 413。这两个接口不进公开 API 文档。
- **爬虫节点**：在另一台机器上用一键安装直接装成节点，不用手动改
  配置文件：

  ```bash
  bash <(curl -fsSL https://raw.githubusercontent.com/cuijianzhuang/dhtsearch/main/scripts/install.sh) install \
    --node-center https://中心地址 --node-token <与中心同值> --node-name hk-1
  ```

  安装器会把三个**环境变量**写进配置文件（节点侧配置只认环境、不进
  设置库——节点的库是它自己的本地库，中心改设置不应能改动节点）：
  `CENTER_URL`、`NODE_TOKEN`、`NODE_NAME`（可选，默认主机名）；并且
  **只安装并启动 API 服务**——节点不跑 Web 前端（机器上原有的 Web
  服务会被停用，升级也不会把它带回来；节点机器因此不需要 Node.js）。
  令牌在输出里只以掩码形式出现（前 4 位 + …）。装完安装器会自动做
  一次连通性测试（向中心的 `/api/node/check` 发一个探针哈希），四种
  结果分开报告：通 / 中心未开启节点接入（503）/ 令牌不正确（401）/
  连不上（网络错误）；测试不过也**不中断安装**——节点失败开放，中心
  修好后推送自动恢复。之后改配置走管理菜单 `dhtsearch` →「10) 爬虫
  节点」：可改中心地址/令牌（隐藏输入）/节点名、可解除节点模式（恢复
  独立部署并重新启用 Web）、可随时手动跑连通性测试；`dhtsearch
  status` 也会显示当前角色。手动兜底方法仍然可用：自己往
  `/etc/dhtsearch/dhtsearch.env` 写这三个变量再重启 API 服务，效果
  相同。设了 `CENTER_URL` 即进入节点模式：爬虫、刮削、抓取照常跑，
  但抓到的记录不再本地过滤/入库，而是攒批（约 100 条或 5 秒）推给
  中心；排队前先批量问中心准入（已入库/退避中的哈希不抓，问询失败
  则照常抓——与单机准入的故障开放一致）；刮削观测随入库批次一并
  上报。节点本地库只留自己的设置与统计，`/api/stats` 与停滞看门狗
  照常工作（进度信号换成已送达数）。
- **失败语义**：中心不可达时推送重试一次，仍失败则整批落**磁盘
  暂存**（数据目录下 `spool/`，追加式分段文件、段约 8MB），内存
  缓冲（2000 条）溢出同样落盘而非丢弃；中心恢复后按段从旧到新
  回放，回放过的段删除，节点重启也会先回放上次留下的暂存。
  暂存总量有上限（默认 1GB，`NODE_SPOOL_MAX_MB` 可调，设 0 关闭
  暂存、回到纯内存缓冲语义），超限时丢最旧的整段并计数
  （`node_dropped`）；落盘/回放量另有 `node_spooled` /
  `node_replayed` 计数、当前占用见 `node_spool_segments` /
  `node_spool_bytes`。也就是说：暂存能扛住「不超过上限的」中心
  停机与节点重启，超限部分仍会丢——它是一个有界的本地缓冲，
  不是通用消息队列。节点模式下数据保留清理与老种复查不启动
  （本地无索引可清/可查，中心对合并后的索引统一执行）。
- **WireGuard 隧道接入（推荐的连通方式）**：上面的直连方式要求中心的
  API 端口能被节点机器访问到；不想把 API 暴露在公网上时，用安装器
  自带的隧道编排——中心与节点之间拉一条专用 WireGuard 隧道
  （接口 `wg-dht`，子网 10.9.0.0/24，中心 10.9.0.1，UDP 51820；
  特意避开常见的 wg0 / 10.8.0.0/24 以免与机器上其他 WireGuard
  配置撞车），节点的推送只走隧道，中心 API 端口无需任何公网暴露。

  中心侧（一次性开启 + 每节点一张接入包）：

  ```bash
  dhtsearch wg-setup --endpoint <本机公网IP或域名>   # 建隧道；云主机的弹性公网 IP 常不在网卡上，需显式给出（也可菜单里改）
  dhtsearch wg-bundle --name hk-1                    # 注册节点并打印一行接入包
  ```

  `wg-bundle` 在中心侧替节点生成密钥对、分配隧道 IP（从 .2 起，
  移除后释放复用），把节点加进隧道配置并热生效。打印出的**接入包
  是凭据**：一行 base64，内含节点私钥、隧道 IP、中心公钥、接入点、
  API 端口与 `NODE_TOKEN`（脚本能从中心配置读到、或生成时粘贴输入
  时才带；都没有则不带令牌，节点安装时再提示输入）——只走可信渠道
  传递。运维面都在管理菜单「10) 爬虫节点 → 5) 中心隧道」里（开启/
  改接入点、生成接入包、手动添加、移除节点）；命令行另有
  `wg-add --name N --pubkey K [--ip A]`（节点自备密钥对时用）和
  `wg-remove --name N`。

  节点侧只需粘贴接入包，二选一：

  ```bash
  bash <(curl -fsSL https://raw.githubusercontent.com/cuijianzhuang/dhtsearch/main/scripts/install.sh) install \
    --join-bundle '<接入包整行>'
  # 或在已装机器的管理菜单「10) 爬虫节点 → 4) 粘贴接入包加入中心」
  ```

  安装器据此写好节点隧道配置、拉起隧道（约 15 秒内没有握手会单独
  报错，与 API 自检的四种结果分开报告——先查中心安全组是否放行
  UDP 51820），随后按普通节点流程把 `CENTER_URL` 指向隧道内的
  `http://10.9.0.1:<API端口>` 并跑连通性自检。隧道状态在
  `/etc/wireguard`（`wg-dht.conf` / `wg-dht.state` / `wg-dht.key`），
  不在安装/升级管理的路径上，**升级不会动它**；`dhtsearch status`
  会显示隧道运行状态（中心侧是已注册节点数，节点侧是最近握手
  时间）。

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

只想把站点跑起来（单机部署）用一键安装，三条命令内完成（细节见下文「一键部署」）：

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/cuijianzhuang/dhtsearch/main/scripts/install.sh) install
# 按提示选端口与数据库，装完浏览器打开 http://<服务器>:3000（或反代后的域名）
dhtsearch   # 装后管理菜单：状态 / 升级 / 改端口 / 数据库切换 / 卸载
```

下面是从源码运行（开发调试）的说明。前置：Go 版本以 `server/go.mod` 为准（当前 1.25.6）；前端开发用 Node 22（与 CI 一致）。

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
| `DHT_SAMPLERS` | `8` | BEP-51 并发采样 worker 数（每身份，调大反而降低入库速度，见上） |
| `DHT_IDENTITIES` | `1` | 单进程 DHT 身份数（各自 node ID/路由表，监听 `DHT_PORT..DHT_PORT+N-1`，钳 1–16，需重启） |
| `DHT_IPV6_ENABLED` | `true` | 每身份同端口再起 IPv6 DHT 服务（BEP 32，独立 udp6 socket/路由表）；主机无 IPv6 时自动退化纯 IPv4、不影响启动，需重启 |
| `META_WORKERS` | `128` | 元数据并发 worker 数（入库速度的主要旋钮） |
| `META_SHARDS` | `8` | anacrolix 底座的客户端分片数（按哈希首字节路由，摊薄单客户端全局锁，需重启） |
| `META_FETCHER` | `anacrolix` | 元数据抓取底座：`anacrolix` / `native`（自研薄抓取器）/ `libtorrent`（边车进程，安装/升级时自动下载安装，见「抓取管线」一节），需重启；订阅源抓取器固定 anacrolix |
| `META_TIMEOUT` | `45s` | 单个元数据获取超时 |
| `META_NEG_CACHE_ENABLED` | `true` | 抓取准入负缓存：已入库跳过 + 失败退避（热生效） |
| `META_NEG_CACHE_MAX_BACKOFF_H` | `24` | 失败退避上限小时数（1h 起翻倍，热生效） |
| `CRAWLER_WATCHDOG_ENABLED` | `true` | 入库停转看门狗：停滞打栈、自愈退出（热生效，见「抓取管线」） |
| `ALERT_ENABLED` | `false` | 停滞外发告警总开关：看门狗的停滞/恢复经 Webhook / Telegram 通知（热生效，见「抓取管线」） |
| `ALERT_WEBHOOK_URL` | 无 | 通用 Webhook 地址（POST JSON `{title, text, ts}`，按密钥脱敏，热生效） |
| `ALERT_TELEGRAM_BOT_TOKEN` / `ALERT_TELEGRAM_CHAT_ID` | 无 | Telegram 通道：机器人 token（脱敏）与会话 ID，两者都配才生效（热生效） |
| `ALERT_STALL_MINUTES` | `15` | 停滞判定分钟数 = 看门狗警告阈值，退出阈值为其两倍（需重启） |
| `META_PRESCREEN_ENABLED` | `true` | 元数据早筛：窗口内无任何 peer 连上的 infohash 提前放弃，不占满 `META_TIMEOUT` |
| `META_PRESCREEN_TIMEOUT` | `10s` | 早筛窗口（包含在 `META_TIMEOUT` 之内，不额外叠加） |
| `META_PRIORITY_ENABLED` | `true` | 抓取优先级队列：做种数 > 来源 > 新鲜度（关 = 先进先出） |
| `FETCH_METADATA` | `true` | 是否获取元数据（false 时只收 infohash） |
| `SCRAPE_ENABLED` | `true` | 是否启用 tracker scrape 排序（见「Tracker 刮削排序」） |
| `TRACKERS` | 内置列表 | 逗号分隔的 tracker announce URL，既作 scrape 目标也作获取的 peer 提示；完整默认列表见 `env.example` |
| `RESCRAPE_ENABLED` | `true` | 是否对已入库种子做定期复查刮削（见「Tracker 刮削排序」的定期复查） |
| `RESCRAPE_MIN_AGE_H` | `48` | 快照新于该小时数的不复查；从未刮过的种子不受此限 |
| `RESCRAPE_PER_MIN` | `60` | 复查速率上限（个/分钟，一轮一扣） |
| `MIN_TORRENT_SIZE` | `104857600`（100 MiB） | 低于此总体积的种子不入库 |
| `ENV_FILE` | `.env` | .env 文件路径（相对工作目录） |
| `RATE_LIMIT_RPS` | `3` | 每客户端 IP 的持续请求速率（令牌桶，0 = 关闭限流） |
| `RATE_LIMIT_BURST` | `30` | 令牌桶突发容量 |
| `SEARCH_MAX_INFLIGHT` | `16` | 并发搜索上限，超出直接 503 甩负载 |
| `SEARCH_TIMEOUT` | `10s` | 单次搜索的数据库时间预算 |
| `ADMIN_TOKEN` | 无 | 管理端恢复令牌；未设置且无账号、无第三方登录时 `/api/admin/*` 整体禁用（见下文「管理端」） |
| `GITHUB_CLIENT_ID` / `GITHUB_CLIENT_SECRET` / `GITHUB_ALLOWED_USERS` | 无 | GitHub 登录：OAuth App 凭据与放行用户名（也可在设置页配） |
| `OIDC_ISSUER` / `OIDC_CLIENT_ID` / `OIDC_CLIENT_SECRET` | 无 | SSO（OIDC）登录凭据；另有 `OIDC_ALLOWED_EMAILS` / `OIDC_ALLOWED_DOMAIN` 放行名单、`OIDC_PROVIDER_NAME` 按钮名（均可在设置页配） |
| `PUBLIC_API_ENABLED` | `true` | 公开 API 总开关，关闭时公开接口整体 503 |
| `PUBLIC_API_REQUIRE_TOKEN` | `false` | 是否要求公开接口带 API 令牌（见下文「公开 API 令牌」；自带前台经 `INTERNAL_API_TOKEN` 第一方通道豁免） |
| `INTERNAL_API_TOKEN` | 无 | 第一方内部令牌：web 以 `X-Internal-Token` 头呈现给 API，绕过令牌强制且不计配额；install.sh 在 API 与 web 两侧自动生成一致的值，手动部署需两侧设置相同值 |
| `PUBLIC_API_TOKEN_RATE_PER_MIN` | `600` | 新建令牌的默认每分钟配额（0 = 不限） |
| `SITE_NAME` | `DHTSearch` | 站点名称（标题、页头、OG 默认标题） |
| `THEME_COLOR` | `teal` | 主色预设：`teal` / `blue` / `violet` / `green` / `orange` / `red` |
| `THEME_APPEARANCE` | `system` | 默认外观：`system` / `light` / `dark`（访客本地选择优先） |
| `FILTER_BLOCK_WORDS` | 空 | 逗号分隔的屏蔽词，命中种子名或文件路径即丢弃（单独计数） |
| `FILTER_ADULT_WORDS` | 空 | 逗号分隔，追加到内置成人词表，命中即按成人内容丢弃 |
| `FILTER_BLOCK_DOMAINS` | 空 | 逗号分隔的屏蔽域名（含子域名），命中即按成人内容丢弃 |
| `FILTER_ENABLED` | `true` | 内容过滤总开关，关掉则三类过滤全部放行 |
| `FILTER_ADULT_ENABLED` | `true` | 成人类过滤开关（内置词表/域名/JAV 信号） |
| `FILTER_SPAM_ENABLED` | `true` | 垃圾类过滤开关（结构性规则 + `FILTER_BLOCK_WORDS`） |
| `FILTER_TOO_SMALL_ENABLED` | `true` | 体积下限过滤开关（下限值见 `MIN_TORRENT_SIZE`） |
| `FILTER_MAX_NAME_LEN` | `300` | 名称超过此长度（字符数）判垃圾 |
| `FILTER_STUFFING_MIN_REPEAT` | `4` | 同一词在名称中重复达到此次数判关键词堆叠 |
| `FILTER_NON_ALNUM_MAX_RATIO` | `0.5` | 名称中非字母数字占比超过此值判垃圾 |
| `FILTER_RANDOM_FILE_MAX_RATIO` | `0.3` | 随机字符串文件名占比超过此值判垃圾（文件数 ≥5 才判） |
| `FILTER_ALLOW_WORDS` | 空 | 逗号分隔的例外短语，标题含短语即豁免成人判定（不豁免屏蔽词与垃圾规则） |

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

  按表批量拷贝 `torrents` / `stats` / `blocked` / `swarm_stats` /
  `fetch_attempts` / `search_terms` 等表（表结构同构，字节原样搬运；`dht_nodes` 路由
  快照是运行时状态，不迁移）；目标里已有同 `info_hash`（或同 `key`）
  的行**跳过不覆盖**，所以
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
| `API_BASE` | `http://localhost:8080` | 后端 API 地址，**运行时**读取（改后端端口不用重新构建）；一键安装时由 install.sh 按 API 端口自动写入 web 服务，优先于下面的构建期内联值 |
| `NEXT_PUBLIC_API_BASE` | `http://localhost:8080` | 构建时内联的回退地址（Release 产物构建时写入 `http://127.0.0.1:8080`）；`API_BASE` 已设置时不生效 |
| `NEXT_PUBLIC_GA_ID` | 无 | Google Analytics 衡量 ID（`G-XXXXXXXXXX`），留空则不加载任何分析脚本 |

## API

公开接口（无鉴权，有限流，见「DoS 防护」）：

| 方法与路径 | 说明 |
| --- | --- |
| `GET /api/search?q=xx&page=1&page_size=20` | 搜索（q 为空返回最新收录），结果含拼好的 magnet 链接（带 tracker 参数）与做种信息（`seeders`/`leechers`/`completed`/`scraped_at`，无数据为 null）及健康度（`health`）。可选 `category=video\|audio\|image\|doc\|software\|archive\|other` 按主文件扩展名分类筛选、`sort=relevance\|newest\|size\|seeders` 排序（relevance 为加权评分，seeders 无数据垫底）、`hide_dead=1` 隐藏疑似死种；响应附各分类计数（`categories`）与 tracker 列表（`trackers`） |
| `GET /api/torrents/{hash}/similar` | 相似种子：名称关键词重叠 + 同分类优先的简单推荐（≤12 条） |
| `GET /api/torrents/{hash}/files?page=1&page_size=100` | 文件列表分页（默认 100 条/页，上限 500），响应含 `total`（已收录条数）/`total_pages`。搜索结果与管理详情只内嵌/返回第一页，完整列表走此接口，避免大文件种子撑爆响应与页面 |
| `GET /rss?q=xx&category=video` | 关键词订阅 RSS 2.0（最新在前，≤50 条，条目链接为磁力）；无 q 输出全站最新。前端同路径 `/rss` 为代理入口 |
| `POST /api/report` | 访客举报：`{info_hash, reason: adult\|spam\|infringement\|other, note?}`，落 reports 表；同访客对同种子限流，超限 429 |
| `GET /api/stats` | 收录数、成人/垃圾/体积过滤计数、LLM 审核统计、爬虫/刮削/RSS 状态 |
| `GET /api/trending` | 豆瓣热门榜单（欧美电影、美剧、日剧、韩剧四类标题 + 更新时间），供首页快捷搜索词 |
| `GET /api/trending-terms?window=today\|7d&limit=20` | 本站热门搜索：成功搜索的匿名聚合计数（按归一化词 + UTC 天分桶，只存词/计数/展示大小写，无 IP、无 UA、无任何访客级记录），`terms` 按计数降序、含 `count`；首页据此渲染「热门搜索」行。分桶保留 90 天 |
| `GET /api/healthz` | 健康检查，返回 `{"ok":true}`，不限流 |
| `GET /api/site` | 站点名称与主题（主色预设、默认外观），供前端渲染，无需鉴权 |

### 公开 API 令牌（可选鉴权）

公开接口默认无鉴权。要把搜索 API 开给外部客户端、又不想完全敞开时，
可以在管理端「API 令牌」页（`/admin/api-tokens`）签发令牌，并在
「设置 → 公开 API」里打开 `PUBLIC_API_REQUIRE_TOKEN`：

- **令牌形态**：`dht_` + 64 位十六进制随机串（256 位熵）。创建时明文
  **只显示一次**，后端只存 SHA-256 哈希（随机令牌熵足够高，快哈希
  即可防爆破，也省去逐请求校验的 CPU 开销）；列表里只显示前缀
  （`dht_` + 前 8 位）用于区分。禁用立即生效（403），删除立即失效
  （401）。
- **携带方式**：`Authorization: Bearer <token>` 或 `X-API-Key: <token>`。
- **错误码**：无令牌/错令牌 `401`、令牌被禁用 `403`、超令牌配额
  `429`、公开 API 总开关（`PUBLIC_API_ENABLED`）关闭时 `503`。
  `/api/healthz` 永远不鉴权、不限流。
- **限速叠加**：每 IP 令牌桶（默认 3 req/s）对所有请求照常生效；
  令牌另有自己的每分钟配额（新建默认 600 次/分钟，可在设置里改
  默认值，也可在令牌页逐个改，0 = 不限）。两道都过才放行，任一
  触发都是 `429`。
- **未开启强制时**：接口保持匿名可用，但请求若带了有效令牌会被
  计数（令牌页能看调用量与最后使用时间），可先观察再切换强制。
- **第一方豁免**：自带前台也是调用这些接口的，但它走内部通道：
  install.sh 在 API 与 web 两侧生成同一个随机 `INTERNAL_API_TOKEN`，
  web 的服务端请求以 `X-Internal-Token` 头呈现，命中即绕过强制
  闸门且不计入任何令牌的配额——开启强制只拦外部调用，站点自身
  搜索不受影响。该令牌只存环境变量、不入库、不出现在令牌列表、
  不打日志；手动部署时需在两侧设置相同值，缺失时设置页保存会
  给出警告、前台会回到被 401 拦截的状态。`PUBLIC_API_ENABLED`
  总开关关闭时第一方同样返回 503（这是有意的整体停用）。

管理接口（全部需要 `Authorization: Bearer <会话令牌或 ADMIN_TOKEN>`，无任何登录方式时整体 503，见「管理端」）：

| 方法与路径 | 说明 |
| --- | --- |
| `POST /api/admin/login` | 校验恢复令牌并签发会话（单独限流：每客户端令牌桶容量 5 次、每 10 秒恢复 1 次，超限 429） |
| `POST /api/admin/auth/password` | 用户名密码登录并签发会话 |
| `POST /api/admin/auth/github/exchange` / `POST /api/admin/auth/oidc/exchange` | 第三方登录回调交换（前端 OAuth 路由调用） |
| `GET /api/admin/auth/config` | 登录页用：已启用的登录方式（公开，无敏感信息） |
| `GET /api/admin/me` / `POST /api/admin/logout` / `POST /api/admin/me/password` | 当前身份 / 退出（吊销会话） / 自助改密（踢掉其他会话） |
| `GET/POST /api/admin/users`、`POST/DELETE /api/admin/users/{id}` | 账号管理（仅 admin 角色） |
| `GET /api/admin/overview` | 概览：总数/今日新增/未审核/已审核/已拉黑/待处理举报、管线与审核计数、过滤开关状态、爬虫/刮削/榜单状态、数据库后端 |
| `GET /api/admin/trends` | 入库趋势分桶（`range=24h` 按小时 / `14d` 按天），概览趋势图数据 |
| `GET /api/admin/moderation` | LLM 审核状态：启用/Key 是否已设/模型、队列计数、最近审核记录 |
| `POST /api/admin/moderation/test` | 测试连接：向配置的端点真发一次最小请求（后端去抖 5 秒），可带未保存的地址/模型/Key 试探 |
| `GET/POST /api/admin/api-tokens`、`POST /api/admin/api-tokens/{id}/enable\|disable`、`PUT /api/admin/api-tokens/{id}/rate`、`DELETE /api/admin/api-tokens/{id}` | 公开 API 令牌的签发与管理（明文只在创建响应里出现一次） |
| `GET /api/admin/reports?status=pending\|resolved\|dismissed\|all` | 访客举报队列（分页，附种子现名）；概览响应含 `reports_pending` 待处理数 |
| `POST /api/admin/reports/{id}/resolve` | 举报标记已处理 |
| `POST /api/admin/reports/{id}/dismiss` | 举报忽略 |
| `POST /api/admin/reports/{id}/block` | 一键拉黑被举报种子（删除+黑名单）并标记已处理 |
| `GET /api/admin/torrents` | 种子列表（分页 + 关键词 + 审核状态/分类/大小过滤 + 排序：`sort=newest\|oldest\|size_desc\|size_asc\|files_desc\|seeders`、`category`、`size=lt10m\|10m_100m\|100m_1g\|1g_10g\|gt10g`，非法值回落默认） |
| `GET /api/admin/torrents/{hash}` | 种子详情（不含文件列表，文件走下一行分页接口，避免大文件种子撑爆响应） |
| `GET /api/admin/torrents/{hash}/files?page=1&page_size=100` | 种子文件列表分页（默认 100 条/页，上限 500，支持 `q` 按文件名过滤；`total` 为过滤后条数、`total_all` 为全量） |
| `POST /api/admin/torrents/{hash}/approve` | 标记已审核 |
| `POST /api/admin/torrents/{hash}/block` | 拉黑（删除并记入黑名单，不再收录） |
| `DELETE /api/admin/torrents/{hash}` | 只删记录（可被重新收录） |
| `POST /api/admin/torrents/batch` | 批量通过/拉黑/删除（单次 ≤100 条） |
| `GET /api/admin/update` | 系统更新状态：已装/最新版本、自动升级开关与上次结果、进行中升级的进度（`progress`/`phase`，无进度标记时为 null） |
| `POST /api/admin/update/check` | 检查更新（只查 GitHub 最新 Release，1 小时缓存） |
| `POST /api/admin/update/request` | 请求立即升级（写契约文件，由 root 侧升级代理执行；仅 admin 角色，已有升级进行中返回 409） |
| `GET /api/admin/blocked` | 黑名单列表 |
| `DELETE /api/admin/blocked/{hash}` | 取消拉黑 |
| `GET /api/admin/settings` | 全量配置：分组、每项生效值、来源（数据库/环境变量/默认）、敏感项脱敏 |
| `PUT /api/admin/settings` | 批量保存运行配置：整批类型/范围校验，任一项非法整批拒绝并逐项报错 |

## 管理端

入口：前端 `/admin`（概览 `/admin`、种子管理 `/admin/torrents`、未审核队列
`/admin/review`、黑名单 `/admin/blocked`、举报队列 `/admin/reports`、LLM 审核
`/admin/llm`、API 令牌 `/admin/api-tokens`、账号 `/admin/users`、设置
`/admin/settings`）。默认关闭：没有任何登录方式时后端在启动日志里打印
`admin: disabled`，所有 `/api/admin/*` 返回 503，前端显示「管理端未启用」提示。
最简单的启用方式仍是设置环境变量 `ADMIN_TOKEN`（足够长的随机串，如
`openssl rand -hex 32`）并重启——它此后是**恢复令牌**，见下文「登录方式」。

功能：

- **概览**：按管线健康重排的仪表盘：顶部是五段管线
  （DHT 网络 → 采样发现 → 元数据抓取 → 内容过滤 → 入库）的实时动画，
  由 8 秒轮询的真实增量驱动（无增量即静止、抓取停滞段变警告色并挂
  徽标、轮询失败冻结并标过期；「入库」显示库内当前总数，与种子总数
  同口径）；其下是种子总数 / 今日新增 / 未审核 / 已审核 / 已拉黑 /
  待处理举报六卡（带占比）、入库趋势折线（24 小时 / 14 天）、过滤
  构成环图、爬虫 / 刮削 / LLM 审核 / 榜单与系统四个状态面板、最新
  入库实时流。榜单抓取失败时红字提示「正在展示上次榜单」。
- **种子管理**：分页列表（关键词 + 审核状态过滤）、详情（文件列表分页），
  单条通过（标记已审核）/ 拉黑（删除并记入黑名单，不再收录）/ 删除
  （只删记录，可被重新收录）。注意审核只是队列标记：**未审核的种子
  照常在前台公开、可搜索**，只有拉黑才对访客隐藏、删除才移除记录。
- **未审核队列**：只看未审核，支持多选批量通过 / 批量拉黑 / 批量删除
  （操作浮动条）。
- **黑名单**：查看 blocked 表，逐条取消拉黑。
- **举报队列**：访客举报（成人/垃圾/侵权/其他 + 备注）逐条处理：
  标记已处理 / 忽略 / 一键拉黑被举报种子。
- **LLM 审核**：审核专页。启用状态与队列计数（待审 / 已审 / 删除 /
  出错）、审核设置（开关、端点、Key 脱敏、模型、试运行、标题清理等，
  与设置页同一份配置）、「测试连接」（真发一次最小请求验端点与模型，
  可先填未保存的值试探）、最近审核记录（种子名 / 结论 / 处理 / 时间，
  落库保留最近 1000 条）。设置页的「LLM 审核」组只留入口卡片跳到此页。
- **API 令牌**：签发/禁用/删除公开 API 令牌、逐令牌改每分钟配额；
  明文只在创建时显示一次（见上文「公开 API 令牌」）。
- **账号管理**：建号、改角色、禁用、重置密码、删号（见下文「登录方式」）。
- **设置**：全部 90 项配置可视化（16 组：服务与数据库 / 系统更新 /
  数据保留 / 告警通知 / 节点接入 / 登录与账号 / 外观与主题 / 爬虫 / 元数据抓取 / 刮削排序 / 内容过滤 /
  LLM 审核 / 趋势 / 订阅源 / 限流与搜索 / 公开 API）。设置首页是分组
  索引（每组一张卡：说明、项数、只读/需重启/已自定义标记），每组一个
  独立子页编辑。启动配置（监听地址、数据库路径/连接串、管理令牌）
  只读展示，改 env 文件后重启生效；运行配置在线可改，存数据库覆盖层，
  优先级 **数据库 > 环境变量 > 默认值**，多数即时热生效，结构性参数
  （worker 数、端口等）明确标注「需重启」。密钥类脱敏展示、永不回显
  明文。「外观与主题」组含站点名称、主色预设（六种，全站 CSS 变量
  切换）、默认外观；前台与管理端右上角另有外观切换（浅 / 深 / 跟随
  系统），访客选择同步存 cookie 由服务端直接渲染，刷新/换页不回滚、
  无闪烁。「系统更新」子页顶部是升级卡（见下文一键部署的面板升级）。

**登录方式**（登录页按已启用的方式动态组合）：

- **用户名密码**：管理员账号存在时启用。账号在 `/admin/users` 里建
  （建号、改角色、禁用、重置密码、删号）；角色分 `admin`（全部权限）
  与 `reviewer`（仅概览、种子审核、黑名单——进不了设置与账号管理）。
  密码只存 bcrypt 哈希。首次部署还没有账号时，先用恢复令牌登录，
  登录页与账号页都会引导创建第一个管理员。
- **GitHub 登录**：在 https://github.com/settings/developers 建 OAuth
  App，Authorization callback URL 填 `<站点地址>/api/auth/github/callback`，
  把 Client ID/Secret 与放行用户名填进设置页「登录与账号」组（或同名
  环境变量）；名单外的 GitHub 用户即使授权成功也不会建号、不会发会话。
- **OIDC SSO**：任意标准 OIDC Provider（discovery + RS256 id_token
  全项校验），回调地址 `<站点地址>/api/auth/oidc/callback`；放行名单
  （邮箱/域名）皆空时 fail-closed。SAML 不支持。
- **恢复令牌**：`ADMIN_TOKEN` 永久保留，登录页折叠在「恢复令牌」入口
  里；适合忘密码/第三方登录配错时兜底。

鉴权：所有 `/api/admin/*` 都要 `Authorization: Bearer <会话令牌或
ADMIN_TOKEN>`。任何登录方式成功后都换发**服务端会话**（cookie 只存
会话令牌、库里只存其 SHA-256，24 小时；登出即吊销，改密踢掉其他
会话，管理员禁用/删号踢掉其全部会话）。登录接口单独限流（令牌桶：每键容量 5 次、可连续尝试 5 次，第 6 次起
返回 429，之后每 10 秒恢复 1 次，即持续速率 6 次/分钟；每次尝试——
不论成败——都消耗一个令牌。令牌登录按客户端地址计键，账密登录按
客户端地址+用户名双键计键，GitHub/OIDC 交换按客户端地址+提供商计键）。
管理接口豁免公开
API 的每 IP 限流（有自己的登录限流），且限流参数有下限保护，改不出
把管理员自己锁死的组合。前端把会话存进 httpOnly cookie，页面脚本
读不到，浏览器端操作经 Next 的 `/api/admin-action` 代理转发。

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

命中任一即丢弃并计入统计（`adult_filtered` / `spam_filtered` / `size_filtered` /
`blocked_filtered`）。规则见 `server/internal/filter/`。

三类过滤各有开关（`FILTER_ENABLED` 总开关 + `FILTER_ADULT_ENABLED` /
`FILTER_SPAM_ENABLED` / `FILTER_TOO_SMALL_ENABLED`），垃圾规则的关键阈值
（名称长度、关键词重复次数、符号占比、随机文件名占比）也可调，全部在
管理端设置页「内容过滤」组热改（或用同名环境变量）；`FILTER_ALLOW_WORDS`
是例外短语表，标题含短语即豁免成人判定（不豁免屏蔽词与垃圾规则）。
词表同样可自定义：`FILTER_BLOCK_WORDS` 是运营者自己的屏蔽词（单独计数），
`FILTER_ADULT_WORDS` / `FILTER_BLOCK_DOMAINS` 把自定义词与域名追加到内置
成人词表/域名判定之上（内置词表是底座，不可删改）。内容过滤组还提供
宽松 / 标准 / 严格 / 成人模式四套模板，一键填充开关与阈值（不自动保存、
不动自定义词表）；成人模式即标准参数关闭成人过滤，成人内容将直接入库。
关掉某类后该类计数随之冻结，概览页「过滤构成」面板会显示四个开关的
当前状态。

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

子命令：`install` / `upgrade` / `status` / `uninstall`，外加运维子命令 `start` / `stop` / `restart`、`check-update`（只查不升，退出码 0 已最新 / 2 有新版 / 1 查询失败）、`backup` / `backups` / `restore`、`selfcheck`、`config`（脱敏打印配置）、`set-token`、`autostart on|off`、`auto-update on|off`（无参在终端里运行给交互菜单，菜单里是同一套能力）。常用选项：`--version vX.Y.Z` 指定版本（默认最新 Release）、`--file <tar.gz>` 用本地包安装（版本号从包名解析，`status` 显示真实版本）、`--from-source` 无 Release 产物时从源码构建兜底（需要 git + Go + Node）、`--dry-run` 只写配置与 unit 文件、不动 systemd（自检/排障用）、`uninstall --purge` 连配置和数据一起删（默认保留）。参数与安装包在动手前先校验，坏参数不会留半成品。

自定义端口：`install --api-port 18080 --web-port 13000`（也可预先设环境变量 `API_PORT` / `WEB_PORT`；优先级为 参数 > 环境变量 > 已有配置 > 默认 8080/3000）。API 端口写进配置文件的 `HTTP_ADDR`，同时生成给前端 unit 的 `API_BASE=http://127.0.0.1:<api-port>`——前端在服务端启动时读这个运行时变量（不写死在构建产物里），所以改后端端口不用重打 Release 包，前端会自动跟过去。upgrade 不带端口参数时保留已有端口不回退默认。启动服务前脚本会先检查两个端口：被其他进程占用会直接报出占用进程并退出，不会等到 120 秒健康检查超时（被本服务自己占着属正常，不误报）。

数据库选择：`install --db sqlite|postgres-local|postgres-url`（交互安装时在端口之后也会问）。默认 `sqlite`；`postgres-local` 在本机自动安装并配置 PostgreSQL（Debian/Ubuntu 走 apt，其他发行版会停下来给手动指引）：检测到已有可用的本机 PG 就复用，否则自动安装，建 `dhtsearch` 角色和同名库、生成随机密码，把 `DATABASE_URL` 写进配置文件，安装汇总里把完整连接地址（含密码）只打印一次请保存；`postgres-url --database-url postgres://...` 用已有 PG，写配置前先实测连通性（用发布包里的二进制跑一次空迁移），连不上直接中止、不写配置。已有配置的机器重复 install 时数据库后端保持不变（除非显式给 `--db`），upgrade 永远不动数据库选择。配置文件因为可能带数据库密码，权限收紧为 600（root 专属；systemd 以 root 读 EnvironmentFile 后注入进程环境，服务不受影响）。

装后切换数据库用 `dhtsearch` 菜单的「数据库切换」：显示当前后端（SQLite 给文件路径，PG 给脱敏后的 URL），选目标（切 PG 同样有本机自动装 / 已有地址两条路），确认后严格按 停服务 → `dhtsearch-server migrate` 迁数据并核对行数 → 改 `DATABASE_URL` → 启服务 → 健康检查 的顺序执行；迁移失败或行数对不上就不改配置、按原配置把服务拉回来，新后端健康检查不过同样回滚。切回 SQLite 时数据从 PG 迁回 `DB_PATH` 文件（文件里已有的同 hash 旧行不覆盖）。

装完的布局：

| 路径 | 内容 |
| --- | --- |
| `dhtsearch.service` / `dhtsearch-web.service` | API+爬虫 / 前端两个 systemd 单元，`enable --now` |
| `/usr/local/bin/dhtsearch-server`、`/opt/dhtsearch/web/` | 二进制与前端产物 |
| `/usr/local/bin/dhtsearch` | 管理脚本入口（install 时由 install.sh 自身复制），无参运行开交互菜单：顶部多行状态头（版本/服务/数据库/自动更新状态，后台查到的新版本会直接提示），条目按任务域分组——服务（状态/日志/重启/启动/停止）、更新（检查更新/升级/自动更新设置）、配置（改端口/爬虫节点/数据库切换/配置管理/开机自启）、数据（备份与恢复/自检）、系统（安装/卸载），选项标签带 `[开/关]` 状态；与服务二进制 `dhtsearch-server` 是两个文件 |
| `/etc/dhtsearch/dhtsearch.env` | 配置（端口、数据库：默认 SQLite 的 `DB_PATH`，或 `--db` 写入的 `DATABASE_URL`），权限 600，重复 install / upgrade 不覆盖 |
| `/var/lib/dhtsearch/` | 数据库、`VERSION`、自动更新状态文件 `autoupdate`、面板↔脚本的契约文件（更新：`update-panel` / `update-request` / `update-status`；重启：`restart-request` / `restart-status`)、备份（`backups/`：upgrade 的回滚备份 + `db-*` 数据库备份） |

菜单和子命令还提供一套日常运维能力：「启动 / 停止」与「重启」分开，「开机自启」开关两个服务的 enable 状态；「配置管理」可脱敏查看当前配置、设置 / 重置 `ADMIN_TOKEN`（随机生成只显示一次）、用编辑器改 env（改完提示重启）；「备份与恢复」按当前后端备份数据库（SQLite 复制文件、PostgreSQL 走 `pg_dump`）到 `backups/db-*`，恢复时先停服并给当前库留一份 `db-pre-restore-*` 快照；「自检」一次跑完服务状态、端口监听、磁盘余量、版本、数据库连通和近 24 小时 error 日志摘要；「检查更新」只查不升并把结果记下来，`status` 会显示上次检查时间和结果。

自动更新默认关闭。在菜单「自动更新」或 `install.sh auto-update on daily|weekly` 开启后，会装一个 systemd 定时器 `dhtsearch-update.timer`（每天或每周一凌晨 03:37，带最多 30 分钟随机延迟，关机错过会在开机后补跑），到点执行 `dhtsearch upgrade --yes`——走的还是手动升级同一套流程（先备份、健康检查、失败自动回滚），没有第二套升级逻辑。开关状态、频率和上次检查结果存在 `/var/lib/dhtsearch/autoupdate`（脚本自己的状态文件，不进服务配置文件），`status` 里能看到；关闭或卸载时定时器会一并移除。

标注「重启后生效」的设置（监听地址、数据库、抓取底座等）保存后可以在面板里直接重启服务，不用再开 SSH：「服务与数据库」子页顶部有重启卡，点「重启服务」后面板把请求写进契约文件 `restart-request`，同一个升级代理在一分钟内以 root 身份只重启 `dhtsearch.service`（Web 服务不重启，面板页面本身不中断），并把结果写回 `restart-status`；重启期间管理接口短暂不可达属正常，卡片会显示「重启中…」并在服务恢复后确认「已重启」。

也可以在管理面板里管升级：「系统更新」子页顶部有升级卡（当前/最新版本、检查更新、立即升级、自动升级状态与上次结果），同页有自动升级开关与时间设置。升级执行时卡片显示真实进度条——脚本在下载 → 校验 → 备份 → 安装 → 重启各阶段把进度写进 `update-status`，面板 5 秒轮询呈现，失败时进度冻结在出事的阶段。面板进程以服务用户运行、无 root，绝不直接执行升级——它只把意图写进数据目录的契约文件（`update-panel` / `update-request`)，由 install.sh 安装的每分钟定时器 `dhtsearch-upgrade-request.timer`（升级代理）以 root 身份消费：同步自动升级定时器的开关与时间、执行面板请求的升级，并把结果写回 `update-status` 供面板展示。所有入口（菜单、CLI、自动更新、面板请求）的升级都先过版本闸：目标版本与已装相同直接报「已是最新版本」退出（不下载、不备份、不动服务），目标更旧则拒绝——没有新版时不会再白走一遍停服升级流程。升级备份用硬链接快照（`cp -al`，近乎瞬时；跨文件系统退化为 tar/全量拷贝），并自动轮转：升级快照保留最近 3 组、数据库备份保留最近 10 份。优先级：面板保存过更新设置后以面板为准（契约文件存在即生效），`install.sh auto-update` 命令会删掉该文件、把控制权收回 CLI；面板从未保存过时一直以 CLI 状态为准。注意：管理入口脚本本身随 Release 包更新——从菜单里发起的升级会用新包里的 install.sh 刷新 `/usr/local/bin/dhtsearch`，所以自动更新的定时器永远调到的是与已装版本配套的脚本。

交互菜单里选「安装」会依次询问 API 端口和 Web 端口（带默认值、校验 1-65535），新装时还会问数据库（SQLite / PostgreSQL 本机自动装 / PostgreSQL 已有地址），打印汇总确认后才执行；「修改端口」显示当前端口、询问新端口，保存后重启两个服务并跑健康检查；「数据库切换」显示当前后端、选目标、二次确认并提示短暂停机后执行带数据迁移的切换；升级 / 重启 / 改端口 / 切库 / 卸载在菜单里执行前都要二次确认，卸载可选择保留数据或 `--purge` 全删。

API 默认只听 `127.0.0.1:8080`、前端听 `127.0.0.1:3000`，公网访问请在前面加反向代理。日志：`journalctl -u dhtsearch -f`（前端 `-u dhtsearch-web`）；健康检查：`curl http://127.0.0.1:8080/api/healthz`（install/upgrade 会等它最多 120 秒，失败打印日志尾巴；端口改过的话换成实际端口）。

Release 产物由 `.github/workflows/release.yml` 构建：`dhtsearch-<version>-linux-<amd64|arm64>.tar.gz` + 对应 `.sha256`，install 时先验 sha256 再安装。本地等价打包用 `./scripts/package-release.sh --version vX.Y.Z --arch amd64`。

发版分自动和手动两条路：

- **自动发版**（`.github/workflows/auto-release.yml`）：push 到 `main` 时，只有改动包含业务代码才发版，版本号在现有最大 `vX.Y.Z` tag 基础上 patch +1（仓库还没有 tag 时首个版本为 `v0.1.0`）。业务代码白名单由 `scripts/release-scope.sh` 判定（后端 `server/` 的 Go 代码与 go.mod/go.sum、前端 `web/app|components|lib|public` 与 web 构建配置，外加 `scripts/install.sh`——它是用户每天在跑的出厂管理入口，改动算产品变更），只改文档、其他 `scripts/`、`.github/` 不发版；业务代码与文档混在一起照常发。HEAD 已有版本 tag、或提交信息含 `[skip release]` 时跳过。连续多次 push 只发最新一次（旧的运行会被取消）。判断与版本号计算可用 `./scripts/test-release-tools.sh` 本地单测。
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
| `server/internal/crawler/` | DHT 爬虫（被动监听 + BEP 51 采样、多身份、节点质量评分、peer 提示缓存） |
| `server/internal/scraper/` | BEP 15 tracker scrape 排序 + swarm 快照落库 + 复查刮削引擎 |
| `server/internal/rescrape/` | 已入库种子定期复查（选最陈旧快照、低速率、队列满跳过） |
| `server/internal/metadata/` | BEP 9 元数据获取（优先级队列、早筛、分片客户端） |
| `server/internal/metadata/native/` | 自研薄抓取器底座（`META_FETCHER=native`） |
| `server/internal/admission/` | 抓取准入闸（已入库跳过 + 失败退避负缓存） |
| `server/internal/watchdog/` | 入库停转看门狗（停滞/爬行检测、打栈、自愈退出、事件钩子） |
| `server/internal/alerts/` | 停滞外发告警（Webhook / Telegram 投递、轮次去重、退出前同步刷送） |
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
