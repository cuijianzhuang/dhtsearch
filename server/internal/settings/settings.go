// Package settings is the single source of truth for the server's
// configuration: a registry of every knob, the layering that resolves
// its effective value, and the admin-facing snapshot/update logic.
//
// Resolution order, highest first:
//
//  1. database override — written by the admin settings API into the
//     store's settings table (only registry items marked Editable)
//  2. environment — the process env / env file, folded into the base
//     values main() builds from its flags
//  3. the registry default
//
// Items fall into three operational classes:
//
//   - startup items (Editable=false): they decide how the process
//     itself comes up (listen address, database location, the admin
//     token). The settings page shows them read-only; changing them
//     means editing the env file and restarting.
//   - restart items (Editable=true, Restart=true): stored as DB
//     overrides and editable in the UI, but they shape structures built
//     once at startup (worker pool sizes, the scrape pipeline wiring),
//     so a change applies on the next restart.
//   - live items (Editable=true, Restart=false): consumers read them
//     through the Manager at use time (or are pushed via OnChange), so
//     a save takes effect without a restart.
//
// All values are carried as canonical strings — the same spellings the
// environment uses ("45s", "true", "a,b,c") — so every layer speaks
// one format and the typed getters parse on demand. Values are
// validated before they can enter any layer above the default.
package settings

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Type is a setting's value kind; it drives validation and the UI
// control the admin page renders.
type Type string

const (
	TypeBool     Type = "bool"
	TypeInt      Type = "int"
	TypeInt64    Type = "int64"
	TypeFloat    Type = "float"
	TypeDuration Type = "duration"
	TypeString   Type = "string"
	TypeList     Type = "list"
	TypeSecret   Type = "secret"
	TypeEnum     Type = "enum"
)

// Item is one registry entry.
type Item struct {
	Key   string
	Group string
	Label string
	Desc  string
	Type  Type
	// Default is the canonical built-in value (layer 3).
	Default string
	// Editable: the admin API accepts DB overrides for this item.
	Editable bool
	// Restart: an override applies only after a process restart.
	Restart bool
	// Sensitive: the value is a secret. Snapshots never carry it; the
	// API reports only whether one is set.
	Sensitive bool
	// Options lists the allowed values for TypeEnum.
	Options []string
	// Min/Max bound numeric types (durations in seconds). Nil = open.
	Min, Max *float64
	// MaxLen caps string/secret length and per-entry list length.
	MaxLen int
	// MaxItems caps list length.
	MaxItems int
}

// Group is one settings-page section, in display order.
type Group struct {
	Key   string
	Label string
}

// Storage is the persistence the Manager needs; *store.Store
// implements it.
type Storage interface {
	LoadSettings(ctx context.Context) (map[string]string, error)
	SaveSettings(ctx context.Context, set map[string]string, del []string, ts int64) error
}

// Manager resolves effective values from the three layers and owns
// the DB override layer. Safe for concurrent use.
type Manager struct {
	mu        sync.RWMutex
	base      map[string]string // env folded with defaults, canonical
	fromEnv   map[string]bool   // key -> the base value came from the environment
	overrides map[string]string // DB layer, canonical, validated
	storage   Storage
	version   int64 // bumped on every accepted Update; consumers poll it
	onChange  func(keys []string)
	logger    *log.Logger
}

// New builds a Manager over the given base layer and loads the DB
// overrides from storage. Overrides that fail validation (written by
// an older/newer binary, or by hand) are ignored with a log line
// rather than breaking startup.
func New(base map[string]string, fromEnv map[string]bool, storage Storage, logger *log.Logger) (*Manager, error) {
	if logger == nil {
		logger = log.Default()
	}
	m := &Manager{
		base:      make(map[string]string, len(Registry)),
		fromEnv:   fromEnv,
		overrides: map[string]string{},
		storage:   storage,
		logger:    logger,
	}
	for _, it := range Registry {
		v, ok := base[it.Key]
		if !ok || v == "" && it.Type != TypeString && it.Type != TypeSecret && it.Type != TypeList {
			// An empty base for a typed value means "not supplied":
			// fall back to the registry default so getters always parse.
			// (Strings, secrets and lists legitimately use "".)
			v = it.Default
		}
		if err := validate(it, v); err != nil {
			logger.Printf("settings: base value for %s invalid (%v); using default %q", it.Key, err, it.Default)
			v = it.Default
		}
		m.base[it.Key] = canonical(it, v)
	}
	if storage != nil {
		loaded, err := storage.LoadSettings(context.Background())
		if err != nil {
			return nil, fmt.Errorf("settings: load overrides: %w", err)
		}
		for k, v := range loaded {
			it, ok := ByKey(k)
			if !ok || !it.Editable {
				logger.Printf("settings: ignoring stored override for %s (unknown or not editable)", k)
				continue
			}
			if err := validate(it, v); err != nil {
				logger.Printf("settings: ignoring stored override for %s: %v", k, err)
				continue
			}
			m.overrides[k] = canonical(it, v)
		}
	}
	return m, nil
}

// SetOnChange registers the push hook fired after every accepted
// Update with the keys whose effective value changed. Consumers that
// cannot cheaply poll (package-level state like the content filter)
// hang off this; polling consumers use Version().
func (m *Manager) SetOnChange(fn func(keys []string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onChange = fn
}

// Version is a generation counter for the override layer.
func (m *Manager) Version() int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.version
}

// Get returns the effective canonical value of a registry key.
func (m *Manager) Get(key string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if v, ok := m.overrides[key]; ok {
		return v
	}
	return m.base[key]
}

// Source reports which layer supplies the effective value:
// "db", "env" or "default".
func (m *Manager) Source(key string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if _, ok := m.overrides[key]; ok {
		return "db"
	}
	if m.fromEnv[key] {
		return "env"
	}
	return "default"
}

// Typed getters. Values were validated on entry, so a parse failure
// here means registry corruption; the item default is the fallback.

func (m *Manager) Bool(key string) bool {
	b, err := strconv.ParseBool(m.Get(key))
	if err != nil {
		it, _ := ByKey(key)
		b, _ = strconv.ParseBool(it.Default)
	}
	return b
}

func (m *Manager) Int(key string) int {
	n, err := strconv.Atoi(m.Get(key))
	if err != nil {
		it, _ := ByKey(key)
		n, _ = strconv.Atoi(it.Default)
	}
	return n
}

func (m *Manager) Int64(key string) int64 {
	n, err := strconv.ParseInt(m.Get(key), 10, 64)
	if err != nil {
		it, _ := ByKey(key)
		n, _ = strconv.ParseInt(it.Default, 10, 64)
	}
	return n
}

func (m *Manager) Float(key string) float64 {
	f, err := strconv.ParseFloat(m.Get(key), 64)
	if err != nil {
		it, _ := ByKey(key)
		f, _ = strconv.ParseFloat(it.Default, 64)
	}
	return f
}

func (m *Manager) Duration(key string) time.Duration {
	d, err := time.ParseDuration(m.Get(key))
	if err != nil {
		it, _ := ByKey(key)
		d, _ = time.ParseDuration(it.Default)
	}
	return d
}

// List splits the canonical comma-joined value.
func (m *Manager) List(key string) []string {
	return splitList(m.Get(key))
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// --- snapshot (admin GET) ---

// ItemState is one setting as the admin API presents it. Sensitive
// values are never included: Value is empty and Set reports presence.
type ItemState struct {
	Key       string   `json:"key"`
	Label     string   `json:"label"`
	Desc      string   `json:"desc,omitempty"`
	Type      Type     `json:"type"`
	Value     string   `json:"value"`
	Set       bool     `json:"set,omitempty"`
	Source    string   `json:"source"`
	Editable  bool     `json:"editable"`
	Restart   bool     `json:"restart"`
	Sensitive bool     `json:"sensitive"`
	Options   []string `json:"options,omitempty"`
}

// GroupState is one settings-page section with its items.
type GroupState struct {
	Key   string      `json:"key"`
	Label string      `json:"label"`
	Items []ItemState `json:"items"`
}

// Snapshot returns the full configuration, grouped, with sensitive
// values masked.
func (m *Manager) Snapshot() []GroupState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]GroupState, 0, len(Groups))
	for _, g := range Groups {
		gs := GroupState{Key: g.Key, Label: g.Label}
		for _, it := range Registry {
			if it.Group != g.Key {
				continue
			}
			st := ItemState{
				Key: it.Key, Label: it.Label, Desc: it.Desc, Type: it.Type,
				Editable: it.Editable, Restart: it.Restart, Sensitive: it.Sensitive,
				Options: it.Options,
			}
			eff := m.effectiveLocked(it.Key)
			if it.Sensitive {
				st.Set = eff != ""
			} else {
				st.Value = eff
			}
			st.Source = m.sourceLocked(it.Key)
			gs.Items = append(gs.Items, st)
		}
		out = append(out, gs)
	}
	return out
}

func (m *Manager) effectiveLocked(key string) string {
	if v, ok := m.overrides[key]; ok {
		return v
	}
	return m.base[key]
}

func (m *Manager) sourceLocked(key string) string {
	if _, ok := m.overrides[key]; ok {
		return "db"
	}
	if m.fromEnv[key] {
		return "env"
	}
	return "default"
}

// --- update (admin PUT) ---

// UpdateError carries per-key validation failures. An Update that
// returns one has applied nothing.
type UpdateError struct {
	Errors map[string]string
}

func (e *UpdateError) Error() string {
	keys := make([]string, 0, len(e.Errors))
	for k := range e.Errors {
		keys = append(keys, k)
	}
	return fmt.Sprintf("invalid settings: %s", strings.Join(keys, ", "))
}

// MaskedPlaceholder is what the UI sends back for a sensitive field it
// did not touch; it (and the empty string) means "leave unchanged".
const MaskedPlaceholder = "********"

// Update validates and applies a batch of override changes. A nil
// value deletes the override (revert to env/default); a non-nil value
// sets it. The batch is atomic: any validation error rejects all of
// it. Returns the keys that took effect live and the keys that need a
// restart, plus the full list of changed keys via OnChange.
func (m *Manager) Update(values map[string]*string) (applied, restart []string, err error) {
	errs := map[string]string{}
	type change struct {
		it    Item
		value *string // canonical; nil = delete
	}
	var changes []change
	for key, raw := range values {
		it, ok := ByKey(key)
		if !ok {
			errs[key] = "unknown setting"
			continue
		}
		if !it.Editable {
			errs[key] = "setting is read-only"
			continue
		}
		if raw == nil {
			changes = append(changes, change{it, nil})
			continue
		}
		v := *raw
		if it.Sensitive && (v == "" || v == MaskedPlaceholder) {
			continue // untouched secret field, not a change
		}
		if verr := validate(it, v); verr != nil {
			errs[key] = verr.Error()
			continue
		}
		cv := canonical(it, v)
		changes = append(changes, change{it, &cv})
	}
	if len(errs) > 0 {
		return nil, nil, &UpdateError{Errors: errs}
	}

	m.mu.Lock()
	set := map[string]string{}
	var del []string
	var changed []string
	for _, c := range changes {
		cur, has := m.overrides[c.it.Key]
		if c.value == nil {
			if has {
				del = append(del, c.it.Key)
				changed = append(changed, c.it.Key)
			}
			continue
		}
		if has && cur == *c.value {
			continue // no-op
		}
		set[c.it.Key] = *c.value
		changed = append(changed, c.it.Key)
	}
	if len(changed) == 0 {
		m.mu.Unlock()
		return nil, nil, nil
	}
	if m.storage != nil {
		if serr := m.storage.SaveSettings(context.Background(), set, del, time.Now().Unix()); serr != nil {
			m.mu.Unlock()
			return nil, nil, fmt.Errorf("settings: save overrides: %w", serr)
		}
	}
	for k, v := range set {
		m.overrides[k] = v
	}
	for _, k := range del {
		delete(m.overrides, k)
	}
	m.version++
	onChange := m.onChange
	for _, k := range changed {
		if it, _ := ByKey(k); it.Restart {
			restart = append(restart, k)
		} else {
			applied = append(applied, k)
		}
	}
	m.mu.Unlock()

	if onChange != nil {
		onChange(changed)
	}
	return applied, restart, nil
}

// --- validation & canonicalization ---

// canonical normalizes a validated value to the one spelling every
// layer stores (durations via String(), lists comma-joined, bools
// via FormatBool, numbers re-rendered).
func canonical(it Item, v string) string {
	switch it.Type {
	case TypeBool:
		if b, err := strconv.ParseBool(strings.TrimSpace(v)); err == nil {
			return strconv.FormatBool(b)
		}
	case TypeInt:
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return strconv.Itoa(n)
		}
	case TypeInt64:
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
			return strconv.FormatInt(n, 10)
		}
	case TypeFloat:
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			return strconv.FormatFloat(f, 'f', -1, 64)
		}
	case TypeDuration:
		if d, err := time.ParseDuration(strings.TrimSpace(v)); err == nil {
			return d.String()
		}
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return (time.Duration(n) * time.Second).String()
		}
	case TypeList:
		return strings.Join(splitList(v), ",")
	case TypeString, TypeSecret, TypeEnum:
		return strings.TrimSpace(v)
	}
	return v
}

func f64(v float64) *float64 { return &v }

func validate(it Item, v string) error {
	v = strings.TrimSpace(v)
	switch it.Type {
	case TypeBool:
		if _, err := strconv.ParseBool(v); err != nil {
			return fmt.Errorf("must be true or false")
		}
	case TypeInt, TypeInt64:
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return fmt.Errorf("must be an integer")
		}
		if err := checkRange(it, float64(n)); err != nil {
			return err
		}
	case TypeFloat:
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return fmt.Errorf("must be a number")
		}
		if err := checkRange(it, f); err != nil {
			return err
		}
	case TypeDuration:
		d, err := time.ParseDuration(v)
		if err != nil {
			// Accept bare seconds, matching the env parser in main.
			if n, nerr := strconv.Atoi(v); nerr == nil {
				d = time.Duration(n) * time.Second
			} else {
				return fmt.Errorf("must be a duration like 45s or 2m")
			}
		}
		if err := checkRange(it, d.Seconds()); err != nil {
			return err
		}
	case TypeEnum:
		for _, o := range it.Options {
			if v == o {
				return nil
			}
		}
		return fmt.Errorf("must be one of: %s", strings.Join(it.Options, ", "))
	case TypeString, TypeSecret:
		maxLen := it.MaxLen
		if maxLen <= 0 {
			maxLen = 2048
		}
		if len(v) > maxLen {
			return fmt.Errorf("too long (max %d characters)", maxLen)
		}
		if strings.ContainsAny(v, "\x00\r\n") {
			return fmt.Errorf("must not contain control characters")
		}
		return validateStringKey(it.Key, v)
	case TypeList:
		items := splitList(v)
		maxItems := it.MaxItems
		if maxItems <= 0 {
			maxItems = 256
		}
		if len(items) > maxItems {
			return fmt.Errorf("too many entries (max %d)", maxItems)
		}
		maxLen := it.MaxLen
		if maxLen <= 0 {
			maxLen = 512
		}
		for _, e := range items {
			if len(e) > maxLen {
				return fmt.Errorf("entry too long (max %d characters)", maxLen)
			}
			if strings.ContainsAny(e, "\x00\r\n") {
				return fmt.Errorf("entries must not contain control characters")
			}
			if err := validateListEntry(it.Key, e); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkRange(it Item, f float64) error {
	if it.Min != nil && f < *it.Min {
		return fmt.Errorf("must be >= %v", *it.Min)
	}
	if it.Max != nil && f > *it.Max {
		return fmt.Errorf("must be <= %v", *it.Max)
	}
	return nil
}

// validateStringKey holds the per-key rules a plain type check cannot
// express.
func validateStringKey(key, v string) error {
	switch key {
	case KeySiteName:
		if v == "" {
			return fmt.Errorf("must not be empty")
		}
	case KeyOpenAIBaseURL, KeyFeedsKeywordURL:
		if v == "" {
			return nil
		}
		u, err := url.Parse(v)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("must be an http(s) URL")
		}
		if key == KeyFeedsKeywordURL && !strings.Contains(v, "%s") {
			return fmt.Errorf("must contain %%s for the keyword")
		}
	}
	return nil
}

func validateListEntry(key, e string) error {
	switch key {
	case KeyTrackers:
		if !strings.Contains(e, "://") {
			return fmt.Errorf("tracker %q is not an announce URL", e)
		}
	case KeyFeeds:
		u, err := url.Parse(e)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("feed %q is not an http(s) URL", e)
		}
	}
	return nil
}
