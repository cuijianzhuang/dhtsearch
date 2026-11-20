package filter

import (
	"strings"
	"testing"
)

// withConfig installs a live Config for the duration of one test and
// restores the defaults afterwards: the config is package-global, so
// every test that touches it must put it back.
func withConfig(t *testing.T, c Config) {
	t.Helper()
	SetConfig(c)
	t.Cleanup(func() { SetConfig(DefaultConfig()) })
}

const bigSize = 200 << 20 // comfortably above the default size floor

// adultName hits the built-in keyword list ("porn" is a keyword).
const adultName = "Some Porn Video 2024 1080p"

// stuffedName repeats one word five times: spam at the default
// stuffing threshold of 4, clean below it.
const stuffedName = "movie movie movie movie movie"

func TestFilterMasterSwitchOffPassesEverything(t *testing.T) {
	c := DefaultConfig()
	c.Enabled = false
	withConfig(t, c)

	if r := Check(adultName, nil, bigSize); r.Rejected() {
		t.Fatalf("master off: adult name rejected: %+v", r)
	}
	if r := Check(stuffedName, nil, bigSize); r.Rejected() {
		t.Fatalf("master off: stuffed name rejected: %+v", r)
	}
	if r := Check("Tiny Torrent", nil, 1024); r.Rejected() {
		t.Fatalf("master off: tiny torrent rejected: %+v", r)
	}
}

func TestFilterAdultSwitchOffOnlySparesAdult(t *testing.T) {
	c := DefaultConfig()
	c.AdultEnabled = false
	withConfig(t, c)

	if r := Check(adultName, nil, bigSize); r.Adult || r.Rejected() {
		t.Fatalf("adult off: adult name flagged: %+v", r)
	}
	// The other categories keep working.
	if r := Check(stuffedName, nil, bigSize); !r.Spam {
		t.Fatalf("adult off: stuffed name not flagged spam: %+v", r)
	}
	if r := Check("Tiny Torrent", nil, 1024); !r.TooSmall {
		t.Fatalf("adult off: tiny torrent not flagged too small: %+v", r)
	}
}

func TestFilterSpamSwitchOffOnlySparesSpam(t *testing.T) {
	c := DefaultConfig()
	c.SpamEnabled = false
	withConfig(t, c)
	SetBlockWords([]string{"ubuntu"})
	t.Cleanup(func() { SetBlockWords(nil) })

	if r := Check(stuffedName, nil, bigSize); r.Spam || r.Rejected() {
		t.Fatalf("spam off: stuffed name flagged: %+v", r)
	}
	// Block words ride the spam switch too.
	if r := Check("Ubuntu 24.04 LTS ISO", nil, bigSize); r.Blocked || r.Rejected() {
		t.Fatalf("spam off: block word still flagged: %+v", r)
	}
	if r := Check(adultName, nil, bigSize); !r.Adult {
		t.Fatalf("spam off: adult name not flagged: %+v", r)
	}
}

func TestFilterTooSmallSwitchOff(t *testing.T) {
	c := DefaultConfig()
	c.TooSmallEnabled = false
	withConfig(t, c)

	if r := Check("Tiny Torrent", nil, 1024); r.TooSmall || r.Rejected() {
		t.Fatalf("too-small off: tiny torrent flagged: %+v", r)
	}
}

func TestFilterThresholdChangesApply(t *testing.T) {
	// Name length: 301 runes is spam at the default 300, fine at 1000.
	longName := strings.Repeat("a", 301)
	if r := Check(longName, nil, bigSize); !r.Spam {
		t.Fatalf("default config: 301-rune name not flagged: %+v", r)
	}
	c := DefaultConfig()
	c.MaxNameLen = 1000
	withConfig(t, c)
	if r := Check(longName, nil, bigSize); r.Spam {
		t.Fatalf("MaxNameLen=1000: 301-rune name flagged: %+v", r)
	}

	// Stuffing: three repeats pass at the default 4, hit at 3.
	three := "movie movie movie"
	c = DefaultConfig()
	withConfig(t, c)
	if r := Check(three, nil, bigSize); r.Spam {
		t.Fatalf("default config: 3 repeats flagged: %+v", r)
	}
	c.StuffingMinRepeat = 3
	SetConfig(c)
	if r := Check(three, nil, bigSize); !r.Spam {
		t.Fatalf("StuffingMinRepeat=3: 3 repeats not flagged: %+v", r)
	}

	// Non-alphanumeric ratio: "abcdefg!!!" has ratio 0.3 — clean at
	// the default 0.5, spam at 0.2.
	c = DefaultConfig()
	withConfig(t, c)
	if r := Check("abcdefg!!!", nil, bigSize); r.Spam {
		t.Fatalf("default config: ratio 0.3 name flagged: %+v", r)
	}
	c.NonAlnumMaxRatio = 0.2
	SetConfig(c)
	if r := Check("abcdefg!!!", nil, bigSize); !r.Spam {
		t.Fatalf("NonAlnumMaxRatio=0.2: ratio 0.3 name not flagged: %+v", r)
	}

	// Random file names: five vowel-less 20+ char names are a 1.0
	// ratio — spam at the default 0.3, clean at 1.0 (never exceeds).
	randomFiles := []File{
		{Path: "bcdfghjklmnpqrtvwxz1.mkv", Size: bigSize},
		{Path: "bcdfghjklmnpqrtvwxz2.mkv", Size: 1},
		{Path: "bcdfghjklmnpqrtvwxz3.mkv", Size: 1},
		{Path: "bcdfghjklmnpqrtvwxz4.mkv", Size: 1},
		{Path: "bcdfghjklmnpqrtvwxz5.mkv", Size: 1},
	}
	c = DefaultConfig()
	withConfig(t, c)
	if r := Check("Random Files Release", randomFiles, bigSize); !r.Spam {
		t.Fatalf("default config: all-random files not flagged: %+v", r)
	}
	c.RandomFileMaxRatio = 1.0
	SetConfig(c)
	if r := Check("Random Files Release", randomFiles, bigSize); r.Spam {
		t.Fatalf("RandomFileMaxRatio=1.0: all-random files flagged: %+v", r)
	}
}

func TestFilterAllowWordsExemptAdultOnly(t *testing.T) {
	withConfig(t, DefaultConfig())
	t.Cleanup(func() { SetAllowWords(nil) })

	const title = "Sex Education Documentary 2024"
	if r := Check(title, nil, bigSize); !r.Adult {
		t.Fatalf("baseline: %q not flagged adult: %+v", title, r)
	}
	SetAllowWords([]string{"sex education"})
	if r := Check(title, nil, bigSize); r.Adult {
		t.Fatalf("allow word set: %q still flagged adult: %+v", title, r)
	}
	// The exemption is phrase-scoped: another adult title still hits.
	if r := Check(adultName, nil, bigSize); !r.Adult {
		t.Fatalf("allow word leaked to other titles: %+v", r)
	}
	// And it never neutralises the operator's own block words.
	SetBlockWords([]string{"ubuntu"})
	t.Cleanup(func() { SetBlockWords(nil) })
	SetAllowWords([]string{"ubuntu"})
	if r := Check("Ubuntu 24.04 LTS ISO", nil, bigSize); !r.Blocked {
		t.Fatalf("allow word neutralised a block word: %+v", r)
	}
}
