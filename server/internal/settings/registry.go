package settings

// Setting keys. They deliberately reuse the environment variable
// names the server has always read, so the env layer, the env file
// and the settings page all talk about the same knob by the same
// name. FILTER_BLOCK_WORDS and the appearance keys are new with the
// settings system (env support included, for symmetry).
const (
	// service (startup, read-only)
	KeyHTTPAddr     = "HTTP_ADDR"
	KeyDBPath       = "DB_PATH"
	KeyDatabaseURL  = "DATABASE_URL"
	KeyAdminToken   = "ADMIN_TOKEN"
	KeyEnvFile      = "ENV_FILE"
	// appearance
	KeySiteName       = "SITE_NAME"
	KeyThemeColor     = "THEME_COLOR"
	KeyThemeAppearance = "THEME_APPEARANCE"
	// crawler
	KeyCrawlEnabled  = "CRAWL_ENABLED"
	KeyDHTPort       = "DHT_PORT"
	KeyDHTSamplers   = "DHT_SAMPLERS"
	KeyFetchMetadata = "FETCH_METADATA"
	// metadata
	KeyMetaWorkers   = "META_WORKERS"
	KeyMetaTimeout   = "META_TIMEOUT"
	KeyMinTorrentSize = "MIN_TORRENT_SIZE"
	// scrape
	KeyScrapeEnabled = "SCRAPE_ENABLED"
	KeyTrackers      = "TRACKERS"
	// moderation & filtering
	KeyModerationEnabled   = "MODERATION_ENABLED"
	KeyOpenAIAPIKey        = "OPENAI_API_KEY"
	KeyOpenAIBaseURL       = "OPENAI_BASE_URL"
	KeyOpenAIModel         = "OPENAI_MODEL"
	KeyModerationInterval  = "MODERATION_INTERVAL"
	KeyModerationTimeout   = "MODERATION_TIMEOUT"
	KeyModerationBatchSize = "MODERATION_BATCH_SIZE"
	KeyModerationMaxBatches = "MODERATION_MAX_BATCHES"
	KeyModerationDryRun    = "MODERATION_DRY_RUN"
	KeyModerationTrimTitles = "MODERATION_TRIM_TITLES"
	KeyFilterBlockWords    = "FILTER_BLOCK_WORDS"
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
	KeyRateLimitRPS     = "RATE_LIMIT_RPS"
	KeyRateLimitBurst   = "RATE_LIMIT_BURST"
	KeySearchMaxInflight = "SEARCH_MAX_INFLIGHT"
	KeySearchTimeout    = "SEARCH_TIMEOUT"
)

// Groups in settings-page display order.
var Groups = []Group{
	{Key: "service", Label: "服务与数据库"},
	{Key: "appearance", Label: "外观与主题"},
	{Key: "crawler", Label: "爬虫"},
	{Key: "metadata", Label: "元数据抓取"},
	{Key: "scrape", Label: "刮削排序"},
	{Key: "moderation", Label: "审核与过滤"},
	{Key: "trending", Label: "趋势"},
	{Key: "feeds", Label: "订阅源"},
	{Key: "ratelimit", Label: "限流与搜索"},
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
	// --- service: startup-only, shown read-only ---
	{Key: KeyHTTPAddr, Group: "service", Label: "HTTP 监听地址", Type: TypeString, Default: ":8080",
		Desc: "在 /etc/dhtsearch/dhtsearch.env 里修改，重启生效"},
	{Key: KeyDBPath, Group: "service", Label: "SQLite 数据库路径", Type: TypeString, Default: "./dhtsearch.db",
		Desc: "配置了 DATABASE_URL 时忽略。在 env 文件里修改，重启生效"},
	{Key: KeyDatabaseURL, Group: "service", Label: "Postgres 连接串", Type: TypeSecret, Sensitive: true, Default: "",
		Desc: "设置后使用 Postgres 代替 SQLite。在 env 文件里修改，重启生效"},
	{Key: KeyAdminToken, Group: "service", Label: "管理端令牌", Type: TypeSecret, Sensitive: true, Default: "",
		Desc: "只从环境读取，从不入库。在 env 文件里修改，重启生效"},
	{Key: KeyEnvFile, Group: "service", Label: "环境文件路径", Type: TypeString, Default: ".env"},

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
		Min: f64(1), Max: f64(256), Desc: "BEP 51 并发采样数；实测 8 已远超抓取池消耗速度，调大只会抢带宽"},
	{Key: KeyFetchMetadata, Group: "crawler", Label: "抓取元数据", Type: TypeBool, Default: "true", Editable: true, Restart: true,
		Desc: "关 = 只收集裸 infohash，不抓元数据、不做过滤"},

	// --- metadata ---
	{Key: KeyMetaWorkers, Group: "metadata", Label: "元数据 worker 数", Type: TypeInt, Default: "128", Editable: true, Restart: true,
		Min: f64(1), Max: f64(2048), Desc: "入库吞吐的主要旋钮；实测 128 最优，256 反而因争抢下降"},
	{Key: KeyMetaTimeout, Group: "metadata", Label: "元数据超时", Type: TypeDuration, Default: "45s", Editable: true,
		Min: f64(1), Max: f64(600), Desc: "单个种子等元数据的最长时长，保存后立即生效；实测缩短会让成功率塌陷，不要低于 20s"},
	{Key: KeyMinTorrentSize, Group: "metadata", Label: "最小收录体积（字节）", Type: TypeInt64, Default: "104857600", Editable: true,
		Min: f64(0), Max: f64(1 << 40), Desc: "小于该体积的种子不收录，保存后对新抓取立即生效"},

	// --- scrape ---
	{Key: KeyScrapeEnabled, Group: "scrape", Label: "启用 Tracker 刮削排序", Type: TypeBool, Default: "true", Editable: true, Restart: true,
		Desc: "按 tracker 报告的做种数给待抓队列排序，热门内容优先入库"},
	{Key: KeyTrackers, Group: "scrape", Label: "Tracker 列表", Type: TypeList, Default: "", Editable: true, Restart: true,
		MaxItems: 64, Desc: "逗号分隔的 announce URL；同时用于刮削与磁力链接的 peer 提示"},

	// --- moderation & filtering: all live (next sweep / next fetch) ---
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
	{Key: KeyFilterBlockWords, Group: "moderation", Label: "自定义屏蔽词", Type: TypeList, Default: "", Editable: true,
		MaxItems: 500, MaxLen: 100, Desc: "标题或文件名命中任一词即按垃圾内容丢弃，对新抓取立即生效"},

	// --- trending: live ---
	{Key: KeyTrendingEnabled, Group: "trending", Label: "启用豆瓣趋势", Type: TypeBool, Default: "true", Editable: true,
		Desc: "关后首页不再显示热门影视快捷搜索"},
	{Key: KeyTrendingInterval, Group: "trending", Label: "趋势刷新周期", Type: TypeDuration, Default: "1h0m0s", Editable: true,
		Min: f64(60), Max: f64(7 * 24 * 3600)},
	{Key: KeyTrendingLimit, Group: "trending", Label: "每榜保留条数", Type: TypeInt, Default: "10", Editable: true,
		Min: f64(1), Max: f64(50)},

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
		Min: f64(0), Max: f64(100000), Desc: "令牌桶持续速率；0 = 关闭限流。保存后立即生效"},
	{Key: KeyRateLimitBurst, Group: "ratelimit", Label: "每 IP 突发上限", Type: TypeInt, Default: "30", Editable: true,
		Min: f64(0), Max: f64(1000000), Desc: "令牌桶容量；0 = 按速率自动取 10 秒量"},
	{Key: KeySearchMaxInflight, Group: "ratelimit", Label: "搜索最大并发", Type: TypeInt, Default: "16", Editable: true,
		Min: f64(1), Max: f64(4096), Desc: "超出的搜索请求排队 1 秒后返回 503"},
	{Key: KeySearchTimeout, Group: "ratelimit", Label: "搜索数据库超时", Type: TypeDuration, Default: "10s", Editable: true,
		Min: f64(0.1), Max: f64(120)},
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
