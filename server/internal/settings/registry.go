package settings

// Setting keys. They deliberately reuse the environment variable
// names the server has always read, so the env layer, the env file
// and the settings page all talk about the same knob by the same
// name. FILTER_BLOCK_WORDS and the appearance keys are new with the
// settings system (env support included, for symmetry).
const (
	// service (startup, read-only)
	KeyHTTPAddr    = "HTTP_ADDR"
	KeyDBPath      = "DB_PATH"
	KeyDatabaseURL = "DATABASE_URL"
	KeyAdminToken  = "ADMIN_TOKEN"
	KeyEnvFile     = "ENV_FILE"
	// system update: the panel records intent only. The API process
	// runs unprivileged and never executes an upgrade; saving these
	// syncs them to a state file in the data dir that the root-side
	// installer script reads (see internal/api/update.go and the
	// upgrade agent in scripts/install.sh).
	KeyAutoUpdateEnabled = "AUTO_UPDATE_ENABLED"
	KeyAutoUpdateTime    = "AUTO_UPDATE_TIME"
	// data retention: periodic deletion of provably-dead torrents
	// (see internal/retention). Off by default — deletion is
	// irreversible, so the operator turns it on deliberately after
	// reading the eligible count on the settings page.
	KeyRetentionEnabled    = "RETENTION_ENABLED"
	KeyRetentionMinAgeDays = "RETENTION_MIN_AGE_DAYS"
	KeyRetentionIntervalH  = "RETENTION_INTERVAL_H"
	// stall alerts: the watchdog's episodes leave the machine
	// (see internal/alerts). The channel knobs are live; the stall
	// threshold is wired into the watchdog at startup, so it is a
	// restart item like the other structural knobs.
	KeyAlertEnabled          = "ALERT_ENABLED"
	KeyAlertWebhookURL       = "ALERT_WEBHOOK_URL"
	KeyAlertTelegramBotToken = "ALERT_TELEGRAM_BOT_TOKEN"
	KeyAlertTelegramChatID   = "ALERT_TELEGRAM_CHAT_ID"
	KeyAlertStallMinutes     = "ALERT_STALL_MINUTES"
	// appearance
	KeySiteName        = "SITE_NAME"
	KeyThemeColor      = "THEME_COLOR"
	KeyThemeAppearance = "THEME_APPEARANCE"
	// crawler
	KeyCrawlEnabled           = "CRAWL_ENABLED"
	KeyDHTPort                = "DHT_PORT"
	KeyDHTSamplers            = "DHT_SAMPLERS"
	KeyDHTIdentities          = "DHT_IDENTITIES"
	KeyDHTIPv6Enabled         = "DHT_IPV6_ENABLED"
	KeyFetchMetadata          = "FETCH_METADATA"
	KeyCrawlerWatchdogEnabled = "CRAWLER_WATCHDOG_ENABLED"
	// metadata
	KeyMetaWorkers             = "META_WORKERS"
	KeyMetaShards              = "META_SHARDS"
	KeyMetaFetcher             = "META_FETCHER"
	KeyMetaLTSidecar           = "META_LT_SIDECAR"
	KeyMetaTimeout             = "META_TIMEOUT"
	KeyMetaPrescreenEnabled    = "META_PRESCREEN_ENABLED"
	KeyMetaPrescreenTimeout    = "META_PRESCREEN_TIMEOUT"
	KeyMetaPriorityEnabled     = "META_PRIORITY_ENABLED"
	KeyMetaNegCacheEnabled     = "META_NEG_CACHE_ENABLED"
	KeyMetaNegCacheMaxBackoffH = "META_NEG_CACHE_MAX_BACKOFF_H"
	KeyMinTorrentSize          = "MIN_TORRENT_SIZE"
	// scrape
	KeyScrapeEnabled   = "SCRAPE_ENABLED"
	KeyTrackers        = "TRACKERS"
	KeyRescrapeEnabled = "RESCRAPE_ENABLED"
	KeyRescrapeMinAgeH = "RESCRAPE_MIN_AGE_H"
	KeyRescrapePerMin  = "RESCRAPE_PER_MIN"
	// static content filtering
	KeyFilterEnabled            = "FILTER_ENABLED"
	KeyFilterAdultEnabled       = "FILTER_ADULT_ENABLED"
	KeyFilterSpamEnabled        = "FILTER_SPAM_ENABLED"
	KeyFilterTooSmallEnabled    = "FILTER_TOO_SMALL_ENABLED"
	KeyFilterMaxNameLen         = "FILTER_MAX_NAME_LEN"
	KeyFilterStuffingMinRepeat  = "FILTER_STUFFING_MIN_REPEAT"
	KeyFilterNonAlnumMaxRatio   = "FILTER_NON_ALNUM_MAX_RATIO"
	KeyFilterRandomFileMaxRatio = "FILTER_RANDOM_FILE_MAX_RATIO"
	KeyFilterBlockWords         = "FILTER_BLOCK_WORDS"
	KeyFilterAdultWords         = "FILTER_ADULT_WORDS"
	KeyFilterBlockDomains       = "FILTER_BLOCK_DOMAINS"
	KeyFilterAllowWords         = "FILTER_ALLOW_WORDS"
	// LLM moderation
	KeyModerationEnabled    = "MODERATION_ENABLED"
	KeyOpenAIAPIKey         = "OPENAI_API_KEY"
	KeyOpenAIBaseURL        = "OPENAI_BASE_URL"
	KeyOpenAIModel          = "OPENAI_MODEL"
	KeyModerationInterval   = "MODERATION_INTERVAL"
	KeyModerationTimeout    = "MODERATION_TIMEOUT"
	KeyModerationBatchSize  = "MODERATION_BATCH_SIZE"
	KeyModerationMaxBatches = "MODERATION_MAX_BATCHES"
	KeyModerationDryRun     = "MODERATION_DRY_RUN"
	KeyModerationTrimTitles = "MODERATION_TRIM_TITLES"
	// trending
	KeyTrendingEnabled  = "TRENDING_ENABLED"
	KeyTrendingInterval = "TRENDING_INTERVAL"
	KeyTrendingLimit    = "TRENDING_LIMIT"
	// feeds
	KeyFeedsEnabled     = "FEEDS_ENABLED"
	KeyFeeds            = "FEEDS"
	KeyFeedsKeywordURL  = "FEEDS_KEYWORD_URL"
	KeyFeedsInterval    = "FEEDS_INTERVAL"
	KeyFeedsWorkers     = "FEEDS_WORKERS"
	KeyFeedsMetaTimeout = "FEEDS_META_TIMEOUT"
	// rate limiting & search
	KeyRateLimitRPS      = "RATE_LIMIT_RPS"
	KeyRateLimitBurst    = "RATE_LIMIT_BURST"
	KeySearchMaxInflight = "SEARCH_MAX_INFLIGHT"
	KeySearchTimeout     = "SEARCH_TIMEOUT"
	// public API tokens
	KeyPublicAPIEnabled         = "PUBLIC_API_ENABLED"
	KeyPublicAPIRequireToken    = "PUBLIC_API_REQUIRE_TOKEN"
	KeyPublicAPITokenRatePerMin = "PUBLIC_API_TOKEN_RATE_PER_MIN"
	// auth: admin accounts, GitHub OAuth, OIDC SSO. All live — the
	// auth handlers read them through the Manager per request, so
	// saving here changes the login page's offered methods at once.
	KeyGitHubClientID     = "GITHUB_CLIENT_ID"
	KeyGitHubClientSecret = "GITHUB_CLIENT_SECRET"
	KeyGitHubAllowedUsers = "GITHUB_ALLOWED_USERS"
	KeyOIDCIssuer         = "OIDC_ISSUER"
	KeyOIDCClientID       = "OIDC_CLIENT_ID"
	KeyOIDCClientSecret   = "OIDC_CLIENT_SECRET"
	KeyOIDCProviderName   = "OIDC_PROVIDER_NAME"
	KeyOIDCAllowedEmails  = "OIDC_ALLOWED_EMAILS"
	KeyOIDCAllowedDomain  = "OIDC_ALLOWED_DOMAIN"
)

// Groups in settings-page display order.
var Groups = []Group{
	{Key: "service", Label: "服务与数据库"},
	{Key: "update", Label: "系统更新"},
	{Key: "retention", Label: "数据保留"},
	{Key: "alerts", Label: "告警通知"},
	{Key: "auth", Label: "登录与账号"},
	{Key: "appearance", Label: "外观与主题"},
	{Key: "crawler", Label: "爬虫"},
	{Key: "metadata", Label: "元数据抓取"},
	{Key: "scrape", Label: "刮削排序"},
	{Key: "filter", Label: "内容过滤"},
	{Key: "moderation", Label: "LLM 审核"},
	{Key: "trending", Label: "趋势"},
	{Key: "feeds", Label: "订阅源"},
	{Key: "ratelimit", Label: "限流与搜索"},
	{Key: "publicapi", Label: "公开 API"},
}

// Theme color presets offered for THEME_COLOR. The hex values live
// in the web app (web/lib/theme.ts) and must stay in sync with these
// keys; the server only validates and stores the key.
var ThemeColors = []string{"teal", "blue", "violet", "green", "orange", "red"}

// Registry is every configuration item the server knows about, in
// display order within each group. Defaults match the historical
// env/flag defaults in cmd/server so a settings-free deployment
// behaves exactly as before.
var Registry = []Item{
	// --- service: startup-only, shown read-only. Restart=true even
	// though Editable=false: the snapshot's restart flag states when a
	// change takes effect, and every value here is captured exactly
	// once at process start (listener bind, store open, token read,
	// env-file load), so editing the env file only bites on restart —
	// the Desc lines have always said so; the flag now agrees. ---
	{Key: KeyHTTPAddr, Group: "service", Label: "HTTP 监听地址", Type: TypeString, Default: ":8080", Restart: true,
		Desc: "在 /etc/dhtsearch/dhtsearch.env 里修改，重启生效"},
	{Key: KeyDBPath, Group: "service", Label: "SQLite 数据库路径", Type: TypeString, Default: "./dhtsearch.db", Restart: true,
		Desc: "配置了 DATABASE_URL 时忽略。在 env 文件里修改，重启生效"},
	{Key: KeyDatabaseURL, Group: "service", Label: "Postgres 连接串", Type: TypeSecret, Sensitive: true, Default: "", Restart: true,
		Desc: "设置后使用 Postgres 代替 SQLite。在 env 文件里修改，重启生效"},
	{Key: KeyAdminToken, Group: "service", Label: "管理端令牌", Type: TypeSecret, Sensitive: true, Default: "", Restart: true,
		Desc: "只从环境读取，从不入库。在 env 文件里修改，重启生效"},
	{Key: KeyEnvFile, Group: "service", Label: "环境文件路径", Type: TypeString, Default: ".env", Restart: true},

	// --- system update: intent only. The API never runs an upgrade;
	// saving here syncs the two values to the panel state file in the
	// data dir, and the root-side upgrade agent (a per-minute systemd
	// timer installed by install.sh) applies them: it arms/disarms the
	// auto-update timer with this time and runs panel-requested
	// upgrades. Restart=false because the server itself needs no
	// restart — the script side picks the change up within ~1 minute.
	// Until an admin saves these keys at least once, the script keeps
	// following its own CLI state (dhtsearch auto-update). ---
	{Key: KeyAutoUpdateEnabled, Group: "update", Label: "自动升级", Type: TypeBool, Default: "false", Editable: true,
		Desc: "开 = root 侧的 systemd 定时器按下面的时间自动执行 upgrade（带备份、健康检查与失败回滚）。面板只记录开关与时间并同步到数据目录的状态文件，脚本侧的升级代理约 1 分钟内据此启停定时器；从未在面板保存过时以脚本 CLI（dhtsearch auto-update）的状态为准"},
	{Key: KeyAutoUpdateTime, Group: "update", Label: "自动升级时间", Type: TypeString, Default: "03:37", Editable: true, MaxLen: 5,
		Desc: "每天执行自动升级的时间，HH:MM 格式（服务器本地时间），另有最多 30 分钟随机延迟；仅在自动升级开启时有意义"},

	// --- data retention: all live. The runner re-reads these before
	// every pass, so saving applies to the next scheduled run (and the
	// settings page's status card always shows the blast radius under
	// the current values). Default off: deleting a torrent row is
	// irreversible — its files blob goes with it — so the operator
	// opts in deliberately. Only the health derivation's likely_dead
	// tier is ever eligible (zero seeders AND zero leechers across at
	// least 3 consecutive scrapes spanning at least 48 hours), and
	// only past the minimum age; the blocked table is never touched,
	// so a blacklisted hash stays blacklisted even after its row is
	// cleaned. ---
	{Key: KeyRetentionEnabled, Group: "retention", Label: "自动清理死种", Type: TypeBool, Default: "false", Editable: true,
		Desc: "开 = 按下面的周期自动删除「疑似死亡」且入库超过最短保留天数的种子（连同文件列表、刮削快照与抓取退避记录；黑名单不受影响，拉黑过的哈希不会因为清理而解禁）。删除不可恢复，默认关。保存后立即生效"},
	{Key: KeyRetentionMinAgeDays, Group: "retention", Label: "最短保留天数", Type: TypeInt, Default: "30", Editable: true,
		Min: f64(1), Max: f64(3650), Desc: "入库未满该天数的种子永不清理，哪怕健康度已是疑似死亡。保存后立即生效"},
	{Key: KeyRetentionIntervalH, Group: "retention", Label: "清理周期（小时）", Type: TypeInt, Default: "24", Editable: true,
		Min: f64(1), Max: f64(720), Desc: "自动清理的执行间隔；服务启动后不久先跑一轮。保存后立即生效"},

	// --- stall alerts: delivery only — detection stays with the
	// watchdog (crawler group), whose episode transitions this
	// group forwards. The channel knobs and the master switch are
	// live: the notifier re-reads them at every event, so pasting a
	// webhook and flipping the switch starts alerting on the next
	// episode, and the admin API's test endpoint can verify a
	// configuration before the switch is ever turned on. The stall
	// threshold is the exception: it is the watchdog's WarnAfter,
	// assembled once at startup (the exit threshold is twice it),
	// so it carries Restart=true like the other structural knobs.
	// Default off: a fresh deployment has no channel configured,
	// and alerting at nobody is worse than a clear opt-in. ---
	{Key: KeyAlertEnabled, Group: "alerts", Label: "启用停滞告警", Type: TypeBool, Default: "false", Editable: true,
		Desc: "开 = 看门狗判定入库停滞（及爬行、冻结）时经下面配置的通道外发告警，恢复时再发一条；同一轮停滞只发一次。关 = 看门狗照常打栈、自愈，只是不外发。保存后立即生效"},
	{Key: KeyAlertWebhookURL, Group: "alerts", Label: "Webhook 地址", Type: TypeSecret, Sensitive: true, Default: "", Editable: true, MaxLen: 1000,
		Desc: "通用 Webhook：停滞/恢复时 POST JSON {title, text, ts}（ts 为 Unix 秒）。钉钉/飞书/企业微信/Slack 等群机器人或自建接收端地址填这里；地址常含密钥，故按密钥处理：只显示是否已设置，保存时留空表示不改"},
	{Key: KeyAlertTelegramBotToken, Group: "alerts", Label: "Telegram 机器人 Token", Type: TypeSecret, Sensitive: true, Default: "", Editable: true,
		Desc: "@BotFather 创建机器人得到的 token；与下面的会话 ID 都配好后 Telegram 通道生效。只显示是否已设置，保存时留空表示不改"},
	{Key: KeyAlertTelegramChatID, Group: "alerts", Label: "Telegram 会话 ID", Type: TypeString, Default: "", Editable: true, MaxLen: 64,
		Desc: "接收告警的会话 ID：个人会话为正数 ID，群为负数；先给机器人发一句话，再用 getUpdates 查到"},
	{Key: KeyAlertStallMinutes, Group: "alerts", Label: "停滞判定（分钟）", Type: TypeInt, Default: "15", Editable: true, Restart: true,
		Min: f64(5), Max: f64(120), Desc: "入库零增长持续该分钟数即判定停滞：看门狗打全量协程栈，告警开启时同时外发停滞告警；退出自愈阈值为其两倍（默认 15 → 30 分钟）。该值在启动时装配给看门狗，重启生效"},

	// --- auth: admin login methods. All live: the login page asks the
	// backend which methods are configured and renders only those. ---
	{Key: KeyGitHubClientID, Group: "auth", Label: "GitHub Client ID", Type: TypeString, Default: "", Editable: true, MaxLen: 200,
		Desc: "GitHub OAuth App 的 Client ID；与密钥、放行名单都配好后，登录页出现 GitHub 登录按钮"},
	{Key: KeyGitHubClientSecret, Group: "auth", Label: "GitHub Client Secret", Type: TypeSecret, Sensitive: true, Default: "", Editable: true,
		Desc: "只显示是否已设置，保存时留空表示不改"},
	{Key: KeyGitHubAllowedUsers, Group: "auth", Label: "GitHub 放行用户名", Type: TypeList, Default: "", Editable: true,
		MaxItems: 200, MaxLen: 100, Desc: "只有名单内的 GitHub 用户名（不区分大小写）能登录；名单外的人授权成功也不会发会话"},
	{Key: KeyOIDCIssuer, Group: "auth", Label: "SSO Issuer 地址", Type: TypeString, Default: "", Editable: true, MaxLen: 300,
		Desc: "OIDC 提供商的 Issuer，如 https://accounts.google.com；与 Client ID、密钥都配好后登录页出现 SSO 按钮（仅支持 OIDC，不支持 SAML）"},
	{Key: KeyOIDCClientID, Group: "auth", Label: "SSO Client ID", Type: TypeString, Default: "", Editable: true, MaxLen: 200},
	{Key: KeyOIDCClientSecret, Group: "auth", Label: "SSO Client Secret", Type: TypeSecret, Sensitive: true, Default: "", Editable: true,
		Desc: "只显示是否已设置，保存时留空表示不改"},
	{Key: KeyOIDCProviderName, Group: "auth", Label: "SSO 按钮名称", Type: TypeString, Default: "SSO", Editable: true, MaxLen: 30,
		Desc: "登录页 SSO 按钮上显示的名字，如公司 IdP 的名称"},
	{Key: KeyOIDCAllowedEmails, Group: "auth", Label: "SSO 放行邮箱", Type: TypeList, Default: "", Editable: true,
		MaxItems: 500, MaxLen: 200, Desc: "允许通过 SSO 登录的邮箱（不区分大小写）"},
	{Key: KeyOIDCAllowedDomain, Group: "auth", Label: "SSO 放行邮箱域名", Type: TypeString, Default: "", Editable: true, MaxLen: 100,
		Desc: "如 example.com，该域名下的邮箱都可通过 SSO 登录；与放行邮箱至少填一个，两者都空时 SSO 登录一律拒绝"},

	// --- appearance: live, consumed by the web app via /api/site ---
	{Key: KeySiteName, Group: "appearance", Label: "站点名称", Type: TypeString, Default: "DHTSearch", Editable: true, MaxLen: 60,
		Desc: "浏览器标题、页头与分享标题里的站点名"},
	{Key: KeyThemeColor, Group: "appearance", Label: "主色", Type: TypeEnum, Default: "teal", Editable: true,
		Options: ThemeColors, Desc: "全站按钮、链接与徽标的主色预设"},
	{Key: KeyThemeAppearance, Group: "appearance", Label: "默认外观", Type: TypeEnum, Default: "system", Editable: true,
		Options: []string{"system", "light", "dark"}, Desc: "访客未手动切换时的初始外观；访客自己的选择优先"},

	// --- crawler: structural, restart ---
	{Key: KeyCrawlEnabled, Group: "crawler", Label: "启用 DHT 爬虫", Type: TypeBool, Default: "true", Editable: true, Restart: true},
	{Key: KeyDHTPort, Group: "crawler", Label: "DHT 监听端口", Type: TypeInt, Default: "0", Editable: true, Restart: true,
		Min: f64(0), Max: f64(65535), Desc: "0 = 随机端口"},
	{Key: KeyDHTSamplers, Group: "crawler", Label: "采样 worker 数", Type: TypeInt, Default: "8", Editable: true, Restart: true,
		Min: f64(1), Max: f64(256), Desc: "BEP 51 并发采样数（每个 DHT 身份各一份）；实测 8 已远超抓取池消耗速度，调大只会抢带宽"},
	{Key: KeyDHTIdentities, Group: "crawler", Label: "DHT 身份数", Type: TypeInt, Default: "1", Editable: true, Restart: true,
		Min: f64(1), Max: f64(16), Desc: "同一进程内运行的 DHT 节点身份数：每个身份有独立的节点 ID、路由表与节点质量跟踪。单个身份只能采到自身路由表邻域的哈希，身份多了 BEP 51 覆盖的 ID 空间才变宽。身份 i 监听 DHT_PORT+i（DHT_PORT=0 时各自随机端口），请确保 DHT_PORT..DHT_PORT+身份数-1 未被占用；采样 worker 数按身份计，总采样量 = 采样 worker 数 × 身份数。1 = 与旧行为完全一致"},
	{Key: KeyDHTIPv6Enabled, Group: "crawler", Label: "IPv6 DHT（BEP 32）", Type: TypeBool, Default: "true", Editable: true, Restart: true,
		Desc: "开 = 每个 DHT 身份在同一端口号上再起一个 IPv6 DHT 服务（独立的 udp6 socket、路由表与采样队列，与 IPv4 服务共用节点 ID），IPv6 独占的种子群落也能被发现，采到的 IPv6 peer 提示与 IPv4 一样喂给元数据抓取。主机没有可用 IPv6 时自动退化为纯 IPv4 运行（日志打一行说明），不会启动失败；关 = 只跑 IPv4。改动需重启生效"},
	{Key: KeyFetchMetadata, Group: "crawler", Label: "抓取元数据", Type: TypeBool, Default: "true", Editable: true, Restart: true,
		Desc: "关 = 只收集裸 infohash，不抓元数据、不做过滤"},
	{Key: KeyCrawlerWatchdogEnabled, Group: "crawler", Label: "停转看门狗", Type: TypeBool, Default: "true", Editable: true,
		Desc: "发现层在动但 15 分钟零入库时打全量 goroutine 栈告警，30 分钟仍无恢复则退出进程由 systemd 拉起自愈；发现层也冻住（像整机断网）时只告警不退出。保存后立即生效"},

	// --- metadata ---
	{Key: KeyMetaWorkers, Group: "metadata", Label: "元数据 worker 数", Type: TypeInt, Default: "128", Editable: true, Restart: true,
		Min: f64(1), Max: f64(2048), Desc: "入库吞吐的主要旋钮；实测 128 最优，256 反而因争抢下降"},
	{Key: KeyMetaShards, Group: "metadata", Label: "元数据客户端分片数", Type: TypeInt, Default: "8", Editable: true, Restart: true,
		Min: f64(1), Max: f64(64), Desc: "抓取用的 torrent 客户端数量：种子按 infohash 首字节分片到各客户端，把单客户端全局锁的争用摊开（128 worker、8 片时每片约 16 个并发种子，而不是 128 个全挤在一把锁上）；调大摊薄锁争用，调到 1 回到单客户端。仅对 anacrolix 底座有意义，native 底座无客户端、忽略此项"},
	{Key: KeyMetaFetcher, Group: "metadata", Label: "抓取底座", Type: TypeEnum, Default: "anacrolix", Editable: true, Restart: true,
		Options: []string{"anacrolix", "native", "libtorrent"}, Desc: "anacrolix = 基于 torrent 客户端的抓取（默认，现有行为）；native = 自研薄抓取器：只做 tracker announce 取 peer、直连 peer 走 BEP 9 取元数据，没有客户端全局锁，单个卡死 peer 只损失自己的协程；libtorrent = libtorrent-rasterbar 2.x 边车进程（dhtsearch-lt-sidecar，与主程序同目录或见 META_LT_SIDECAR）：成熟引擎的 peer/连接管理，种子以 upload_mode 只取元数据不下分片，DHT 在边车内关闭、peer 来源仍为磁力 tracker + PEX。三个底座的计数口径（成功/超时/失败/早筛丢弃）一致，切换后对比 pipeline 日志即可 A/B。边车未安装时设置页会拒绝切到 libtorrent；若启动时边车不可用（例如文件被删），服务会降级到 anacrolix 继续运行并在管理端显著告警，绝不因底座选择启动失败。订阅源抓取始终走 anacrolix，不受此项影响"},
	{Key: KeyMetaLTSidecar, Group: "metadata", Label: "libtorrent 边车路径", Type: TypeString, Default: "", Editable: true, Restart: true,
		Desc: "dhtsearch-lt-sidecar 二进制路径；留空 = 先找主程序同目录、再找 PATH。仅 META_FETCHER=libtorrent 时使用"},
	{Key: KeyMetaTimeout, Group: "metadata", Label: "元数据超时", Type: TypeDuration, Default: "45s", Editable: true,
		Min: f64(1), Max: f64(600), Desc: "单个种子等元数据的最长时长，保存后立即生效；实测缩短会让成功率塌陷，不要低于 20s"},
	{Key: KeyMetaPrescreenEnabled, Group: "metadata", Label: "元数据早筛", Type: TypeBool, Default: "true", Editable: true,
		Desc: "先在早筛窗口内等任意 peer 连上；一个都没连上的提前放弃，不再占满整个元数据超时。保存后立即生效"},
	{Key: KeyMetaPrescreenTimeout, Group: "metadata", Label: "早筛窗口", Type: TypeDuration, Default: "10s", Editable: true,
		Min: f64(1), Max: f64(300), Desc: "早筛窗口，包含在元数据超时之内（不是额外叠加）；窗口内连上 peer 的种子仍享有完整的元数据超时。保存后立即生效"},
	{Key: KeyMetaPriorityEnabled, Group: "metadata", Label: "抓取优先级队列", Type: TypeBool, Default: "true", Editable: true, Restart: true,
		Desc: "待抓 infohash 先进优先级队列（做种数 > 来源 > 新鲜度），worker 按优先级取；关 = 回到先进先出直连抓取"},
	{Key: KeyMetaNegCacheEnabled, Group: "metadata", Label: "抓取失败退避（负缓存）", Type: TypeBool, Default: "true", Editable: true,
		Desc: "入队前先查：已入库的 infohash 直接跳过、不再重复抓取；抓取失败的按 1 小时起步翻倍的退避期跳过（刚被 tracker 报过做种的、订阅源来的不受退避限制，但已入库的一律跳过）。省下的 worker 槽位留给没试过的哈希。保存后立即生效"},
	{Key: KeyMetaNegCacheMaxBackoffH, Group: "metadata", Label: "失败退避上限（小时）", Type: TypeInt, Default: "24", Editable: true,
		Min: f64(1), Max: f64(168), Desc: "连续失败的退避间隔 1h → 2h → 4h → 8h … 翻倍到此上限封顶；抓取成功一次即清零重来。保存后对新记录的失败立即生效"},
	{Key: KeyMinTorrentSize, Group: "metadata", Label: "最小收录体积（字节）", Type: TypeInt64, Default: "104857600", Editable: true,
		Min: f64(0), Max: f64(1 << 40), Desc: "小于该体积的种子不收录，保存后对新抓取立即生效"},

	// --- scrape ---
	{Key: KeyScrapeEnabled, Group: "scrape", Label: "启用 Tracker 刮削排序", Type: TypeBool, Default: "true", Editable: true, Restart: true,
		Desc: "按 tracker 报告的做种数给待抓队列排序，热门内容优先入库"},
	{Key: KeyTrackers, Group: "scrape", Label: "Tracker 列表", Type: TypeList, Default: "", Editable: true, Restart: true,
		MaxItems: 64, Desc: "逗号分隔的 announce URL；同时用于刮削与磁力链接的 peer 提示"},
	// --- swarm re-scrape: all live. The runner re-reads these
	// before every round. It only ever queries trackers for
	// torrents already in the index — never a metadata fetch — and
	// writes through the same swarm write path as ingest, so the
	// snapshots behind seeder sorting and the health derivation
	// stay fresh and dead torrents keep accumulating the all-zero
	// rounds the retention cleanup needs as evidence. ---
	{Key: KeyRescrapeEnabled, Group: "scrape", Label: "已入库种子定期复查", Type: TypeBool, Default: "true", Editable: true,
		Desc: "开 = 后台每分钟挑快照最陈旧的已入库种子重新向 tracker 刮削一次（从未刮过的最优先），刷新做种/下载数与健康度证据。只查 tracker，不触发元数据抓取；元数据队列满时整轮跳过，不与入库抢资源。保存后立即生效"},
	{Key: KeyRescrapeMinAgeH, Group: "scrape", Label: "复查最小间隔（小时）", Type: TypeInt, Default: "48", Editable: true,
		Min: f64(1), Max: f64(8760), Desc: "快照比该时长新的一律不复查；从未刮过的种子不受此限、永远可入选。保存后立即生效"},
	{Key: KeyRescrapePerMin, Group: "scrape", Label: "复查速率（个/分钟）", Type: TypeInt, Default: "60", Editable: true,
		Min: f64(1), Max: f64(10000), Desc: "每分钟最多复查的种子数，一轮一扣，绝不突发。保存后立即生效"},

	// --- static content filtering: all live (next fetch) ---
	{Key: KeyFilterEnabled, Group: "filter", Label: "启用静态过滤", Type: TypeBool, Default: "true", Editable: true,
		Desc: "总开关：关后成人、垃圾、体积三类判定全部停用，新抓取的种子直接入库（LLM 审核不受影响）"},
	{Key: KeyFilterAdultEnabled, Group: "filter", Label: "成人过滤", Type: TypeBool, Default: "true", Editable: true,
		Desc: "内置词表、成人站域名、横幅词与 JAV 信号判定；关后这类种子不再被丢弃、也不再计数"},
	{Key: KeyFilterSpamEnabled, Group: "filter", Label: "垃圾过滤", Type: TypeBool, Default: "true", Editable: true,
		Desc: "结构性垃圾规则（空名、堆砌、符号占比、随机文件名等）与自定义屏蔽词；关后这类种子不再被丢弃、也不再计数"},
	{Key: KeyFilterTooSmallEnabled, Group: "filter", Label: "体积过滤", Type: TypeBool, Default: "true", Editable: true,
		Desc: "按「最小收录体积」丢弃过小种子（体积值在元数据抓取组）；关后不再丢弃、也不再计数"},
	{Key: KeyFilterMaxNameLen, Group: "filter", Label: "名字最大长度（字符）", Type: TypeInt, Default: "300", Editable: true,
		Min: f64(10), Max: f64(10000), Desc: "垃圾规则：名字超过该长度即丢弃"},
	{Key: KeyFilterStuffingMinRepeat, Group: "filter", Label: "关键词堆砌阈值（次）", Type: TypeInt, Default: "4", Editable: true,
		Min: f64(2), Max: f64(100), Desc: "垃圾规则：同一个词在名字里重复达到该次数即判为关键词堆砌"},
	{Key: KeyFilterNonAlnumMaxRatio, Group: "filter", Label: "符号占比上限", Type: TypeFloat, Default: "0.5", Editable: true,
		Min: f64(0), Max: f64(1), Desc: "垃圾规则：名字里非字母/数字/空格的字符占比超过该值即丢弃，0–1 之间"},
	{Key: KeyFilterRandomFileMaxRatio, Group: "filter", Label: "随机文件名占比上限", Type: TypeFloat, Default: "0.3", Editable: true,
		Min: f64(0), Max: f64(1), Desc: "垃圾规则：随机字符串式文件名占比超过该值即丢弃（文件数不少于 5 个时才判定），0–1 之间"},
	{Key: KeyFilterBlockWords, Group: "filter", Label: "自定义屏蔽词", Type: TypeList, Default: "", Editable: true,
		MaxItems: 500, MaxLen: 100, Desc: "标题或文件名命中任一词即丢弃，单独计数（总览「过滤构成」里的自定义屏蔽），对新抓取立即生效；受垃圾过滤开关控制"},
	{Key: KeyFilterAdultWords, Group: "filter", Label: "自定义成人词", Type: TypeList, Default: "", Editable: true,
		MaxItems: 500, MaxLen: 100, Desc: "追加到内置成人词表之上：标题或文件名命中即按成人内容丢弃，匹配方式与内置词一致（短 ASCII 词按词边界、其余按子串）；受成人过滤开关控制。内置词表是底座，不可删改"},
	{Key: KeyFilterBlockDomains, Group: "filter", Label: "自定义屏蔽域名", Type: TypeList, Default: "", Editable: true,
		MaxItems: 500, MaxLen: 200, Desc: "追加到成人域名判定：标题或文件名出现这些域名（含子域名）即按成人内容丢弃；填裸域名如 example.com，粘贴完整 URL 也会自动归一化；受成人过滤开关控制"},
	{Key: KeyFilterAllowWords, Group: "filter", Label: "例外短语", Type: TypeList, Default: "", Editable: true,
		MaxItems: 500, MaxLen: 100, Desc: "先从标题和文件路径中移除这些短语再做成人判定，正经内容字面含敏感词时用；只豁免成人判定，不影响垃圾规则与自定义屏蔽词"},

	// --- LLM moderation: all live (next sweep). Edited on the
	// dedicated /admin/llm page; the settings page links there. ---
	{Key: KeyModerationEnabled, Group: "moderation", Label: "启用 LLM 审核", Type: TypeBool, Default: "true", Editable: true,
		Desc: "周期性用 LLM 清理静态过滤漏过的成人/垃圾内容；需要先填 API 密钥"},
	{Key: KeyOpenAIAPIKey, Group: "moderation", Label: "LLM API 密钥", Type: TypeSecret, Sensitive: true, Default: "", Editable: true,
		Desc: "OpenAI 兼容接口的密钥；只显示是否已设置，保存时留空表示不改"},
	{Key: KeyOpenAIBaseURL, Group: "moderation", Label: "LLM 接口地址", Type: TypeString, Default: "https://openrouter.ai/api/v1", Editable: true, MaxLen: 300},
	{Key: KeyOpenAIModel, Group: "moderation", Label: "LLM 模型", Type: TypeString, Default: "nvidia/nemotron-3-super-120b-a12b:free", Editable: true, MaxLen: 200},
	{Key: KeyModerationInterval, Group: "moderation", Label: "审核周期", Type: TypeDuration, Default: "1h0m0s", Editable: true,
		Min: f64(60), Max: f64(7 * 24 * 3600)},
	{Key: KeyModerationTimeout, Group: "moderation", Label: "审核请求超时", Type: TypeDuration, Default: "2m0s", Editable: true,
		Min: f64(1), Max: f64(600)},
	{Key: KeyModerationBatchSize, Group: "moderation", Label: "每批标题数", Type: TypeInt, Default: "100", Editable: true,
		Min: f64(1), Max: f64(1000)},
	{Key: KeyModerationMaxBatches, Group: "moderation", Label: "每轮最多批数", Type: TypeInt, Default: "20", Editable: true,
		Min: f64(0), Max: f64(100000), Desc: "0 = 不限"},
	{Key: KeyModerationDryRun, Group: "moderation", Label: "审核试运行", Type: TypeBool, Default: "false", Editable: true,
		Desc: "开 = 只记录将删除什么，不真删"},
	{Key: KeyModerationTrimTitles, Group: "moderation", Label: "标题去广告", Type: TypeBool, Default: "true", Editable: true,
		Desc: "审核同一轮里顺带生成去广告的展示标题（原始标题保留且仍可搜）"},

	// --- trending: live ---
	{Key: KeyTrendingEnabled, Group: "trending", Label: "启用豆瓣趋势", Type: TypeBool, Default: "true", Editable: true,
		Desc: "关后首页不再显示热门影视快捷搜索"},
	{Key: KeyTrendingInterval, Group: "trending", Label: "趋势刷新周期", Type: TypeDuration, Default: "1h0m0s", Editable: true,
		Min: f64(60), Max: f64(7 * 24 * 3600),
		Desc: "向豆瓣刷新四个榜单的间隔，时长格式（如 30m、1h、6h），范围 1 分钟～7 天；保存后下一轮起生效，抓取失败会自动退避重试，也可在概览页手动立即刷新"},
	{Key: KeyTrendingLimit, Group: "trending", Label: "每榜保留条数", Type: TypeInt, Default: "10", Editable: true,
		Min: f64(1), Max: f64(50),
		Desc: "每个榜单（电影 / 美剧 / 日剧 / 韩剧）保留的标题数，范围 1–50 条；保存后下一轮刷新起生效"},

	// --- feeds ---
	{Key: KeyFeedsEnabled, Group: "feeds", Label: "启用 RSS 订阅源", Type: TypeBool, Default: "true", Editable: true},
	{Key: KeyFeeds, Group: "feeds", Label: "RSS 源列表", Type: TypeList, Default: "", Editable: true,
		MaxItems: 32, Desc: "逗号分隔的 RSS 地址，保存后下一轮轮询生效"},
	{Key: KeyFeedsKeywordURL, Group: "feeds", Label: "关键词搜索 URL", Type: TypeString, Default: "", Editable: true, MaxLen: 500,
		Desc: "带一个 %s 的 RSS 搜索地址，用趋势里的日剧/韩剧标题去搜更早的发布；空 = 关"},
	{Key: KeyFeedsInterval, Group: "feeds", Label: "轮询周期", Type: TypeDuration, Default: "30m0s", Editable: true,
		Min: f64(60), Max: f64(7 * 24 * 3600)},
	{Key: KeyFeedsWorkers, Group: "feeds", Label: "订阅源抓取 worker 数", Type: TypeInt, Default: "16", Editable: true, Restart: true,
		Min: f64(1), Max: f64(256)},
	{Key: KeyFeedsMetaTimeout, Group: "feeds", Label: "订阅源元数据超时", Type: TypeDuration, Default: "1m30s", Editable: true,
		Min: f64(1), Max: f64(600)},

	// --- rate limiting & search: live ---
	{Key: KeyRateLimitRPS, Group: "ratelimit", Label: "每 IP 请求速率（/秒）", Type: TypeFloat, Default: "3", Editable: true,
		Min: f64(0), Max: f64(100000), Desc: "令牌桶持续速率；0 = 关闭限流，正数最低 0.1。只限公开接口，管理端不受此限。保存后立即生效"},
	{Key: KeyRateLimitBurst, Group: "ratelimit", Label: "每 IP 突发上限", Type: TypeInt, Default: "30", Editable: true,
		Min: f64(0), Max: f64(1000000), Desc: "令牌桶容量；0 = 按速率自动取 10 秒量，正数最低 10"},
	{Key: KeySearchMaxInflight, Group: "ratelimit", Label: "搜索最大并发", Type: TypeInt, Default: "16", Editable: true,
		Min: f64(1), Max: f64(4096), Desc: "超出的搜索请求排队 1 秒后返回 503"},
	{Key: KeySearchTimeout, Group: "ratelimit", Label: "搜索数据库超时", Type: TypeDuration, Default: "10s", Editable: true,
		Min: f64(0.1), Max: f64(120)},

	// --- public API: live; the token gate reads these per request ---
	{Key: KeyPublicAPIEnabled, Group: "publicapi", Label: "启用公开 API", Type: TypeBool, Default: "true", Editable: true,
		Desc: "关 = /api/search、/api/stats、/api/trending 全部返回 503（前台搜索也会停）；/api/healthz 与管理端不受影响"},
	{Key: KeyPublicAPIRequireToken, Group: "publicapi", Label: "公开 API 要求令牌", Type: TypeBool, Default: "false", Editable: true,
		Desc: "开 = 外部调用必须带 API 令牌（Authorization: Bearer 或 X-API-Key），无/错令牌 401、被禁用 403。站点前台走内部第一方通道（INTERNAL_API_TOKEN，install.sh 在 API 与 web 两侧自动生成一致的值）不受影响；内部令牌未配置时前台也会被一并拦截，保存时会有警告。关 = 保持无鉴权，但带有效令牌的请求会被计数"},
	{Key: KeyPublicAPITokenRatePerMin, Group: "publicapi", Label: "新令牌默认限速（次/分钟）", Type: TypeInt, Default: "600", Editable: true,
		Min: f64(0), Max: f64(1000000), Desc: "创建令牌时的默认每分钟配额，之后可在「API 令牌」页逐个修改；0 = 不限"},
}

// byKey indexes Registry for lookups.
var byKey = func() map[string]Item {
	m := make(map[string]Item, len(Registry))
	for _, it := range Registry {
		m[it.Key] = it
	}
	return m
}()

// ByKey returns the registry entry for key.
func ByKey(key string) (Item, bool) {
	it, ok := byKey[key]
	return it, ok
}
