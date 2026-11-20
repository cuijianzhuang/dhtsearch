// Package filter implements adult/spam content filtering for indexed
// torrents. A torrent is dropped when either Adult or Spam is true.
package filter

import (
	"fmt"
	"regexp"
	"strings"
	"sync/atomic"
	"unicode"
)

// File is a single file entry of a torrent.
type File struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// Result is the outcome of Check. Reasons explains every signal that hit.
type Result struct {
	Adult    bool `json:"adult"`
	Spam     bool `json:"spam"`
	TooSmall bool `json:"too_small"`
	// Blocked is the operator's own block-word list (FILTER_BLOCK_WORDS)
	// hitting. It is counted separately from Spam so the operator can
	// see what their own list is doing; it rides the spam switch.
	Blocked bool     `json:"blocked"`
	Reasons []string `json:"reasons,omitempty"`
}

// Rejected reports whether any signal fired, i.e. the torrent must not be
// indexed.
func (r Result) Rejected() bool { return r.Adult || r.Spam || r.TooSmall || r.Blocked }

// minTotalSize is the smallest torrent worth indexing. Anything below
// it is dropped as junk (fake stubs, single images, link/readme-only
// torrents). It is runtime-adjustable (MIN_TORRENT_SIZE via the
// settings system), so it lives behind atomic accessors: Check runs on
// fetch workers while the admin API may rewrite it.
var minTotalSize atomic.Int64

func init() { minTotalSize.Store(100 << 20) } // 100 MiB

// MinTotalSize returns the current size floor in bytes.
func MinTotalSize() int64 { return minTotalSize.Load() }

// SetMinTotalSize replaces the size floor; it applies to the next
// Check call.
func SetMinTotalSize(v int64) { minTotalSize.Store(v) }

// blockWords holds the operator-defined extra block list
// (FILTER_BLOCK_WORDS via the settings system): a matcher rebuilt on
// every update, swapped atomically so Check never sees a half-built
// one. The zero value is an empty matcher, which matches nothing.
var blockWords atomic.Pointer[matcher]

// SetBlockWords replaces the operator block list; it applies to the
// next Check call. Matching reuses the built-in keyword machinery
// (substring for CJK/long tokens, word boundaries for short ASCII).
func SetBlockWords(words []string) {
	m := newMatcher(words)
	blockWords.Store(&m)
}

// allowWords holds the operator-defined exception phrases
// (FILTER_ALLOW_WORDS via the settings system): each phrase is removed
// from the name and file paths before the adult signals are matched,
// the same treatment the built-in adultExceptions get, so a legitimate
// title that happens to contain a keyword can be exempted without
// touching the compiled-in lists. Allow words only ever neutralise
// adult signals — never the structural spam rules and never the
// operator's own block words.
var allowWords atomic.Pointer[[]string]

// SetAllowWords replaces the operator exception list; it applies to
// the next Check call.
func SetAllowWords(words []string) {
	out := make([]string, 0, len(words))
	for _, w := range words {
		if w = strings.ToLower(strings.TrimSpace(w)); w != "" {
			out = append(out, w)
		}
	}
	allowWords.Store(&out)
}

// adultWords is the live adult keyword matcher: the built-in
// adultKeywords list merged with the operator's additions
// (FILTER_ADULT_WORDS via the settings system). The merged matcher is
// rebuilt on every update and swapped atomically, exactly like
// blockWords, so Check never matches against a half-built list and
// never pays a merge cost per item. The built-in list is always the
// base — operator words only ever add to it.
var adultWords atomic.Pointer[matcher]

// SetAdultWords replaces the operator's adult keyword additions; it
// applies to the next Check call. Matching reuses the built-in
// keyword machinery (substring for CJK/long tokens, word boundaries
// for short ASCII), so an operator word behaves exactly like a
// built-in one.
func SetAdultWords(words []string) {
	merged := make([]string, 0, len(adultKeywords)+len(words))
	merged = append(merged, adultKeywords...)
	merged = append(merged, words...)
	m := newMatcher(merged)
	adultWords.Store(&m)
}

// userDomains is the live matcher for the operator's blocked domains
// (FILTER_BLOCK_DOMAINS via the settings system): a regex over the
// literal domains, rebuilt on every update and swapped atomically.
// A nil value means no operator domains are configured. Hits count
// as adult signals, same as the built-in adult-domain pattern.
var userDomains atomic.Pointer[regexp.Regexp]

// SetBlockDomains replaces the operator's blocked domain list; it
// applies to the next Check call. Entries are normalised (scheme,
// "www." prefix, path and trailing dot stripped, lowercased) so the
// admin can paste a bare domain or a full URL. A listed domain also
// matches its subdomains, but not unrelated domains that merely end
// with the same letters.
func SetBlockDomains(domains []string) {
	var parts []string
	seen := map[string]bool{}
	for _, d := range domains {
		d = normalizeDomain(d)
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		parts = append(parts, regexp.QuoteMeta(d))
	}
	if len(parts) == 0 {
		userDomains.Store(nil)
		return
	}
	re := regexp.MustCompile(`\b(?:www\.)?(?:` + strings.Join(parts, "|") + `)\b`)
	userDomains.Store(re)
}

// normalizeDomain reduces an operator-supplied domain entry to the
// bare lowercase host name the matcher looks for.
func normalizeDomain(d string) string {
	d = strings.ToLower(strings.TrimSpace(d))
	if i := strings.Index(d, "://"); i >= 0 {
		d = d[i+3:]
	}
	if i := strings.IndexAny(d, "/?#"); i >= 0 {
		d = d[:i]
	}
	d = strings.TrimPrefix(d, "www.")
	return strings.TrimSuffix(d, ".")
}

const (
	maxNameLen        = 300
	maxTotalSize      = 20 << 40 // 20 TiB
	maxFileCount      = 20000
	stuffingMinRepeat = 4
	nonAlnumMaxRatio  = 0.5
	randomMinLen      = 20
	randomMaxRatio    = 0.3
	randomMinFiles    = 5
)

// Config is the filter's live rule configuration: the category
// switches and the spam thresholds, pushed from the settings system
// (FILTER_* keys) by the same OnChange hook that feeds SetMinTotalSize
// and SetBlockWords. Check reads the current Config on every call, so
// a settings save applies to the very next fetched torrent.
type Config struct {
	// Enabled is the master switch: off means Check passes everything.
	Enabled bool
	// AdultEnabled gates the adult signals (keywords, domains,
	// banners, JAV codes).
	AdultEnabled bool
	// SpamEnabled gates the structural spam rules and the operator
	// block words.
	SpamEnabled bool
	// TooSmallEnabled gates the MinTotalSize floor.
	TooSmallEnabled bool
	// MaxNameLen is the longest acceptable torrent name, in runes.
	MaxNameLen int
	// StuffingMinRepeat is how often one word may repeat in the name
	// before it counts as keyword stuffing.
	StuffingMinRepeat int
	// NonAlnumMaxRatio is the highest tolerated share of
	// non-alphanumeric runes in the name.
	NonAlnumMaxRatio float64
	// RandomFileMaxRatio is the highest tolerated share of
	// random-looking file names (only judged with enough files).
	RandomFileMaxRatio float64
}

// DefaultConfig returns the historical behaviour: every category on,
// thresholds at the values the package constants have always used.
func DefaultConfig() Config {
	return Config{
		Enabled:            true,
		AdultEnabled:       true,
		SpamEnabled:        true,
		TooSmallEnabled:    true,
		MaxNameLen:         maxNameLen,
		StuffingMinRepeat:  stuffingMinRepeat,
		NonAlnumMaxRatio:   nonAlnumMaxRatio,
		RandomFileMaxRatio: randomMaxRatio,
	}
}

var liveConfig atomic.Pointer[Config]

// SetConfig replaces the live rule configuration; it applies to the
// next Check call. Non-positive thresholds fall back to the defaults
// so a bad push cannot silently disable a rule by zeroing it.
func SetConfig(c Config) {
	d := DefaultConfig()
	if c.MaxNameLen <= 0 {
		c.MaxNameLen = d.MaxNameLen
	}
	if c.StuffingMinRepeat <= 0 {
		c.StuffingMinRepeat = d.StuffingMinRepeat
	}
	if c.NonAlnumMaxRatio <= 0 {
		c.NonAlnumMaxRatio = d.NonAlnumMaxRatio
	}
	if c.RandomFileMaxRatio <= 0 {
		c.RandomFileMaxRatio = d.RandomFileMaxRatio
	}
	liveConfig.Store(&c)
}

func configNow() Config {
	if c := liveConfig.Load(); c != nil {
		return *c
	}
	return DefaultConfig()
}

var (
	adultDomainRe = regexp.MustCompile(adultDomainPattern)
	javCodeRe     = regexp.MustCompile(javCodePattern)
	javStudioRe   = regexp.MustCompile(javStudioPattern)

	// matcher for adultNameKeywords, applied to the name only.
	kwName matcher
)

// matcher matches a keyword list: plain substring search for CJK and longer
// tokens, word-boundary regex for short ASCII tokens.
type matcher struct {
	substrings []string
	wordRe     *regexp.Regexp
}

func newMatcher(keywords []string) matcher {
	var m matcher
	var shorts []string
	for _, kw := range keywords {
		kw = strings.ToLower(kw)
		if isShortASCII(kw) {
			shorts = append(shorts, regexp.QuoteMeta(kw))
		} else {
			m.substrings = append(m.substrings, kw)
		}
	}
	if len(shorts) > 0 {
		m.wordRe = regexp.MustCompile(`\b(?:` + strings.Join(shorts, "|") + `)\b`)
	}
	return m
}

// find returns the first keyword found in the lowercased text, or "".
func (m matcher) find(text string) string {
	for _, kw := range m.substrings {
		if strings.Contains(text, kw) {
			return kw
		}
	}
	if m.wordRe != nil {
		return m.wordRe.FindString(text)
	}
	return ""
}

func init() {
	kwName = newMatcher(adultNameKeywords)
	// The live adult matcher starts as the built-in list alone;
	// SetAdultWords rebuilds it with operator additions merged in.
	SetAdultWords(nil)
}

// isShortASCII reports whether s is a short pure-ASCII token for which
// substring matching would cause too many false positives.
func isShortASCII(s string) bool {
	if len(s) == 0 || len(s) > 4 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] > 127 {
			return false
		}
	}
	return true
}

// Check inspects a torrent's name, file list and total size and returns
// whether it should be dropped as adult or spam content. The live
// Config decides which categories run at all: a category whose switch
// is off never sets its flag (and so is never counted by the caller).
func Check(name string, files []File, totalSize int64) Result {
	var r Result
	cfg := configNow()
	if !cfg.Enabled {
		return r
	}
	add := func(format string, args ...interface{}) {
		r.Reasons = append(r.Reasons, fmt.Sprintf(format, args...))
	}

	lowerName := strings.ToLower(name)

	if cfg.AdultEnabled {
		// --- Adult signals ---
		// Exception phrases — the built-in list plus the operator's
		// FILTER_ALLOW_WORDS — are removed before matching, so a
		// legitimate title containing a keyword is not flagged.
		strip := adultExceptions
		if aw := allowWords.Load(); aw != nil && len(*aw) > 0 {
			strip = append(append(make([]string, 0, len(adultExceptions)+len(*aw)), adultExceptions...), *aw...)
		}
		adultHit := false
		checkAdult := func(what, text string) {
			if text == "" {
				return
			}
			for _, ex := range strip {
				text = strings.ReplaceAll(text, strings.ToLower(ex), "")
			}
			if kw := adultWords.Load().find(text); kw != "" {
				add("adult keyword %q in %s", kw, what)
				adultHit = true
				return
			}
			if m := adultDomainRe.FindString(text); m != "" {
				add("adult site domain %q in %s", m, what)
				adultHit = true
				return
			}
			// The operator's own domains (FILTER_BLOCK_DOMAINS) are
			// adult signals too, counted under adult like the
			// built-in domain pattern above.
			if re := userDomains.Load(); re != nil {
				if m := re.FindString(text); m != "" {
					add("blocked domain %q in %s", m, what)
					adultHit = true
				}
			}
		}
		checkAdult("name", lowerName)
		// Adult-forum banners count only in the name: ad files carrying the same
		// banners get bundled into legitimate uploads.
		if kw := kwName.find(lowerName); kw != "" && !adultHit {
			add("adult site banner %q in name", kw)
			adultHit = true
		}
		// A known JAV studio code is a strong signal on its own.
		if m := javStudioRe.FindString(lowerName); m != "" && !adultHit {
			add("jav studio code %q in name", m)
			adultHit = true
		}
		for _, f := range files {
			checkAdult("file", strings.ToLower(f.Path))
		}
		// A generic release-code shape is a weak signal: only flag when another
		// adult signal hit.
		if javCodeRe.MatchString(name) && adultHit {
			add("jav-style product code in name")
		}
		r.Adult = adultHit
	}

	if cfg.SpamEnabled {
		// --- Spam signals ---
		if strings.TrimSpace(name) == "" {
			add("empty name")
			r.Spam = true
		} else if !hasLetterOrDigit(name) {
			add("name contains no letters or digits")
			r.Spam = true
		}
		if runeLen(name) > cfg.MaxNameLen {
			add("name too long: %d chars", runeLen(name))
			r.Spam = true
		}
		if w, n := stuffedWord(name); n >= cfg.StuffingMinRepeat {
			add("word %q repeated %d times (keyword stuffing)", w, n)
			r.Spam = true
		}
		if ratio := nonAlnumRatio(name); ratio > cfg.NonAlnumMaxRatio {
			add("non-alphanumeric ratio %.2f exceeds %.2f", ratio, cfg.NonAlnumMaxRatio)
			r.Spam = true
		}
		if totalSize <= 0 {
			add("zero or negative total size")
			r.Spam = true
		} else if totalSize > maxTotalSize {
			add("total size %d exceeds 20 TiB", totalSize)
			r.Spam = true
		}

		// --- Operator block words ---
		// The admin-defined list (FILTER_BLOCK_WORDS): a hit in the name or
		// any file path drops the torrent. Reported as its own Blocked
		// category (counted separately from the structural spam rules
		// above), but it rides this same spam switch. Checked before the
		// size floor so the reason names the actual cause.
		if bw := blockWords.Load(); bw != nil {
			if kw := bw.find(lowerName); kw != "" {
				add("block word %q in name", kw)
				r.Blocked = true
			} else {
				for _, f := range files {
					if kw := bw.find(strings.ToLower(f.Path)); kw != "" {
						add("block word %q in file", kw)
						r.Blocked = true
						break
					}
				}
			}
		}
	}

	// --- Size floor ---
	// Distinct from spam: the torrent may be perfectly legitimate, just too
	// small to be worth indexing.
	if cfg.TooSmallEnabled {
		if floor := MinTotalSize(); totalSize > 0 && totalSize < floor {
			add("total size %d below minimum %d", totalSize, floor)
			r.TooSmall = true
		}
	}
	if cfg.SpamEnabled {
		if len(files) > maxFileCount {
			add("file count %d exceeds %d", len(files), maxFileCount)
			r.Spam = true
		}
		if len(files) >= randomMinFiles {
			n := 0
			for _, f := range files {
				if looksRandom(f.Path) {
					n++
				}
			}
			if float64(n)/float64(len(files)) > cfg.RandomFileMaxRatio {
				add("%d/%d file names look like random strings", n, len(files))
				r.Spam = true
			}
		}
	}

	return r
}

// hasLetterOrDigit reports whether s contains any letter (incl. CJK) or digit.
func hasLetterOrDigit(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

func runeLen(s string) int { return len([]rune(s)) }

// stuffedWord returns the most repeated word and its count.
func stuffedWord(s string) (string, int) {
	counts := map[string]int{}
	best, bestN := "", 0
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len([]rune(w)) < 2 {
			continue
		}
		counts[w]++
		if counts[w] > bestN {
			best, bestN = w, counts[w]
		}
	}
	return best, bestN
}

// nonAlnumRatio returns the fraction of runes that are not letters, digits
// or whitespace.
func nonAlnumRatio(s string) float64 {
	total, bad := 0, 0
	for _, r := range s {
		total++
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.IsSpace(r) {
			bad++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(bad) / float64(total)
}

// looksRandom reports whether the file's base name looks like a random
// string: 20+ consecutive alphanumerics without any vowel.
func looksRandom(path string) bool {
	base := path
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	if i := strings.LastIndex(base, "."); i > 0 {
		base = base[:i]
	}
	if len(base) < randomMinLen {
		return false
	}
	for i := 0; i < len(base); i++ {
		c := base[i]
		isAlnum := c < 128 && unicode.IsLetter(rune(c)) || unicode.IsDigit(rune(c))
		if !isAlnum {
			return false
		}
		switch c | 0x20 {
		case 'a', 'e', 'i', 'o', 'u':
			return false
		}
	}
	return true
}
