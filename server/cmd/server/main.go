// Command server runs the DHT search engine: DHT crawler, metadata workers
// and the HTTP search API.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"dhtsearch/server/internal/api"
	"dhtsearch/server/internal/crawler"
	"dhtsearch/server/internal/envfile"
	"dhtsearch/server/internal/feeds"
	"dhtsearch/server/internal/filter"
	"dhtsearch/server/internal/metadata"
	"dhtsearch/server/internal/moderator"
	"dhtsearch/server/internal/scraper"
	"dhtsearch/server/internal/settings"
	"dhtsearch/server/internal/store"
	"dhtsearch/server/internal/trending"
	"dhtsearch/server/internal/watchdog"
)

// defaultTrackers are large, long-lived open trackers. They serve both
// pipeline roles (scrape ranking and magnet peer hints); the list is
// overridable via TRACKERS for when one of them inevitably goes away.
// The second udp block are the open trackers US/KR/JP TV (美剧/韩剧/日剧)
// releases (EZTV and friends) announce to — scrapeable, so they also widen
// seeder-ranking coverage for those swarms. The http:// entries are the ACG
// trackers Chinese fansub groups (字幕组) announce to — Nyaa's and AcgnX's,
// carried by dmhy/Mikan/bangumi.moe releases. Only udp:// entries are
// scrapeable, so http ones contribute peer hints only, but for fansub
// swarms that hint is what makes metadata fetch land inside META_TIMEOUT.
// The last block is what 日剧字幕组 releases carry: 动漫花园's own tracker
// (104.143.10.61, stamped on every torrent in its 日剧 section) and the
// open trackers Nyaa live-action uploads list (udp, scrapeable), plus two
// http peer-hint-only ones from the same announce lists.
// All entries probed alive 2026-08 (last block 2026-10) from both deploy
// vantage points; notable dead ones checked: t.acg.rip, tr.bangumi.moe,
// open.acgnxtracker.com, explodie.org (local), tracker.ololosh.space,
// 104.238.198.186 (dmhy's old tracker), open.acgtracker.com,
// tracker.ktxp.com, tracker{2,3,4}.itzmx.com (VPS), tracker.bittor.pw
// (announces but times out on scrape).
const defaultTrackers = "udp://tracker.opentrackr.org:1337/announce," +
	"udp://open.demonii.com:1337/announce," +
	"udp://tracker.torrent.eu.org:451/announce," +
	"udp://exodus.desync.com:6969/announce," +
	"udp://open.stealth.si:80/announce," +
	"udp://tracker.dler.org:6969/announce," +
	"udp://tracker.qu.ax:6969/announce," +
	"udp://tracker-udp.gbitt.info:80/announce," +
	"http://nyaa.tracker.wf:7777/announce," +
	"http://t.nyaatracker.com/announce," +
	"http://opentracker.acgnx.se/announce," +
	"udp://104.143.10.61:8000/announce," +
	"udp://t.overflow.biz:6969/announce," +
	"udp://tracker.skynetcloud.site:6969/announce," +
	"http://tracker1.itzmx.com:8080/announce," +
	"http://tracker.renfei.net:8080/announce"

// defaultFeeds are the publisher RSS feeds polled for infohashes and
// titles: dmhy's 日剧 section (500 items, titles carry the Chinese and
// Japanese names) and Nyaa's live-action category.
const defaultFeeds = "https://share.dmhy.org/topics/rss/sort_id/6/rss.xml," +
	"https://nyaa.si/?page=rss&c=4_0"

// defaultFeedKeywordURL searches dmhy by keyword; the trending 日剧/韩剧 chip
// titles are looked up through it so a chip finds releases older than the
// fixed feeds reach back.
const defaultFeedKeywordURL = "https://share.dmhy.org/topics/rss/rss.xml?keyword=%s"

// envDefault returns the env value or fallback.
func envDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func envInt64(key string, fallback int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return fallback
}

// splitCSV splits a comma-separated list, trimming whitespace and dropping
// empty entries.
func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// scraperStatus adapts the scraper's stats for the API; nil scraper (scrape
// disabled, crawler off, or bare-infohash mode) means no stats section.
func scraperStatus(scr *scraper.Scraper) func() api.ScraperStatus {
	if scr == nil {
		return nil
	}
	return func() api.ScraperStatus {
		ss := scr.Stats()
		return api.ScraperStatus{
			Queue:      ss.QueueLen,
			Scraped:    ss.Scraped,
			Seeded:     ss.Seeded,
			ScrapeErrs: ss.ScrapeErrs,
			Dropped:    ss.Dropped,
			Evicted:    ss.Evicted,
		}
	}
}

func feedsStatus(fs *feeds.Service, f *metadata.Fetcher) func() feeds.Stats {
	if fs == nil {
		return nil
	}
	return func() feeds.Stats {
		st := fs.Stats()
		if f != nil {
			st.Fetched, st.TimedOut, _ = f.Stats()
		}
		return st
	}
}

// feedAlias is the feed title for a fetched torrent, "" for DHT-only ones.
func feedAlias(fs *feeds.Service, infoHash string) string {
	if fs == nil {
		return ""
	}
	return fs.Alias(infoHash)
}

func envFloat(key string, fallback float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return fallback
}

func main() {
	// Subcommands run before any env loading or server flag parsing:
	// `dhtsearch-server migrate --from ... --to ...` copies the
	// database between backends and exits (see migrate.go). With no
	// subcommand the process is the server, exactly as before.
	if migrateRequested() {
		os.Exit(runMigrate(os.Args[2:]))
	}

	logger := log.Default()

	// Load .env before reading any env-backed default. Real environment
	// variables take precedence over the file.
	envPath := envDefault("ENV_FILE", ".env")
	if err := envfile.Load(envPath); err != nil {
		logger.Printf("env: %s: %v", envPath, err)
	}

	addr := flag.String("addr", envDefault("HTTP_ADDR", ":8080"), "HTTP listen address")
	dbPath := flag.String("db", envDefault("DB_PATH", "./dhtsearch.db"), "SQLite database path (ignored when DATABASE_URL is set)")
	databaseURL := flag.String("database-url", envDefault("DATABASE_URL", ""),
		"database URL: postgres:// or postgresql:// selects Postgres; empty uses the SQLite file at DB_PATH")
	crawlEnabled := flag.Bool("crawl", envBool("CRAWL_ENABLED", true), "enable the DHT crawler")
	dhtPort := flag.Int("dht-port", envInt("DHT_PORT", 0), "DHT UDP listen port (0 = random)")
	samplers := flag.Int("dht-samplers", envInt("DHT_SAMPLERS", 8), "concurrent BEP 51 sampling workers")
	metaWorkers := flag.Int("meta-workers", envInt("META_WORKERS", 128), "metadata fetch worker count")
	metaTimeout := flag.Duration("meta-timeout", envDuration("META_TIMEOUT", 45*time.Second), "metadata fetch timeout per torrent")
	metaPrescreen := flag.Bool("meta-prescreen", envBool("META_PRESCREEN_ENABLED", true),
		"prescreen fetches: abandon a hash early when no peer connects within the prescreen window")
	metaPrescreenTimeout := flag.Duration("meta-prescreen-timeout", envDuration("META_PRESCREEN_TIMEOUT", 10*time.Second),
		"prescreen window per fetch (inside the metadata timeout budget, not added to it)")
	metaPriority := flag.Bool("meta-priority", envBool("META_PRIORITY_ENABLED", true),
		"feed metadata workers from a priority queue (seeders > source > freshness) instead of a FIFO channel")
	fetchMetadata := flag.Bool("fetch-metadata", envBool("FETCH_METADATA", true), "fetch torrent metadata (false: store bare infohashes)")
	trackersCSV := flag.String("trackers", envDefault("TRACKERS", defaultTrackers),
		"comma-separated tracker announce URLs, used both to rank discovered infohashes (BEP 15 scrape) and as peer hints on fetch magnets (empty = neither)")
	scrapeEnabled := flag.Bool("scrape", envBool("SCRAPE_ENABLED", true),
		"rank discovered infohashes by tracker-scraped seeder count before fetching")
	feedsEnabled := flag.Bool("feeds", envBool("FEEDS_ENABLED", true),
		"poll publisher RSS feeds: fetch their infohashes ahead of DHT discoveries and store their titles as search aliases")
	feedsCSV := flag.String("feeds-urls", envDefault("FEEDS", defaultFeeds),
		"comma-separated RSS feed URLs")
	feedKeywordURL := flag.String("feeds-keyword-url", envDefault("FEEDS_KEYWORD_URL", defaultFeedKeywordURL),
		"RSS search URL with one %s, queried with each trending 日剧/韩剧 title (empty = off)")
	feedsInterval := flag.Duration("feeds-interval", envDuration("FEEDS_INTERVAL", 30*time.Minute),
		"how often to poll the RSS feeds")
	feedsWorkers := flag.Int("feeds-workers", envInt("FEEDS_WORKERS", 16),
		"metadata fetch workers dedicated to feed infohashes")
	feedsTimeout := flag.Duration("feeds-meta-timeout", envDuration("FEEDS_META_TIMEOUT", 90*time.Second),
		"metadata fetch timeout per feed torrent")
	seedDemo := flag.Bool("seed-demo", false, "insert demo records at startup")
	adminToken := flag.String("admin-token", envDefault("ADMIN_TOKEN", ""),
		"bearer token for the /api/admin/* console API (empty = admin API disabled; the token is read from the environment only and never logged)")
	internalToken := flag.String("internal-token", envDefault("INTERNAL_API_TOKEN", ""),
		"first-party token the bundled web frontend presents (X-Internal-Token) so PUBLIC_API_REQUIRE_TOKEN does not lock the site itself out (empty = channel disabled; environment only, never logged)")
	minSize := flag.Int64("min-size", envInt64("MIN_TORRENT_SIZE", 100<<20),
		"skip torrents whose total size is below this many bytes")

	// API DoS defenses. See internal/api.Options for what each knob bounds.
	rateRPS := flag.Float64("rate-rps", envFloat("RATE_LIMIT_RPS", 3),
		"sustained API requests per second per client IP (0 = disable rate limiting)")
	rateBurst := flag.Int("rate-burst", envInt("RATE_LIMIT_BURST", 30),
		"rate limit burst size per client IP")
	searchInflight := flag.Int("search-max-inflight", envInt("SEARCH_MAX_INFLIGHT", 16),
		"max concurrent search queries before shedding load")
	searchTimeout := flag.Duration("search-timeout", envDuration("SEARCH_TIMEOUT", 10*time.Second),
		"database time budget for one search request")

	// Douban trending quick-search chips.
	trendEnabled := flag.Bool("trending", envBool("TRENDING_ENABLED", true),
		"periodically fetch Douban hot movie/TV titles for /api/trending")
	trendInterval := flag.Duration("trending-interval", envDuration("TRENDING_INTERVAL", time.Hour),
		"how often to refresh the Douban trending lists")
	trendLimit := flag.Int("trending-limit", envInt("TRENDING_LIMIT", 10),
		"titles to keep per trending list")

	// LLM moderation pass.
	modEnabled := flag.Bool("moderate", envBool("MODERATION_ENABLED", true),
		"enable the periodic LLM moderation pass (requires OPENAI_API_KEY)")
	modBaseURL := flag.String("moderate-base-url", envDefault("OPENAI_BASE_URL", "https://openrouter.ai/api/v1"),
		"OpenAI-compatible API base URL")
	modModel := flag.String("moderate-model", envDefault("OPENAI_MODEL", "nvidia/nemotron-3-super-120b-a12b:free"),
		"chat model used for moderation")
	modInterval := flag.Duration("moderate-interval", envDuration("MODERATION_INTERVAL", time.Hour),
		"how often to run the moderation pass")
	modTimeout := flag.Duration("moderate-timeout", envDuration("MODERATION_TIMEOUT", 2*time.Minute),
		"timeout per moderation request attempt")
	modBatch := flag.Int("moderate-batch", envInt("MODERATION_BATCH_SIZE", 100),
		"torrent titles per moderation request")
	modMaxBatches := flag.Int("moderate-max-batches", envInt("MODERATION_MAX_BATCHES", 20),
		"max batches per moderation pass (0 = unlimited)")
	modDryRun := flag.Bool("moderate-dry-run", envBool("MODERATION_DRY_RUN", false),
		"log what moderation would delete without deleting it")
	modTrim := flag.Bool("moderate-trim-titles", envBool("MODERATION_TRIM_TITLES", true),
		"also strip advertising from torrent titles during the moderation pass")
	flag.Parse()

	var st *store.Store
	var err error
	if *databaseURL != "" {
		st, err = store.OpenURL(*databaseURL)
	} else {
		st, err = store.Open(*dbPath)
	}
	if err != nil {
		logger.Fatalf("store: %v", err)
	}
	defer st.Close()

	if *seedDemo {
		seed(st, logger)
	}

	// Settings registry (internal/settings): the base layer is what
	// the flags/env resolved to above; overrides saved from the admin
	// settings page layer on top and win. Structural values below are
	// read back out of the manager, so a restart applies whatever the
	// settings page last saved; live items are read through the
	// manager at use time by the modules themselves.
	base := map[string]string{}
	fromEnv := map[string]bool{}
	reg := func(key, val string) {
		base[key] = val
		fromEnv[key] = os.Getenv(key) != ""
	}
	reg(settings.KeyHTTPAddr, *addr)
	reg(settings.KeyDBPath, *dbPath)
	reg(settings.KeyDatabaseURL, *databaseURL)
	reg(settings.KeyAdminToken, *adminToken)
	reg(settings.KeyEnvFile, envPath)
	reg(settings.KeySiteName, envDefault("SITE_NAME", "DHTSearch"))
	reg(settings.KeyThemeColor, envDefault("THEME_COLOR", "teal"))
	reg(settings.KeyThemeAppearance, envDefault("THEME_APPEARANCE", "system"))
	reg(settings.KeyCrawlEnabled, strconv.FormatBool(*crawlEnabled))
	reg(settings.KeyCrawlerWatchdogEnabled, strconv.FormatBool(envBool("CRAWLER_WATCHDOG_ENABLED", true)))
	reg(settings.KeyDHTPort, strconv.Itoa(*dhtPort))
	reg(settings.KeyDHTSamplers, strconv.Itoa(*samplers))
	reg(settings.KeyFetchMetadata, strconv.FormatBool(*fetchMetadata))
	reg(settings.KeyMetaWorkers, strconv.Itoa(*metaWorkers))
	reg(settings.KeyMetaTimeout, metaTimeout.String())
	reg(settings.KeyMetaPrescreenEnabled, strconv.FormatBool(*metaPrescreen))
	reg(settings.KeyMetaPrescreenTimeout, metaPrescreenTimeout.String())
	reg(settings.KeyMetaPriorityEnabled, strconv.FormatBool(*metaPriority))
	reg(settings.KeyMinTorrentSize, strconv.FormatInt(*minSize, 10))
	reg(settings.KeyScrapeEnabled, strconv.FormatBool(*scrapeEnabled))
	reg(settings.KeyTrackers, *trackersCSV)
	reg(settings.KeyModerationEnabled, strconv.FormatBool(*modEnabled))
	reg(settings.KeyOpenAIAPIKey, os.Getenv("OPENAI_API_KEY"))
	reg(settings.KeyOpenAIBaseURL, *modBaseURL)
	reg(settings.KeyOpenAIModel, *modModel)
	reg(settings.KeyModerationInterval, modInterval.String())
	reg(settings.KeyModerationTimeout, modTimeout.String())
	reg(settings.KeyModerationBatchSize, strconv.Itoa(*modBatch))
	reg(settings.KeyModerationMaxBatches, strconv.Itoa(*modMaxBatches))
	reg(settings.KeyModerationDryRun, strconv.FormatBool(*modDryRun))
	reg(settings.KeyModerationTrimTitles, strconv.FormatBool(*modTrim))
	reg(settings.KeyFilterEnabled, strconv.FormatBool(envBool("FILTER_ENABLED", true)))
	reg(settings.KeyFilterAdultEnabled, strconv.FormatBool(envBool("FILTER_ADULT_ENABLED", true)))
	reg(settings.KeyFilterSpamEnabled, strconv.FormatBool(envBool("FILTER_SPAM_ENABLED", true)))
	reg(settings.KeyFilterTooSmallEnabled, strconv.FormatBool(envBool("FILTER_TOO_SMALL_ENABLED", true)))
	reg(settings.KeyFilterMaxNameLen, strconv.Itoa(envInt("FILTER_MAX_NAME_LEN", 300)))
	reg(settings.KeyFilterStuffingMinRepeat, strconv.Itoa(envInt("FILTER_STUFFING_MIN_REPEAT", 4)))
	reg(settings.KeyFilterNonAlnumMaxRatio, strconv.FormatFloat(envFloat("FILTER_NON_ALNUM_MAX_RATIO", 0.5), 'f', -1, 64))
	reg(settings.KeyFilterRandomFileMaxRatio, strconv.FormatFloat(envFloat("FILTER_RANDOM_FILE_MAX_RATIO", 0.3), 'f', -1, 64))
	reg(settings.KeyFilterBlockWords, envDefault("FILTER_BLOCK_WORDS", ""))
	reg(settings.KeyFilterAllowWords, envDefault("FILTER_ALLOW_WORDS", ""))
	reg(settings.KeyTrendingEnabled, strconv.FormatBool(*trendEnabled))
	reg(settings.KeyTrendingInterval, trendInterval.String())
	reg(settings.KeyTrendingLimit, strconv.Itoa(*trendLimit))
	reg(settings.KeyFeedsEnabled, strconv.FormatBool(*feedsEnabled))
	reg(settings.KeyFeeds, *feedsCSV)
	reg(settings.KeyFeedsKeywordURL, *feedKeywordURL)
	reg(settings.KeyFeedsInterval, feedsInterval.String())
	reg(settings.KeyFeedsWorkers, strconv.Itoa(*feedsWorkers))
	reg(settings.KeyFeedsMetaTimeout, feedsTimeout.String())
	reg(settings.KeyGitHubClientID, envDefault("GITHUB_CLIENT_ID", ""))
	reg(settings.KeyGitHubClientSecret, os.Getenv("GITHUB_CLIENT_SECRET"))
	reg(settings.KeyGitHubAllowedUsers, envDefault("GITHUB_ALLOWED_USERS", ""))
	reg(settings.KeyOIDCIssuer, envDefault("OIDC_ISSUER", ""))
	reg(settings.KeyOIDCClientID, envDefault("OIDC_CLIENT_ID", ""))
	reg(settings.KeyOIDCClientSecret, os.Getenv("OIDC_CLIENT_SECRET"))
	reg(settings.KeyOIDCProviderName, envDefault("OIDC_PROVIDER_NAME", "SSO"))
	reg(settings.KeyOIDCAllowedEmails, envDefault("OIDC_ALLOWED_EMAILS", ""))
	reg(settings.KeyOIDCAllowedDomain, envDefault("OIDC_ALLOWED_DOMAIN", ""))
	reg(settings.KeyRateLimitRPS, strconv.FormatFloat(*rateRPS, 'f', -1, 64))
	reg(settings.KeyRateLimitBurst, strconv.Itoa(*rateBurst))
	reg(settings.KeySearchMaxInflight, strconv.Itoa(*searchInflight))
	reg(settings.KeySearchTimeout, searchTimeout.String())
	reg(settings.KeyPublicAPIEnabled, strconv.FormatBool(envBool("PUBLIC_API_ENABLED", true)))
	reg(settings.KeyPublicAPIRequireToken, strconv.FormatBool(envBool("PUBLIC_API_REQUIRE_TOKEN", false)))
	reg(settings.KeyPublicAPITokenRatePerMin, strconv.Itoa(envInt("PUBLIC_API_TOKEN_RATE_PER_MIN", 600)))

	mgr, err := settings.New(base, fromEnv, st, logger)
	if err != nil {
		logger.Fatalf("settings: %v", err)
	}

	// The content filter keeps package-level state; push its live
	// settings now and on every accepted settings update.
	applyFilter := func() {
		filter.SetMinTotalSize(mgr.Int64(settings.KeyMinTorrentSize))
		filter.SetBlockWords(mgr.List(settings.KeyFilterBlockWords))
		filter.SetAllowWords(mgr.List(settings.KeyFilterAllowWords))
		filter.SetConfig(filter.Config{
			Enabled:            mgr.Bool(settings.KeyFilterEnabled),
			AdultEnabled:       mgr.Bool(settings.KeyFilterAdultEnabled),
			SpamEnabled:        mgr.Bool(settings.KeyFilterSpamEnabled),
			TooSmallEnabled:    mgr.Bool(settings.KeyFilterTooSmallEnabled),
			MaxNameLen:         mgr.Int(settings.KeyFilterMaxNameLen),
			StuffingMinRepeat:  mgr.Int(settings.KeyFilterStuffingMinRepeat),
			NonAlnumMaxRatio:   mgr.Float(settings.KeyFilterNonAlnumMaxRatio),
			RandomFileMaxRatio: mgr.Float(settings.KeyFilterRandomFileMaxRatio),
		})
	}
	applyFilter()
	mgr.SetOnChange(func([]string) { applyFilter() })

	crawlOn := mgr.Bool(settings.KeyCrawlEnabled)
	fetchMeta := mgr.Bool(settings.KeyFetchMetadata)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Crawler. The node-persistence hooks adapt the store's DHTNode
	// rows to the crawler's PersistedNode shape (the same adapter
	// pattern as scraperStatus below); the crawler itself stays
	// store-free.
	cr, err := crawler.Start(crawler.Config{
		Port:     mgr.Int(settings.KeyDHTPort),
		Enabled:  crawlOn,
		Samplers: mgr.Int(settings.KeyDHTSamplers),
		Logger:   logger,
		LoadSeedNodes: func(ctx context.Context) ([]crawler.PersistedNode, error) {
			rows, err := st.LoadDHTNodes(ctx, 512)
			if err != nil {
				return nil, err
			}
			out := make([]crawler.PersistedNode, 0, len(rows))
			for _, r := range rows {
				out = append(out, crawler.PersistedNode{
					Addr: r.Addr, SuccW: r.SuccW, FailW: r.FailW,
					LatencyMs: r.LatencyMs, Attempts: r.Attempts, UpdatedAt: r.UpdatedAt,
				})
			}
			return out, nil
		},
		SaveSeedNodes: func(ctx context.Context, nodes []crawler.PersistedNode) error {
			rows := make([]store.DHTNode, 0, len(nodes))
			for _, n := range nodes {
				rows = append(rows, store.DHTNode{
					Addr: n.Addr, SuccW: n.SuccW, FailW: n.FailW,
					LatencyMs: n.LatencyMs, Attempts: n.Attempts, UpdatedAt: n.UpdatedAt,
				})
			}
			return st.SaveDHTNodes(ctx, rows)
		},
	})
	if err != nil {
		logger.Fatalf("crawler: %v", err)
	}
	defer cr.Close()

	trackerList := mgr.List(settings.KeyTrackers)

	// Trending runs in dynamic mode: the enable gate and the
	// interval/limit are re-read from the settings registry each
	// cycle, so the settings page controls it live.
	trendSvc := trending.New(trending.Config{
		Interval: *trendInterval,
		Limit:    *trendLimit,
		Logger:   logger,
		Store:    st,
		ConfigFunc: func() trending.Config {
			return trending.Config{
				Interval: mgr.Duration(settings.KeyTrendingInterval),
				Limit:    mgr.Int(settings.KeyTrendingLimit),
			}
		},
		EnabledFunc: func() bool { return mgr.Bool(settings.KeyTrendingEnabled) },
	})

	// Pipeline: infohashes -> scrape ranking -> metadata -> filter -> store.
	var fetcher *metadata.Fetcher
	var scr *scraper.Scraper
	var feedSvc *feeds.Service
	var feedFetch *metadata.Fetcher
	var metaQueue *metadata.Queue
	prescreenCfg := func(c *metadata.Config) {
		c.PrescreenEnabled = mgr.Bool(settings.KeyMetaPrescreenEnabled)
		c.PrescreenTimeout = mgr.Duration(settings.KeyMetaPrescreenTimeout)
		c.PrescreenEnabledFunc = func() bool { return mgr.Bool(settings.KeyMetaPrescreenEnabled) }
		c.PrescreenTimeoutFunc = func() time.Duration { return mgr.Duration(settings.KeyMetaPrescreenTimeout) }
	}
	if fetchMeta {
		var err error
		mainMetaCfg := metadata.Config{
			Workers:     mgr.Int(settings.KeyMetaWorkers),
			Timeout:     mgr.Duration(settings.KeyMetaTimeout),
			TimeoutFunc: func() time.Duration { return mgr.Duration(settings.KeyMetaTimeout) },
			Trackers:    trackerList,
			Logger:      logger,
		}
		prescreenCfg(&mainMetaCfg)
		fetcher, err = metadata.NewFetcher(mainMetaCfg)
		if err != nil {
			logger.Fatalf("metadata: %v", err)
		}
		defer fetcher.Close()
		feed := cr.Infohashes()
		var scraped <-chan scraper.Item
		if mgr.Bool(settings.KeyScrapeEnabled) && len(trackerList) > 0 && feed != nil {
			scr, err = scraper.New(scraper.Config{
				Trackers: trackerList,
				Logger:   logger,
			})
			if err != nil {
				logger.Printf("scraper: disabled: %v", err)
			} else {
				defer scr.Close()
				scr.Run(ctx, feed)
				scraped = scr.Out()
			}
		}
		// Feed hashes get their own small fetcher rather than a place in the
		// main one's queue: the main client runs META_WORKERS concurrent
		// fetches of mostly dead DHT hashes, and a live fansub swarm that a
		// standalone client fetches in seconds times out inside it.
		// The service is built unconditionally in dynamic mode: the
		// enable gate, feed list, keyword URL and interval are re-read
		// from the settings registry every poll.
		feedKeywords := func() []string {
			snap := trendSvc.Get()
			return append(append([]string(nil), snap.TVJP...), snap.TVKR...)
		}
		feedSvc = feeds.New(feeds.Config{
			Feeds:      mgr.List(settings.KeyFeeds),
			KeywordURL: mgr.Get(settings.KeyFeedsKeywordURL),
			Keywords:   feedKeywords,
			Interval:   mgr.Duration(settings.KeyFeedsInterval),
			Store:      st,
			Logger:     logger,
			ConfigFunc: func() feeds.Config {
				return feeds.Config{
					Feeds:      mgr.List(settings.KeyFeeds),
					KeywordURL: mgr.Get(settings.KeyFeedsKeywordURL),
					Keywords:   feedKeywords,
					Interval:   mgr.Duration(settings.KeyFeedsInterval),
				}
			},
			EnabledFunc: func() bool { return mgr.Bool(settings.KeyFeedsEnabled) },
		})
		go func(fs *feeds.Service) {
			// Let trending load first so the opening poll already
			// searches the chip titles; bounded in case Douban hangs.
			select {
			case <-trendSvc.Ready():
			case <-time.After(2 * time.Minute):
			case <-ctx.Done():
				return
			}
			fs.Run(ctx)
		}(feedSvc)
		feedMetaCfg := metadata.Config{
			Workers:     mgr.Int(settings.KeyFeedsWorkers),
			Timeout:     mgr.Duration(settings.KeyFeedsMetaTimeout),
			TimeoutFunc: func() time.Duration { return mgr.Duration(settings.KeyFeedsMetaTimeout) },
			Trackers:    trackerList,
			Logger:      logger,
		}
		prescreenCfg(&feedMetaCfg)
		feedFetcher, err := metadata.NewFetcher(feedMetaCfg)
		if err != nil {
			logger.Fatalf("feeds metadata: %v", err)
		}
		defer feedFetcher.Close()
		onRecord := func(rec metadata.Record) {
			// onRecord runs synchronously in a metadata worker's loop,
			// so its store writes get a hard timeout: an unbounded
			// write over a half-dead database connection would park
			// the worker forever (see store.UpsertContext).
			wctx, wcancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer wcancel()
			res := filter.Check(rec.Name, rec.Files, rec.TotalSize)
			if res.Adult {
				st.IncrStatContext(wctx, "adult_filtered", 1)
				return
			}
			if res.Spam {
				st.IncrStatContext(wctx, "spam_filtered", 1)
				return
			}
			if res.TooSmall {
				st.IncrStatContext(wctx, "size_filtered", 1)
				return
			}
			if err := st.UpsertContext(wctx, store.Torrent{
				InfoHash:  rec.InfoHash,
				Name:      rec.Name,
				TotalSize: rec.TotalSize,
				FileCount: rec.FileCount,
				Files:     rec.Files,
				CreatedAt: time.Now().Unix(),
				Alias:     feedAlias(feedSvc, rec.InfoHash),
			}); err != nil {
				logger.Printf("store upsert: %v", err)
				return
			}
			st.IncrStatContext(wctx, "fetched", 1)
		}
		if mgr.Bool(settings.KeyMetaPriorityEnabled) {
			// Workers pop from a priority queue (seeders > source >
			// freshness). The scraper's seeder counts ride along on its
			// items, so its ranking survives the hand-off; without the
			// scraper the queue is the only prioritization the pipeline
			// gets (previously a plain FIFO channel in that case).
			metaQueue = metadata.NewQueue(0)
			q := metaQueue
			if scraped != nil {
				go func() {
					for it := range scraped {
						q.Push(metadata.Item{Hash: it.Hash, Seeders: it.Seeders, Source: metadata.SourceScrape, At: time.Now()})
					}
				}()
			} else {
				go func() {
					for h := range feed {
						q.Push(metadata.Item{Hash: h, Seeders: -1, Source: metadata.SourceDHT, At: time.Now()})
					}
				}()
			}
			fetcher.RunQueue(ctx, q, onRecord)
		} else if scraped != nil {
			hashes := make(chan string)
			go func() {
				defer close(hashes)
				for it := range scraped {
					select {
					case hashes <- it.Hash:
					case <-ctx.Done():
						return
					}
				}
			}()
			fetcher.Run(ctx, hashes, onRecord)
		} else {
			fetcher.Run(ctx, feed, onRecord)
		}
		if feedFetcher != nil {
			feedFetcher.Run(ctx, feedSvc.Out(), onRecord)
			feedFetch = feedFetcher
		}
	} else {
		// Bare infohash collection: no metadata, no filter.
		go func() {
			for hash := range cr.Infohashes() {
				wctx, wcancel := context.WithTimeout(context.Background(), 15*time.Second)
				err := st.UpsertContext(wctx, store.Torrent{
					InfoHash:  hash,
					Name:      hash,
					TotalSize: 1,
					CreatedAt: time.Now().Unix(),
				})
				if err != nil {
					wcancel()
					logger.Printf("store upsert: %v", err)
					continue
				}
				st.IncrStatContext(wctx, "seen", 1)
				wcancel()
			}
		}()
	}

	// Periodic LLM moderation pass over rows the static filter admitted.
	// Always running in dynamic mode: it idles while disabled or keyless
	// and picks up settings changes — including the API key itself —
	// without a restart.
	mod, err := moderator.New(st, moderator.Config{
		Logger: logger,
		ConfigFunc: func() moderator.Config {
			return moderator.Config{
				Enabled:    mgr.Bool(settings.KeyModerationEnabled),
				BaseURL:    mgr.Get(settings.KeyOpenAIBaseURL),
				APIKey:     mgr.Get(settings.KeyOpenAIAPIKey),
				Model:      mgr.Get(settings.KeyOpenAIModel),
				Interval:   mgr.Duration(settings.KeyModerationInterval),
				BatchSize:  mgr.Int(settings.KeyModerationBatchSize),
				MaxBatches: mgr.Int(settings.KeyModerationMaxBatches),
				Timeout:    mgr.Duration(settings.KeyModerationTimeout),
				DryRun:     mgr.Bool(settings.KeyModerationDryRun),
				TrimTitles: mgr.Bool(settings.KeyModerationTrimTitles),
				Logger:     logger,
			}
		},
	})
	if err != nil {
		logger.Fatalf("moderator: %v", err)
	}
	go mod.Run(ctx)

	// Douban trending fetcher for the homepage quick-search chips.
	// The enable gate is consulted per request: disabled means the
	// endpoint serves empty lists even though the service keeps its
	// last snapshot. Restore first: the persisted last good charts
	// serve from the first request, even if Douban is unreachable
	// right now.
	trendSvc.Restore(ctx)
	go trendSvc.Run(ctx)
	trendFn := func() api.Trending {
		t := api.Trending{Movies: []string{}, TV: []string{}, TVJP: []string{}, TVKR: []string{}}
		if !mgr.Bool(settings.KeyTrendingEnabled) {
			return t
		}
		snap := trendSvc.Get()
		t.Movies, t.TV, t.TVJP, t.TVKR = snap.Movies, snap.TV, snap.TVJP, snap.TVKR
		if !snap.UpdatedAt.IsZero() {
			t.UpdatedAt = snap.UpdatedAt.Unix()
		}
		return t
	}

	// Mirror the crawler's in-memory seen count and the fetch outcome
	// counters into the stats table periodically. Both are process-local and
	// monotonic, so only the delta since the last tick is applied.
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				wctx, wcancel := context.WithTimeout(context.Background(), 15*time.Second)
				seen := int64(cr.SeenCount())
				st.IncrStatContext(wctx, "seen", seen-prevSeen(&seen))
				if fetcher != nil {
					_, timedOut, failed := fetcher.Stats()
					st.IncrStatContext(wctx, "meta_timed_out", timedOut-prevTimedOut(&timedOut))
					st.IncrStatContext(wctx, "meta_failed", failed-prevFailed(&failed))
					pd := fetcher.PrescreenDropped()
					st.IncrStatContext(wctx, "prescreen_dropped", pd-prevPrescreen(&pd))
				}
				wcancel()
			}
		}
	}()

	// Pipeline stall watchdog + periodic status line (internal/
	// watchdog). The probe reads the same counters the status line
	// and the admin overview use; the ingest-progress signal is the
	// stats table's "fetched" counter, which onRecord increments
	// only after a record is actually stored (in bare-infohash mode
	// the "seen" stat plays that role, one increment per upsert).
	wd := watchdog.New(watchdog.Config{
		Active:  crawlOn,
		Enabled: func() bool { return mgr.Bool(settings.KeyCrawlerWatchdogEnabled) },
		Logger:  logger,
		Probe: func(ctx context.Context) (watchdog.Probe, error) {
			p := watchdog.Probe{Seen: cr.Stats().Harvested}
			if fetcher != nil {
				_, timedOut, _ := fetcher.Stats()
				p.TimedOut += timedOut
				p.Prescreen += fetcher.PrescreenDropped()
			}
			if feedFetch != nil {
				_, timedOut, _ := feedFetch.Stats()
				p.TimedOut += timedOut
				p.Prescreen += feedFetch.PrescreenDropped()
			}
			if metaQueue != nil {
				p.QueueDepth = metaQueue.Len()
			}
			stats, err := st.Stats(ctx)
			if err != nil {
				return p, err
			}
			if fetchMeta {
				p.Fetched = stats["fetched"]
			} else {
				p.Fetched = stats["seen"]
			}
			return p, nil
		},
	})
	go wd.Run(ctx)

	// HTTP API. The timeouts close slow-read and slow-write (slowloris)
	// connections instead of letting each one pin a goroutine and a socket;
	// MaxHeaderBytes stops oversized header floods from ballooning memory.
	srv := &http.Server{
		Addr: *addr,
		Handler: api.New(st, func() api.CrawlerStatus {
			cs := cr.Stats()
			ws := wd.Status()
			var wSince int64
			if !ws.Since.IsZero() {
				wSince = ws.Since.Unix()
			}
			return api.CrawlerStatus{
				Enabled:       cr.Enabled(),
				Seen:          int64(cr.SeenCount()),
				Nodes:         cs.Nodes,
				Queued:        cs.Queued,
				Sampled:       cs.Sampled,
				SampleErr:     cs.SampleErr,
				Harvested:     cs.Harvested,
				TrackedNodes:  cs.TrackedNodes,
				AvgNodeScore:  cs.AvgNodeScore,
				BannedNodes:   cs.BannedNodes,
				RestoredNodes: cs.RestoredNodes,
				Watchdog:      string(ws.State),
				WatchdogSince: wSince,
			}
		}, logger, api.Options{
			RateRPS:       *rateRPS,
			RateBurst:     *rateBurst,
			MaxInflight:   *searchInflight,
			SearchTimeout: *searchTimeout,
			ScraperStatus: scraperStatus(scr),
			MetaStatus: func() api.MetaStatus {
				if metaQueue == nil {
					return api.MetaStatus{}
				}
				return api.MetaStatus{QueueDepth: metaQueue.Len()}
			},
			FeedsStatus: feedsStatus(feedSvc, feedFetch),
			Trending:    trendFn,
			TrendingStatus: func() api.TrendingStatus {
				ts := trendSvc.Status()
				out := api.TrendingStatus{Failing: ts.Failing, FailStreak: ts.FailStreak}
				if !ts.LastSuccess.IsZero() {
					out.LastSuccess = ts.LastSuccess.Unix()
				}
				return out
			},
			AdminToken:    *adminToken,
			InternalToken: *internalToken,
			Settings:      mgr,
			Trackers:      func() []string { return mgr.List(settings.KeyTrackers) },
		}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    16 << 10,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()
	dbDesc := *dbPath
	if *databaseURL != "" {
		// Never log the URL itself: it carries the password.
		dbDesc = st.Dialect()
	}
	if *adminToken == "" {
		// No recovery token, but the console can still be enabled by
		// admin accounts or a configured federated login.
		nUsers, _ := st.CountAdminUsers(context.Background())
		oauth := mgr.Get(settings.KeyGitHubClientID) != "" || mgr.Get(settings.KeyOIDCIssuer) != ""
		if nUsers > 0 || oauth {
			logger.Printf("admin: enabled (account/OAuth login; ADMIN_TOKEN recovery credential not set)")
		} else {
			logger.Printf("admin: disabled (ADMIN_TOKEN not set; /api/admin/* answers 503, see env.example)")
		}
	} else {
		logger.Printf("admin: enabled (console API at /api/admin/*, UI at the web app's /admin)")
	}
	if *internalToken != "" {
		logger.Printf("public api: first-party channel enabled (the web frontend bypasses PUBLIC_API_REQUIRE_TOKEN; external callers are unaffected)")
	} else if mgr.Bool(settings.KeyPublicAPIRequireToken) {
		logger.Printf("public api: WARNING: PUBLIC_API_REQUIRE_TOKEN is on but INTERNAL_API_TOKEN is not set — the web frontend's own searches will be answered 401; run install.sh upgrade or set the same INTERNAL_API_TOKEN on both services")
	}
	logger.Printf("http: listening on %s (db=%s crawl=%v fetch-metadata=%v)",
		*addr, dbDesc, crawlOn, fetchMeta)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		logger.Fatalf("http: %v", err)
	}
	logger.Printf("shutdown complete")
}

var (
	lastSeen      int64
	lastTimedOut  int64
	lastFailed    int64
	lastPrescreen int64
)

// prevSeen returns the previously reported seen count and records the new
// one, so the stats mirror only applies deltas.
func prevSeen(cur *int64) int64 {
	prev := lastSeen
	lastSeen = *cur
	return prev
}

// prevTimedOut and prevFailed do the same for the fetch outcome counters.
func prevTimedOut(cur *int64) int64 {
	prev := lastTimedOut
	lastTimedOut = *cur
	return prev
}

func prevFailed(cur *int64) int64 {
	prev := lastFailed
	lastFailed = *cur
	return prev
}

func prevPrescreen(cur *int64) int64 {
	prev := lastPrescreen
	lastPrescreen = *cur
	return prev
}

// seed inserts demo records so the API is testable without DHT access.
func seed(st *store.Store, logger *log.Logger) {
	type demo struct {
		hash, name string
		size       int64
		files      []filter.File
	}
	now := time.Now().Unix()
	demos := []demo{
		{"08ada5a7a6183aae1e09d831df6748d566095a10", "Sintel (2010) 1080p Open Movie", 867 << 20,
			[]filter.File{{Path: "Sintel.2010.1080p.mkv", Size: 867 << 20}}},
		{"56bc8614cf8d77448cb6ea12a2bd59fd28fa9348", "Big Buck Bunny (2008) 1080p", 712 << 20,
			[]filter.File{{Path: "BigBuckBunny_1080p.mp4", Size: 712 << 20}}},
		{"2c6b6858b61c9548d0b6e51a4a3f37adac0fe0b3", "Tears of Steel (2012) 4K Open Movie", 2 << 30,
			[]filter.File{{Path: "tears_of_steel_4k.mov", Size: 2 << 30}}},
		{"47f5e5f5e84d0f4a1d2b7f74b2cb24f2b4d2a0b1", "Ubuntu 24.04.1 LTS Desktop amd64 ISO", 5_900_000_000,
			[]filter.File{{Path: "ubuntu-24.04.1-desktop-amd64.iso", Size: 5_900_000_000}}},
		{"e2467cbf021192c241367b892230dc1e05c0580e", "Debian 12.7.0 amd64 netinst ISO", 660 << 20,
			[]filter.File{{Path: "debian-12.7.0-amd64-netinst.iso", Size: 660 << 20}}},
		{"3b245504cf5f11bbdbe1201cea6a6bf45aee1bc0", "Blender 4.2 LTS Windows/macOS/Linux Bundle", 380 << 20,
			[]filter.File{
				{Path: "blender-4.2.0-windows-x64.zip", Size: 190 << 20},
				{Path: "blender-4.2.0-macos-arm64.dmg", Size: 190 << 20},
			}},
		{"88594dbacbde6ef3e7eb30e4a11d0b00e0f02d1d", "NASA 4K Earth From Space Footage Collection", 12 << 30,
			[]filter.File{{Path: "earth_from_space_4k_ep1.mp4", Size: 4 << 30}}},
	}
	for i, d := range demos {
		if err := st.Upsert(store.Torrent{
			InfoHash:  d.hash,
			Name:      d.name,
			TotalSize: d.size,
			FileCount: len(d.files),
			Files:     d.files,
			CreatedAt: now - int64(len(demos)-i)*60,
		}); err != nil {
			logger.Printf("seed: %v", err)
		}
	}
	logger.Printf("seeded %d demo records", len(demos))
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
		if n, err := strconv.Atoi(v); err == nil {
			return time.Duration(n) * time.Second
		}
	}
	return fallback
}
