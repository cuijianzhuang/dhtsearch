package settings

import "testing"

// TestRescrapeDefaults pins the re-scrape registry contract: the
// runner ships ON (read-only tracker work at a conservative rate
// — the opposite default from retention's, whose action is
// irreversible deletion), with a 48-hour minimum snapshot age and
// a 60/minute rate cap, all in the scrape group and all
// hot-editable.
func TestRescrapeDefaults(t *testing.T) {
	m := newManager(t, nil, nil, &fakeStorage{})
	if !m.Bool(KeyRescrapeEnabled) {
		t.Fatal("RESCRAPE_ENABLED must default to true")
	}
	if got := m.Int(KeyRescrapeMinAgeH); got != 48 {
		t.Fatalf("RESCRAPE_MIN_AGE_H = %d, want 48", got)
	}
	if got := m.Int(KeyRescrapePerMin); got != 60 {
		t.Fatalf("RESCRAPE_PER_MIN = %d, want 60", got)
	}
	for _, key := range []string{KeyRescrapeEnabled, KeyRescrapeMinAgeH, KeyRescrapePerMin} {
		it, ok := ByKey(key)
		if !ok {
			t.Fatalf("%s missing from the registry", key)
		}
		if it.Group != "scrape" || !it.Editable || it.Restart {
			t.Fatalf("%s: group=%q editable=%v restart=%v, want scrape/editable/hot",
				key, it.Group, it.Editable, it.Restart)
		}
	}
}
