package settings

import (
	"errors"
	"testing"
)

// fakeStore is an in-memory Store.
type fakeStore struct {
	vals    map[string]string
	setErr  error
	loadErr error
	writes  int
}

func newFake() *fakeStore { return &fakeStore{vals: map[string]string{}} }

func (f *fakeStore) Settings() (map[string]string, error) {
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	out := map[string]string{}
	for k, v := range f.vals {
		out[k] = v
	}
	return out, nil
}

func (f *fakeStore) SetSetting(key, value string) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.writes++
	f.vals[key] = value
	return nil
}

// A stored value has to outrank the environment, or the console could not
// change anything durably: every restart would put the env value back.
func TestLoadPrecedence(t *testing.T) {
	db := newFake()
	db.vals[FilterAdult] = "false"

	s, err := Load(db, map[string]string{
		FilterAdult:    "true", // stored value wins over this
		MinTorrentSize: "5000", // no stored value, so this is used
		// ModerationEnabled unset in both, so the definition's default applies
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.Bool(FilterAdult) {
		t.Error("stored value lost to the environment")
	}
	if got := s.Int64(MinTorrentSize); got != 5000 {
		t.Errorf("MinTorrentSize = %d, want the env seed 5000", got)
	}
	if !s.Bool(ModerationEnabled) {
		t.Error("ModerationEnabled should fall back to its default of true")
	}
}

func TestSetPersistsAndValidates(t *testing.T) {
	db := newFake()
	s, err := Load(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set(FilterAdult, "false"); err != nil {
		t.Fatal(err)
	}
	if s.Bool(FilterAdult) {
		t.Error("in-memory value not updated")
	}
	if db.vals[FilterAdult] != "false" {
		t.Errorf("persisted %q, want \"false\"", db.vals[FilterAdult])
	}

	// Values are normalized, so the UI never has to cope with "TRUE" vs "1".
	if err := s.Set(FilterAdult, "1"); err != nil {
		t.Fatal(err)
	}
	if db.vals[FilterAdult] != "true" {
		t.Errorf("persisted %q, want the canonical \"true\"", db.vals[FilterAdult])
	}

	for _, bad := range []struct{ key, val string }{
		{FilterAdult, "yes please"},
		{MinTorrentSize, "lots"},
		{MinTorrentSize, "-1"},
		{"no_such_setting", "true"},
	} {
		if err := s.Set(bad.key, bad.val); err == nil {
			t.Errorf("Set(%q, %q) was accepted", bad.key, bad.val)
		}
	}
	// A rejected write must not have reached the database or the live value.
	if db.vals[MinTorrentSize] != "" {
		t.Errorf("invalid value was persisted: %q", db.vals[MinTorrentSize])
	}
}

// A failed write must leave the running config matching what is on disk. The
// alternative — updating memory anyway — silently diverges the two, and the
// operator is told the change was saved.
func TestSetKeepsMemoryAndDiskInStep(t *testing.T) {
	db := newFake()
	s, err := Load(db, map[string]string{FilterAdult: "true"})
	if err != nil {
		t.Fatal(err)
	}
	db.setErr = errors.New("disk full")
	if err := s.Set(FilterAdult, "false"); err == nil {
		t.Fatal("expected the write error to surface")
	}
	if !s.Bool(FilterAdult) {
		t.Error("live value changed even though the write failed")
	}
}

func TestDefsCarryCurrentValues(t *testing.T) {
	db := newFake()
	db.vals[MinTorrentSize] = "12345"
	s, err := Load(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, d := range s.Defs() {
		if d.Key != MinTorrentSize {
			continue
		}
		found = true
		if d.Value != "12345" {
			t.Errorf("Defs value = %q, want the stored 12345", d.Value)
		}
		if d.Env == "" || d.Label == "" {
			t.Error("Defs entry missing its env name or label")
		}
	}
	if !found {
		t.Error("MinTorrentSize missing from Defs")
	}
}

func TestGenChangesOnWrite(t *testing.T) {
	s, err := Load(newFake(), nil)
	if err != nil {
		t.Fatal(err)
	}
	before := s.Gen()
	if err := s.Set(ModerationDryRun, "true"); err != nil {
		t.Fatal(err)
	}
	if s.Gen() == before {
		t.Error("Gen did not change after a write")
	}
}

func TestLoadPropagatesStoreError(t *testing.T) {
	db := newFake()
	db.loadErr = errors.New("database is locked")
	if _, err := Load(db, nil); err == nil {
		t.Fatal("expected the load error to surface rather than starting with defaults")
	}
}
