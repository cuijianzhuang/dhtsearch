package settings

import "testing"

// TestRetentionDefaults pins the retention group's registry
// contract: the runner ships OFF (deletion is irreversible — the
// operator opts in), with a 30-day minimum age and a daily
// interval, all in the retention group and all hot-editable.
func TestRetentionDefaults(t *testing.T) {
	m := newManager(t, nil, nil, &fakeStorage{})
	if m.Bool(KeyRetentionEnabled) {
		t.Fatal("RETENTION_ENABLED must default to false")
	}
	if got := m.Int(KeyRetentionMinAgeDays); got != 30 {
		t.Fatalf("RETENTION_MIN_AGE_DAYS = %d, want 30", got)
	}
	if got := m.Int(KeyRetentionIntervalH); got != 24 {
		t.Fatalf("RETENTION_INTERVAL_H = %d, want 24", got)
	}
	for _, key := range []string{KeyRetentionEnabled, KeyRetentionMinAgeDays, KeyRetentionIntervalH} {
		it, ok := ByKey(key)
		if !ok {
			t.Fatalf("%s missing from the registry", key)
		}
		if it.Group != "retention" || !it.Editable || it.Restart {
			t.Fatalf("%s: group=%q editable=%v restart=%v, want retention/editable/hot",
				key, it.Group, it.Editable, it.Restart)
		}
	}
	found := false
	for _, g := range Groups {
		if g.Key == "retention" {
			found = true
		}
	}
	if !found {
		t.Fatal("retention group missing from Groups")
	}
}
