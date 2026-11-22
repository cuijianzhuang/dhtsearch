// Package trending periodically fetches Douban's trending Western movie and
// US-TV lists and caches the titles, feeding the homepage's quick-search
// chips.
//
// Douban has no official API; this uses the JSON endpoints its own frontend
// calls (movie.douban.com/j/search_subjects and /j/subject_abstract). The
// list endpoint only carries Chinese titles, so each subject's English name
// is resolved through one subject_abstract call, cached by subject ID —
// steady-state cost is a couple of requests per interval for whatever newly
// entered the charts. Every failure mode degrades to "keep serving the last
// good list", and an empty snapshot just means the homepage renders no
// trending section.
//
// The last good state is also persisted (see StateStore): the snapshot
// used to live only in memory, so a restart onto a machine that cannot
// currently reach Douban served an empty homepage section until the
// first successful fetch — potentially forever, at one attempt per
// interval. Now Restore loads the persisted state at startup, and the
// refresh loop retries failures with exponential backoff instead of
// waiting a full interval between attempts.
package trending

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	defaultBaseURL  = "https://movie.douban.com"
	defaultInterval = time.Hour
	defaultLimit    = 10
	fetchTimeout    = 20 * time.Second
	// abstractDelay spaces out the per-subject metadata requests so a cold
	// start doesn't burst-hammer Douban.
	abstractDelay = 500 * time.Millisecond
	// maxBody bounds how much of a response is read; the real payloads are a
	// few KB, so 1 MB means "something is very wrong upstream".
	maxBody = 1 << 20
	// maxCachedNames caps the subject-ID -> English-title cache. The
	// charts hold at most a few dozen live subjects, but without a
	// cap the cache (and the persisted blob carrying it) grew by one
	// entry for every subject that ever charted, forever. The cache
	// is purely an optimization — an evicted subject is simply
	// re-resolved if it charts again — so at the cap the insert path
	// evicts arbitrary entries until there is room.
	maxCachedNames = 4096
	// browserUA is required: Douban serves these JSON endpoints to browsers
	// and rejects obvious non-browser clients.
	browserUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
		"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"
)

// retryBase is the first retry delay after a refresh cycle in which
// any chart failed; the delay doubles per consecutive failed cycle
// and is capped at the configured interval, so a post-restart outage
// is retried within a minute while a persistent one backs off to the
// normal cadence instead of hammering Douban. A var so tests can
// shrink it.
var retryBase = time.Minute

// Config tunes the fetcher. Zero values pick the defaults above.
type Config struct {
	// Interval between refreshes.
	Interval time.Duration
	// Limit is how many titles to keep per list.
	Limit int
	// BaseURL overrides the Douban origin (tests point it at a stub).
	BaseURL string
	Logger  *log.Logger
	// ConfigFunc, when non-nil, supplies fresh Interval/Limit values
	// before every refresh (the settings system's live view). BaseURL
	// and Logger stay from the initial Config — they are wiring, not
	// operator knobs.
	ConfigFunc func() Config
	// EnabledFunc, when non-nil, gates refreshing: false skips the
	// fetch entirely (the last snapshot stays, and callers that check
	// the same gate serve empty lists). Nil means always enabled.
	EnabledFunc func() bool
	// Store, when non-nil, persists the last good state across
	// restarts: Restore loads it at startup and every refresh that
	// updates a chart writes it back. Nil keeps the historical
	// in-memory-only behavior (tests, tools).
	Store StateStore
}

// StateStore is the persistence the service needs: one opaque blob,
// written whole. *store.Store satisfies it.
type StateStore interface {
	LoadTrendingState(ctx context.Context) ([]byte, error)
	SaveTrendingState(ctx context.Context, data []byte) error
}

// Snapshot is the current trending state.
type Snapshot struct {
	Movies    []string  `json:"movies"` // 欧美 movies
	TV        []string  `json:"tv"`     // 美剧
	TVJP      []string  `json:"tv_jp"`  // 日剧
	TVKR      []string  `json:"tv_kr"`  // 韩剧
	UpdatedAt time.Time `json:"updated_at"`
}

// Status reports refresh health for the admin overview: whether the
// latest cycles are failing and when a fetch last fully succeeded.
type Status struct {
	UpdatedAt   time.Time // last snapshot change (zero: never populated)
	LastSuccess time.Time // last cycle in which every chart fetched
	FailStreak  int       // consecutive cycles with at least one failed chart
	Failing     bool      // FailStreak > 0
}

// Service fetches and caches the trending lists.
type Service struct {
	cfg    Config
	client *http.Client

	// cfgFn/enabledFn mirror Config's dynamic hooks (nil in static
	// wiring). Only the Run goroutine reads them.
	cfgFn     func() Config
	enabledFn func() bool

	mu   sync.RWMutex
	snap Snapshot

	// Refresh health, guarded by mu: when the last cycle completed
	// with every chart fetched, and how many cycles in a row have
	// had at least one failure since.
	lastSuccess time.Time
	failStreak  int

	// store persists snap+names across restarts; nil disables it.
	store StateStore

	// names caches subject ID -> resolved English title so refreshes only
	// pay the subject_abstract call for subjects new to the charts. Only
	// successful resolutions are cached; failures retry next interval.
	names map[string]string

	// ready closes once the first refresh attempt finishes, so consumers of
	// the lists (the RSS keyword search) can wait for a populated snapshot.
	ready     chan struct{}
	readyOnce sync.Once

	// trigger carries a manual-refresh request from TriggerRefresh to
	// the Run loop. Buffered 1: one queued request is enough, because
	// the cycle it wakes reads the freshest upstream state anyway.
	trigger chan struct{}
}

// New builds a Service; call Run to start refreshing.
func New(cfg Config) *Service {
	if cfg.Interval <= 0 {
		cfg.Interval = defaultInterval
	}
	if cfg.Limit <= 0 {
		cfg.Limit = defaultLimit
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	if cfg.Logger == nil {
		cfg.Logger = log.Default()
	}
	return &Service{
		cfg:       cfg,
		client:    &http.Client{Timeout: fetchTimeout},
		names:     make(map[string]string),
		ready:     make(chan struct{}),
		trigger:   make(chan struct{}, 1),
		cfgFn:     cfg.ConfigFunc,
		enabledFn: cfg.EnabledFunc,
		store:     cfg.Store,
	}
}

// persistedState is the JSON document written through StateStore:
// the last good snapshot plus the resolved-name cache, so a restart
// neither blanks the homepage nor repays the subject_abstract calls.
type persistedState struct {
	Snapshot Snapshot          `json:"snapshot"`
	Names    map[string]string `json:"names"`
}

// Restore loads the persisted state, if any, into memory. Call it
// once before Run. A missing or corrupt state is not an error: the
// service simply starts empty, as it always did.
func (s *Service) Restore(ctx context.Context) {
	if s.store == nil {
		return
	}
	data, err := s.store.LoadTrendingState(ctx)
	if err != nil {
		s.cfg.Logger.Printf("trending: load persisted state: %v (starting empty)", err)
		return
	}
	if len(data) == 0 {
		return
	}
	var st persistedState
	if err := json.Unmarshal(data, &st); err != nil {
		s.cfg.Logger.Printf("trending: persisted state corrupt: %v (starting empty)", err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snap = st.Snapshot
	if st.Names != nil {
		s.names = st.Names
		// States persisted by older builds can carry an uncapped
		// cache; trim it back on load so the blob stops growing.
		for id := range s.names {
			if len(s.names) <= maxCachedNames {
				break
			}
			delete(s.names, id)
		}
	}
	if !st.Snapshot.UpdatedAt.IsZero() {
		s.cfg.Logger.Printf("trending: restored snapshot from store (updated %s); serving it until the next successful fetch",
			st.Snapshot.UpdatedAt.Format(time.RFC3339))
	}
}

// persist writes the current snapshot and name cache through the
// store. Best-effort: a failed write is logged, never fatal — the
// in-memory state stays authoritative for this process.
func (s *Service) persist(ctx context.Context) {
	if s.store == nil {
		return
	}
	s.mu.RLock()
	data, err := json.Marshal(persistedState{Snapshot: s.snap, Names: s.names})
	s.mu.RUnlock()
	if err != nil {
		s.cfg.Logger.Printf("trending: encode persisted state: %v", err)
		return
	}
	if err := s.store.SaveTrendingState(ctx, data); err != nil {
		s.cfg.Logger.Printf("trending: persist state: %v", err)
	}
}

// cfgNow resolves the configuration for the next refresh: dynamic
// Interval/Limit when a ConfigFunc is wired, the static Config
// otherwise.
func (s *Service) cfgNow() Config {
	cfg := s.cfg
	if s.cfgFn != nil {
		dyn := s.cfgFn()
		cfg.Interval, cfg.Limit = dyn.Interval, dyn.Limit
		if cfg.Interval <= 0 {
			cfg.Interval = defaultInterval
		}
		if cfg.Limit <= 0 {
			cfg.Limit = defaultLimit
		}
	}
	return cfg
}

func (s *Service) enabled() bool {
	return s.enabledFn == nil || s.enabledFn()
}

// Ready is closed after the first refresh attempt, successful or not.
func (s *Service) Ready() <-chan struct{} { return s.ready }

// Get returns the latest snapshot. Empty lists mean no successful fetch yet.
func (s *Service) Get() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snap
}

// Status returns the refresh health for the admin overview.
func (s *Service) Status() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Status{
		UpdatedAt:   s.snap.UpdatedAt,
		LastSuccess: s.lastSuccess,
		FailStreak:  s.failStreak,
		Failing:     s.failStreak > 0,
	}
}

// retryDelay is how long to wait before the next cycle after
// failStreak consecutive cycles with at least one failed chart: the
// base delay doubling per streak, capped at the regular interval.
func retryDelay(failStreak int, interval time.Duration) time.Duration {
	if failStreak <= 0 {
		return interval
	}
	d := retryBase
	for i := 1; i < failStreak && d < interval; i++ {
		d *= 2
	}
	if d > interval {
		d = interval
	}
	return d
}

// TriggerRefresh asks the Run loop to start a refresh cycle now
// instead of waiting out the interval (the admin console's 立即刷新).
// It reports false when a manual request is already queued — that
// queued cycle fetches the same fresh state, so a second one adds
// nothing. A request queued while a cycle is running simply starts
// the next cycle as soon as the current one finishes. The enabled
// gate still applies: with the feature off the woken cycle skips the
// fetch like any other.
func (s *Service) TriggerRefresh() bool {
	select {
	case s.trigger <- struct{}{}:
		return true
	default:
		return false
	}
}

// cycle runs one refresh (unless the gate is off) and returns how
// long the Run loop should wait before the next one.
func (s *Service) cycle(ctx context.Context) time.Duration {
	wait := s.cfgNow().Interval
	if s.enabled() {
		ok, failed := s.refresh(ctx)
		s.mu.Lock()
		prevStreak := s.failStreak
		if failed == 0 {
			s.failStreak = 0
			s.lastSuccess = time.Now()
		} else {
			s.failStreak++
		}
		streak := s.failStreak
		s.mu.Unlock()
		wait = retryDelay(streak, wait)
		switch {
		case failed == 0 && prevStreak > 0:
			s.cfg.Logger.Printf("trending: recovered after %d failed cycle(s)", prevStreak)
		case failed > 0 && ok == 0:
			s.cfg.Logger.Printf("trending: all %d charts failed (streak %d); serving last good snapshot, next retry in %s",
				failed, streak, wait)
		case failed > 0:
			s.cfg.Logger.Printf("trending: %d of %d charts failed (streak %d); next retry in %s",
				failed, ok+failed, streak, wait)
		}
	}
	return wait
}

// Run refreshes immediately and then every Interval until ctx is
// done. The interval is re-read each cycle, and a disabled gate skips
// the fetch, so settings changes apply without a restart. A cycle in
// which any chart failed is retried on the retryDelay backoff rather
// than after a full interval, so a transient outage right after a
// restart heals in minutes; the last good snapshot keeps serving
// throughout either way. A manual TriggerRefresh cuts the wait
// short and starts the next cycle immediately.
func (s *Service) Run(ctx context.Context) {
	for {
		wait := s.cycle(ctx)
		s.readyOnce.Do(func() { close(s.ready) })
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-s.trigger:
			t.Stop()
		case <-t.C:
		}
	}
}

// charts are the Douban lists backing the homepage rows. Western charts
// resolve each subject's English name (torrent names carry those); Japanese
// and Korean dramas keep the Chinese chart title, which is what Chinese
// release names use.
var charts = []struct {
	typ, tag string
	english  bool
	apply    func(*Snapshot, []string)
}{
	{"movie", "欧美", true, func(sn *Snapshot, t []string) { sn.Movies = t }},
	{"tv", "美剧", true, func(sn *Snapshot, t []string) { sn.TV = t }},
	{"tv", "日剧", false, func(sn *Snapshot, t []string) { sn.TVJP = t }},
	{"tv", "韩剧", false, func(sn *Snapshot, t []string) { sn.TVKR = t }},
}

// refresh fetches every chart and reports how many succeeded and
// how many failed. A chart that fails keeps its previous value, so
// one bad response never blanks a homepage row. When at least one
// chart updated, the new state is persisted for the next restart.
func (s *Service) refresh(ctx context.Context) (succeeded, failed int) {
	cfg := s.cfgNow()
	type result struct {
		apply  func(*Snapshot, []string)
		titles []string
	}
	var ok []result
	for _, c := range charts {
		titles, err := s.fetchList(ctx, cfg, c.typ, c.tag, c.english)
		if err != nil {
			cfg.Logger.Printf("trending: %s/%s fetch: %v", c.typ, c.tag, err)
			failed++
			continue
		}
		ok = append(ok, result{c.apply, titles})
	}
	if len(ok) == 0 {
		return 0, failed
	}
	s.mu.Lock()
	for _, r := range ok {
		r.apply(&s.snap, r.titles)
	}
	s.snap.UpdatedAt = time.Now()
	s.mu.Unlock()
	s.persist(ctx)
	return len(ok), failed
}

type subject struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// fetchList pulls one chart and returns its titles; english resolves each
// title to the subject's English name where one exists.
func (s *Service) fetchList(ctx context.Context, cfg Config, typ, tag string, english bool) ([]string, error) {
	u := fmt.Sprintf("%s/j/search_subjects?type=%s&tag=%s&page_limit=%d&page_start=0",
		cfg.BaseURL, typ, url.QueryEscape(tag), cfg.Limit)
	body, err := s.get(ctx, u)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Subjects []subject `json:"subjects"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if len(payload.Subjects) == 0 {
		// A 200 with no subjects usually means Douban changed the response
		// shape or is soft-blocking; treat as failure to keep the last list.
		return nil, fmt.Errorf("no subjects in response")
	}
	titles := make([]string, 0, len(payload.Subjects))
	for _, sub := range payload.Subjects {
		if sub.Title == "" {
			continue
		}
		if english {
			titles = append(titles, s.englishTitle(ctx, sub))
		} else {
			titles = append(titles, sub.Title)
		}
	}
	if len(titles) == 0 {
		return nil, fmt.Errorf("no titles in response")
	}
	return titles, nil
}

// englishTitle resolves a subject's English name via subject_abstract,
// falling back to the Chinese chart title when resolution fails.
func (s *Service) englishTitle(ctx context.Context, sub subject) string {
	s.mu.RLock()
	cached, ok := s.names[sub.ID]
	s.mu.RUnlock()
	if ok {
		return cached
	}
	select {
	case <-ctx.Done():
		return sub.Title
	case <-time.After(abstractDelay):
	}
	u := fmt.Sprintf("%s/j/subject_abstract?subject_id=%s", s.cfg.BaseURL, url.QueryEscape(sub.ID))
	body, err := s.get(ctx, u)
	if err != nil {
		s.cfg.Logger.Printf("trending: abstract %s: %v", sub.ID, err)
		return sub.Title
	}
	var payload struct {
		Subject struct {
			Title string `json:"title"`
		} `json:"subject"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		s.cfg.Logger.Printf("trending: abstract %s: decode: %v", sub.ID, err)
		return sub.Title
	}
	name := extractEnglish(payload.Subject.Title, sub.Title)
	if name == "" {
		// Chinese-only production (or an unexpected format): the chart title
		// is the best keyword there is. Cache it — retrying won't improve it.
		name = sub.Title
	}
	s.mu.Lock()
	if _, ok := s.names[sub.ID]; !ok && len(s.names) >= maxCachedNames {
		for id := range s.names {
			if len(s.names) < maxCachedNames {
				break
			}
			delete(s.names, id)
		}
	}
	s.names[sub.ID] = name
	s.mu.Unlock()
	return name
}

// yearSuffix matches the trailing " (2026)" on abstract titles.
var yearSuffix = regexp.MustCompile(`\s*\(\d{4}\)\s*$`)

// hasLetter requires at least one ASCII letter for a string to count as an
// English name.
var hasLetter = regexp.MustCompile(`[A-Za-z]`)

// extractEnglish pulls the original English name out of an abstract title of
// the form "中文名 English Name‎ (2026)", given the chart's Chinese title.
// Returns "" when there is no such name.
func extractEnglish(full, chinese string) string {
	full = yearSuffix.ReplaceAllString(full, "")
	var name string
	if chinese != "" && strings.HasPrefix(full, chinese) {
		// Exact prefix strip. This is the reliable path: a Chinese title
		// that ends in digits ("疯狂动物城2 Zootopia 2") defeats any
		// per-rune classification.
		name = full[len(chinese):]
	} else {
		// Chart and abstract titles disagree; fall back to everything after
		// the last CJK rune.
		cut := 0
		for i, r := range full {
			if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
				cut = i + len(string(r))
			}
		}
		name = full[cut:]
	}
	// Trim whitespace and invisible format runes — Douban embeds a U+200E
	// LEFT-TO-RIGHT MARK after the original name (Cf, not graphic).
	name = strings.TrimFunc(name, func(r rune) bool {
		return unicode.IsSpace(r) || !unicode.IsGraphic(r)
	})
	if !hasLetter.MatchString(name) {
		return ""
	}
	return name
}

// get performs one browser-shaped GET and returns the body.
func (s *Service) get(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Referer", "https://movie.douban.com/")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxBody))
}
