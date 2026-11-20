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
	// appearance
	KeySiteName        = "SITE_NAME"
	KeyThemeColor      = "THEME_COLOR"
	KeyThemeAppearance = "THEME_APPEARANCE"
	// crawler
	KeyCrawlEnabled  = "CRAWL_ENABLED"
	KeyDHTPort       = "DHT_PORT"
	KeyDHTSamplers   = "DHT_SAMPLERS"
	KeyFetchMetadata = "FETCH_METADATA"
	// metadata
	KeyMetaWorkers          = "META_WORKERS"
	KeyMetaTimeout          = "META_TIMEOUT"
	KeyMetaPrescreenEnabled = "META_PRESCREEN_ENABLED"
	KeyMetaPrescreenTimeout = "META_PRESCREEN_TIMEOUT"
	KeyMetaPriorityEnabled  = "META_PRIORITY_ENABLED"
	KeyMinTorrentSize       = "MIN_TORRENT_SIZE"
	// scrape
	KeyScrapeEnabled = "SCRAPE_ENABLED"
	KeyTrackers      = "TRACKERS"
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
		Min: f64(1), Max: f64(256), Desc: "BEP 51 并发采样数；实测 8 已远超抓取池消耗速度，调大只会抢带宽"},
	{Key: KeyFetchMetadata, Group: "crawler", Label: "抓取元数据", Type: TypeBool, Default: "true", Editable: true, Restart: true,
		Desc: "关 = 只收集裸 infohash，不抓元数据、不做过滤"},

	// --- metadata ---
	{Key: KeyMetaWorkers, Group: "metadata", Label: "元数据 worker 数", Type: TypeInt, Default: "128", Editable: true, Restart: true,
		Min: f64(1), Max: f64(2048), Desc: "入库吞吐的主要旋钮；实测 128 最优，256 反而因争抢下降"},
	{Key: KeyMetaTimeout, Group: "metadata", Label: "元数据超时", Type: TypeDuration, Default: "45s", Editable: true,
		Min: f64(1), Max: f64(600), Desc: "单个种子等元数据的最长时长，保存后立即生效；实测缩短会让成功率塌陷，不要低于 20s"},
	{Key: KeyMetaPrescreenEnabled, Group: "metadata", Label: "元数据早筛", Type: TypeBool, Default: "true", Editable: true,
		Desc: "先在早筛窗口内等任意 peer 连上；一个都没连上的提前放弃，不再占满整个元数据超时。保存后立即生效"},
	{Key: KeyMetaPrescreenTimeout, Group: "metadata", Label: "早筛窗口", Type: TypeDuration, Default: "10s", Editable: true,
		Min: f64(1), Max: f64(300), Desc: "早筛窗口，包含在元数据超时之内（不是额外叠加）；窗口内连上 peer 的种子仍享有完整的元数据超时。保存后立即生效"},
	{Key: KeyMetaPriorityEnabled, Group: "metadata", Label: "抓取优先级队列", Type: TypeBool, Default: "true", Editable: true, Restart: true,
		Desc: "待抓 infohash 先进优先级队列（做种数 > 来源 > 新鲜度），worker 按优先级取；关 = 回到先进先出直连抓取"},
	{Key: KeyMinTorrentSize, Group: "metadata", Label: "最小收录体积（字节）", Type: TypeInt64, Default: "104857600", Editable: true,
		Min: f64(0), Max: f64(1 << 40), Desc: "小于该体积的种子不收录，保存后对新抓取立即生效"},

	// --- scrape ---
	{Key: KeyScrapeEnabled, Group: "scrape", Label: "启用 Tracker 刮削排序", Type: TypeBool, Default: "true", Editable: true, Restart: true,
		Desc: "按 tracker 报告的做种数给待抓队列排序，热门内容优先入库"},
	{Key: KeyTrackers, Group: "scrape", Label: "Tracker 列表", Type: TypeList, Default: "", Editable: true, Restart: true,
		MaxItems: 64, Desc: "逗号分隔的 announce URL；同时用于刮削与磁力链接的 peer 提示"},

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
		MaxItems: 500, MaxLen: 100, Desc: "标题或文件名命中任一词即按垃圾内容丢弃，对新抓取立即生效；受垃圾过滤开关控制"},
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
