// Package settings holds the configuration a running server can change
// without a restart.
//
// Values live in the database so they survive restarts, with the process's
// environment supplying the initial value for keys the database has never
// been told about. Once a key is stored, the database wins: the admin page
// would otherwise be unable to change anything durably, since every restart
// would put the environment's value back.
//
// Only settings that are genuinely re-readable belong here. A worker pool
// size, for instance, is fixed when the pool is built, so offering it as a
// live control would show a value the pipeline is not using — those stay in
// the environment and are reported as restart-only.
package settings

import (
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
)

// Kind decides how a value is parsed and how the admin UI renders it.
type Kind string

const (
	KindBool  Kind = "bool"
	KindBytes Kind = "bytes"
)

// Setting keys.
const (
	FilterAdult          = "filter_adult"
	MinTorrentSize       = "min_torrent_size"
	ModerationEnabled    = "moderation_enabled"
	ModerationDryRun     = "moderation_dry_run"
	ModerationTrimTitles = "moderation_trim_titles"
)

// Def describes one live setting for the admin UI.
type Def struct {
	Key     string `json:"key"`
	Kind    Kind   `json:"kind"`
	Label   string `json:"label"`
	Help    string `json:"help"`
	Env     string `json:"env"`
	Value   string `json:"value"`
	Default string `json:"-"`
}

// defs is the whole set of live settings, in display order.
var defs = []Def{
	{
		Key: FilterAdult, Kind: KindBool, Env: "FILTER_ADULT", Default: "true",
		Label: "过滤成人内容",
		Help: "关闭后成人内容照常入库，LLM 审核也不再删除它。" +
			"只影响之后爬到的内容，已丢弃的不会回来；前端文案会自动跟随这个开关。",
	},
	{
		Key: MinTorrentSize, Kind: KindBytes, Env: "MIN_TORRENT_SIZE", Default: "104857600",
		Label: "最小种子体积",
		Help:  "低于此总体积的种子不入库，用来滤掉假种、单图和纯说明文件。",
	},
	{
		Key: ModerationEnabled, Kind: KindBool, Env: "MODERATION_ENABLED", Default: "true",
		Label: "启用 LLM 审核",
		Help:  "关闭后每小时的复审整轮跳过，不消耗任何 API 额度。",
	},
	{
		Key: ModerationDryRun, Kind: KindBool, Env: "MODERATION_DRY_RUN", Default: "false",
		Label: "审核只记录不删除",
		Help:  "照常调用模型并打印判定，但不删除也不拉黑。调整词表或换模型时先开这个。",
	},
	{
		Key: ModerationTrimTitles, Kind: KindBool, Env: "MODERATION_TRIM_TITLES", Default: "true",
		Label: "清理标题广告",
		Help:  "同一次审核请求里让模型多返回一个去广告的标题。原始标题始终保留。",
	},
}

// Defs returns the definitions with their current values filled in.
func (s *Settings) Defs() []Def {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Def, len(defs))
	for i, d := range defs {
		d.Value = s.vals[d.Key]
		out[i] = d
	}
	return out
}

// defByKey looks up a definition, reporting whether the key is a real setting.
// Unknown keys are rejected rather than stored, so a typo in an admin request
// cannot silently create a value nothing reads.
func defByKey(key string) (Def, bool) {
	for _, d := range defs {
		if d.Key == key {
			return d, true
		}
	}
	return Def{}, false
}

// Store persists settings across restarts.
type Store interface {
	Settings() (map[string]string, error)
	SetSetting(key, value string) error
}

// Settings is the live configuration. Reads are cheap and safe from the
// pipeline's hot paths; writes go through the database first so a value the
// caller has been told was saved is genuinely durable.
type Settings struct {
	db Store

	mu   sync.RWMutex
	vals map[string]string

	// gen increments on every change, so callers holding a derived value can
	// notice it went stale without re-reading the map on every use.
	gen atomic.Uint64
}

// Load reads the stored settings, filling in anything unset from env, which
// is supplied as key -> raw value by the caller (already flag-parsed, so a
// command-line override lands here too).
func Load(db Store, env map[string]string) (*Settings, error) {
	stored, err := db.Settings()
	if err != nil {
		return nil, err
	}
	s := &Settings{db: db, vals: make(map[string]string, len(defs))}
	for _, d := range defs {
		switch {
		case stored[d.Key] != "":
			s.vals[d.Key] = stored[d.Key]
		case env[d.Key] != "":
			s.vals[d.Key] = env[d.Key]
		default:
			s.vals[d.Key] = d.Default
		}
	}
	return s, nil
}

// Set validates and stores a new value. The database write happens before the
// in-memory update, so a failure leaves the running config matching what is
// on disk rather than drifting from it.
func (s *Settings) Set(key, raw string) error {
	d, ok := defByKey(key)
	if !ok {
		return fmt.Errorf("unknown setting %q", key)
	}
	norm, err := normalize(d.Kind, raw)
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	if err := s.db.SetSetting(key, norm); err != nil {
		return err
	}
	s.mu.Lock()
	s.vals[key] = norm
	s.mu.Unlock()
	s.gen.Add(1)
	return nil
}

// normalize parses a raw value and returns its canonical string form.
func normalize(kind Kind, raw string) (string, error) {
	switch kind {
	case KindBool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return "", fmt.Errorf("not a boolean: %q", raw)
		}
		return strconv.FormatBool(b), nil
	case KindBytes:
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return "", fmt.Errorf("not a number: %q", raw)
		}
		if n < 0 {
			return "", fmt.Errorf("must not be negative: %d", n)
		}
		return strconv.FormatInt(n, 10), nil
	}
	return "", fmt.Errorf("unknown kind %q", kind)
}

// Bool returns a boolean setting. An unknown key returns false rather than
// panicking: callers are the pipeline's hot paths, and a config lookup is
// never worth taking the process down for.
func (s *Settings) Bool(key string) bool {
	s.mu.RLock()
	v := s.vals[key]
	s.mu.RUnlock()
	b, _ := strconv.ParseBool(v)
	return b
}

// Int64 returns a numeric setting, or 0 for an unknown key.
func (s *Settings) Int64(key string) int64 {
	s.mu.RLock()
	v := s.vals[key]
	s.mu.RUnlock()
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}

// Gen returns a counter that changes whenever any setting changes.
func (s *Settings) Gen() uint64 { return s.gen.Load() }
