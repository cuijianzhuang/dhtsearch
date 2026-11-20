package filter

import "testing"

// Tests for the operator-defined lists: FILTER_ADULT_WORDS additions
// to the built-in adult keyword list, FILTER_BLOCK_DOMAINS additions
// to the adult domain判定， and the FILTER_BLOCK_WORDS category split
// (Blocked, counted separately from structural Spam). All three lists
// are package-global, so every test restores them via Cleanup.

func TestAdultWordsHitNameAndFile(t *testing.T) {
	withConfig(t, DefaultConfig())
	SetAdultWords([]string{"运营商词", "quuxly"})
	t.Cleanup(func() { SetAdultWords(nil) })

	// CJK addition hits as a substring in the name.
	if r := Check("某资源 运营商词 合集", mkFiles("movie.mkv"), bigSize); !r.Adult {
		t.Fatalf("adult word in name not flagged: %+v", r)
	}
	// …and in a file path.
	if r := Check("Some Movie 2024", mkFiles("Some.Movie.运营商词.mkv"), bigSize); !r.Adult {
		t.Fatalf("adult word in file path not flagged: %+v", r)
	}
	// Long ASCII addition hits as a substring, case-insensitively.
	if r := Check("QUUXLY collection 2024", mkFiles("movie.mkv"), bigSize); !r.Adult {
		t.Fatalf("adult word (case) not flagged: %+v", r)
	}
	// A clean torrent is untouched.
	if r := Check("Some Movie 2024", mkFiles("movie.mkv"), bigSize); r.Rejected() {
		t.Fatalf("clean torrent flagged: %+v", r)
	}
}

func TestAdultWordsShortASCIIUsesWordBoundaries(t *testing.T) {
	withConfig(t, DefaultConfig())
	// "qux" is a short pure-ASCII token: like a built-in short keyword
	// it must match on word boundaries only, not inside other words.
	SetAdultWords([]string{"qux"})
	t.Cleanup(func() { SetAdultWords(nil) })

	if r := Check("The Qux Show 2024", mkFiles("movie.mkv"), bigSize); !r.Adult {
		t.Fatalf("short adult word on boundary not flagged: %+v", r)
	}
	if r := Check("Quuxly Adventures 2024", mkFiles("movie.mkv"), bigSize); r.Adult {
		t.Fatalf("short adult word matched inside a longer word: %+v", r)
	}
}

func TestAdultWordsRebuildAndBuiltinSurvives(t *testing.T) {
	withConfig(t, DefaultConfig())
	t.Cleanup(func() { SetAdultWords(nil) })

	SetAdultWords([]string{"firstword"})
	if r := Check("Firstword Special 2024", mkFiles("movie.mkv"), bigSize); !r.Adult {
		t.Fatalf("first list not active: %+v", r)
	}
	// Replacing the list drops the old additions (the merged matcher
	// is rebuilt, not accumulated)…
	SetAdultWords([]string{"secondword"})
	if r := Check("Firstword Special 2024", mkFiles("movie.mkv"), bigSize); r.Adult {
		t.Fatalf("replaced adult word still active: %+v", r)
	}
	if r := Check("Secondword Special 2024", mkFiles("movie.mkv"), bigSize); !r.Adult {
		t.Fatalf("replacement adult word not active: %+v", r)
	}
	// …while the built-ins are always in the merge.
	if r := Check(adultName, nil, bigSize); !r.Adult {
		t.Fatalf("built-in keyword lost after operator list changes: %+v", r)
	}
	SetAdultWords(nil)
	if r := Check(adultName, nil, bigSize); !r.Adult {
		t.Fatalf("built-in keyword lost after clearing operator list: %+v", r)
	}
}

func TestAdultWordsRideAdultSwitch(t *testing.T) {
	c := DefaultConfig()
	c.AdultEnabled = false
	withConfig(t, c)
	SetAdultWords([]string{"运营商词"})
	SetBlockDomains([]string{"badexample.com"})
	t.Cleanup(func() { SetAdultWords(nil); SetBlockDomains(nil) })

	if r := Check("某资源 运营商词 合集", mkFiles("movie.mkv"), bigSize); r.Adult || r.Rejected() {
		t.Fatalf("adult off: operator adult word flagged: %+v", r)
	}
	if r := Check("Movie badexample.com 2024", mkFiles("movie.mkv"), bigSize); r.Adult || r.Rejected() {
		t.Fatalf("adult off: operator domain flagged: %+v", r)
	}
}

func TestBlockDomainsHitNameFileAndSubdomain(t *testing.T) {
	withConfig(t, DefaultConfig())
	SetBlockDomains([]string{"badexample.com"})
	t.Cleanup(func() { SetBlockDomains(nil) })

	if r := Check("Great Movie badexample.com 2024", mkFiles("movie.mkv"), bigSize); !r.Adult {
		t.Fatalf("blocked domain in name not flagged: %+v", r)
	}
	if r := Check("Great Movie 2024", mkFiles("www.badexample.com/movie.mkv"), bigSize); !r.Adult {
		t.Fatalf("blocked domain in file path not flagged: %+v", r)
	}
	// Subdomains of a listed domain are covered.
	if r := Check("Great Movie cdn.badexample.com 2024", mkFiles("movie.mkv"), bigSize); !r.Adult {
		t.Fatalf("subdomain of blocked domain not flagged: %+v", r)
	}
	// A domain that merely ends with the same letters is not a hit.
	if r := Check("Great Movie notbadexample.com 2024", mkFiles("movie.mkv"), bigSize); r.Adult {
		t.Fatalf("look-alike domain flagged: %+v", r)
	}
	if r := Check("Great Movie 2024", mkFiles("movie.mkv"), bigSize); r.Rejected() {
		t.Fatalf("clean torrent flagged: %+v", r)
	}
}

func TestBlockDomainsNormalisation(t *testing.T) {
	withConfig(t, DefaultConfig())
	// Pasted as a full URL with a www. prefix and a path: still works.
	SetBlockDomains([]string{"https://www.PasteExample.com/some/path"})
	t.Cleanup(func() { SetBlockDomains(nil) })

	if r := Check("Great Movie pasteexample.com 2024", mkFiles("movie.mkv"), bigSize); !r.Adult {
		t.Fatalf("normalised domain not flagged: %+v", r)
	}
	// Clearing the list disables it again.
	SetBlockDomains(nil)
	if r := Check("Great Movie pasteexample.com 2024", mkFiles("movie.mkv"), bigSize); r.Adult {
		t.Fatalf("domain still active after clearing: %+v", r)
	}
}

func TestBlockWordsOwnCategory(t *testing.T) {
	withConfig(t, DefaultConfig())
	SetBlockWords([]string{"运营商屏蔽"})
	t.Cleanup(func() { SetBlockWords(nil) })

	// A block-word hit is Blocked — not Spam, not Adult — so the
	// counters can attribute it to the operator's own list.
	r := Check("资源 运营商屏蔽 合集", mkFiles("movie.mkv"), bigSize)
	if !r.Blocked || r.Spam || r.Adult || !r.Rejected() {
		t.Fatalf("block word in name: want Blocked only, got %+v", r)
	}
	r = Check("Some Movie 2024", mkFiles("Some.Movie.运营商屏蔽.mkv"), bigSize)
	if !r.Blocked || r.Spam || r.Adult {
		t.Fatalf("block word in file: want Blocked only, got %+v", r)
	}
	// Structural spam still reports as Spam, distinctly.
	if r := Check(stuffedName, nil, bigSize); !r.Spam || r.Blocked {
		t.Fatalf("stuffed name: want Spam only, got %+v", r)
	}
}

func TestAllowWordsExemptOperatorAdultWords(t *testing.T) {
	withConfig(t, DefaultConfig())
	SetAdultWords([]string{"夜校"})
	SetAllowWords([]string{"夜校教育"})
	t.Cleanup(func() { SetAdultWords(nil); SetAllowWords(nil) })

	// The allow phrase is stripped before matching, so the operator's
	// own adult word inside it is exempted — the same treatment the
	// built-in exceptions get.
	if r := Check("夜校教育纪录片 2024", mkFiles("movie.mkv"), bigSize); r.Adult {
		t.Fatalf("allow phrase did not exempt operator adult word: %+v", r)
	}
	// The bare word outside the phrase still hits.
	if r := Check("深夜夜校合集", mkFiles("movie.mkv"), bigSize); !r.Adult {
		t.Fatalf("operator adult word outside allow phrase not flagged: %+v", r)
	}
}
