package settings

import (
	"context"
	"errors"
	"io"
	"log"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeStorage is an in-memory Storage that also records saves.
type fakeStorage struct {
	mu   sync.Mutex
	data map[string]string
}

func (f *fakeStorage) LoadSettings(context.Context) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]string{}
	for k, v := range f.data {
		out[k] = v
	}
	return out, nil
}

func (f *fakeStorage) SaveSettings(_ context.Context, set map[string]string, del []string, _ int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.data == nil {
		f.data = map[string]string{}
	}
	for k, v := range set {
		f.data[k] = v
	}
	for _, k := range del {
		delete(f.data, k)
	}
	return nil
}

func newManager(t *testing.T, base map[string]string, fromEnv map[string]bool, st Storage) *Manager {
	t.Helper()
	m, err := New(base, fromEnv, st, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func strptr(s string) *string { return &s }

func TestPrecedenceDefaultEnvDB(t *testing.T) {
	st := &fakeStorage{}
	m := newManager(t, map[string]string{
		KeyDHTSamplers: "9", // env layer
	}, map[string]bool{KeyDHTSamplers: true}, st)

	if got := m.Int(KeyMetaWorkers); got != 128 {
		t.Fatalf("default layer: META_WORKERS = %d, want 128", got)
	}
	if got := m.Source(KeyMetaWorkers); got != "default" {
		t.Fatalf("source = %s, want default", got)
	}
	if got := m.Int(KeyDHTSamplers); got != 9 || m.Source(KeyDHTSamplers) != "env" {
		t.Fatalf("env layer: samplers = %d source %s, want 9/env", got, m.Source(KeyDHTSamplers))
	}

	applied, restart, err := m.Update(map[string]*string{KeyDHTSamplers: strptr("3")})
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Int(KeyDHTSamplers); got != 3 || m.Source(KeyDHTSamplers) != "db" {
		t.Fatalf("db layer: samplers = %d source %s, want 3/db", got, m.Source(KeyDHTSamplers))
	}
	if len(restart) != 1 || restart[0] != KeyDHTSamplers {
		t.Fatalf("restart = %v, want [%s]", restart, KeyDHTSamplers)
	}
	if len(applied) != 0 {
		t.Fatalf("applied = %v, want none", applied)
	}

	// Deleting the override reverts to the env layer.
	if _, _, err := m.Update(map[string]*string{KeyDHTSamplers: nil}); err != nil {
		t.Fatal(err)
	}
	if got := m.Int(KeyDHTSamplers); got != 9 || m.Source(KeyDHTSamplers) != "env" {
		t.Fatalf("after delete: samplers = %d source %s, want 9/env", got, m.Source(KeyDHTSamplers))
	}
	if st.data[KeyDHTSamplers] != "" {
		t.Fatalf("storage still holds override: %v", st.data)
	}
}

func TestUpdateValidationIsAtomic(t *testing.T) {
	st := &fakeStorage{}
	m := newManager(t, nil, nil, st)
	_, _, err := m.Update(map[string]*string{
		KeyMetaTimeout: strptr("30s"),   // valid
		KeyMetaWorkers: strptr("abc"),   // not a number
		KeyDHTSamplers: strptr("99999"), // out of range
	})
	var ue *UpdateError
	if !errors.As(err, &ue) {
		t.Fatalf("err = %v, want UpdateError", err)
	}
	if len(ue.Errors) != 2 {
		t.Fatalf("errors = %v, want 2 entries", ue.Errors)
	}
	// Nothing applied: even the valid item keeps its old value.
	if got := m.Duration(KeyMetaTimeout); got != 45*time.Second {
		t.Fatalf("META_TIMEOUT = %v after rejected batch, want 45s", got)
	}
	if len(st.data) != 0 {
		t.Fatalf("storage written on rejected batch: %v", st.data)
	}
}

func TestUpdateRejectsUnknownAndReadOnly(t *testing.T) {
	m := newManager(t, nil, nil, &fakeStorage{})
	for key, val := range map[string]string{
		"NOPE":        "1",
		KeyHTTPAddr:   ":9999", // startup item: read-only
		KeyAdminToken: "secret",
	} {
		_, _, err := m.Update(map[string]*string{key: strptr(val)})
		var ue *UpdateError
		if !errors.As(err, &ue) {
			t.Fatalf("update %s: err = %v, want UpdateError", key, err)
		}
		if ue.Errors[key] == "" {
			t.Fatalf("update %s: no per-key error in %v", key, ue.Errors)
		}
	}
}

func TestSensitiveMaskingAndPlaceholder(t *testing.T) {
	st := &fakeStorage{}
	m := newManager(t, map[string]string{KeyOpenAIAPIKey: "env-key-123"}, map[string]bool{KeyOpenAIAPIKey: true}, st)

	// Snapshot never carries the secret, only its presence.
	for _, g := range m.Snapshot() {
		for _, it := range g.Items {
			if it.Key == KeyOpenAIAPIKey {
				if it.Value != "" || !it.Set {
					t.Fatalf("snapshot leaks or misreports key: %+v", it)
				}
			}
			if strings.Contains(it.Value, "env-key-123") {
				t.Fatalf("snapshot contains secret in %s", it.Key)
			}
		}
	}

	// Placeholder and empty string both mean "unchanged".
	for _, v := range []string{"", MaskedPlaceholder} {
		applied, _, err := m.Update(map[string]*string{KeyOpenAIAPIKey: strptr(v)})
		if err != nil {
			t.Fatal(err)
		}
		if len(applied) != 0 {
			t.Fatalf("placeholder update applied %v, want no change", applied)
		}
		if got := m.Get(KeyOpenAIAPIKey); got != "env-key-123" {
			t.Fatalf("key changed by placeholder: %q", got)
		}
	}

	// A real value replaces it and takes effect live.
	applied, _, err := m.Update(map[string]*string{KeyOpenAIAPIKey: strptr("db-key-456")})
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 || m.Get(KeyOpenAIAPIKey) != "db-key-456" || m.Source(KeyOpenAIAPIKey) != "db" {
		t.Fatalf("after set: applied=%v value=%q source=%s", applied, m.Get(KeyOpenAIAPIKey), m.Source(KeyOpenAIAPIKey))
	}
}

func TestStoredOverridesValidatedAtLoad(t *testing.T) {
	st := &fakeStorage{data: map[string]string{
		KeyMetaWorkers: "not-a-number", // invalid: ignored
		KeyMetaTimeout: "30s",          // valid: honoured
		KeyHTTPAddr:    ":1",           // read-only item: ignored
		"GHOST_KEY":    "x",            // unknown: ignored
	}}
	m := newManager(t, nil, nil, st)
	if got := m.Int(KeyMetaWorkers); got != 128 {
		t.Fatalf("invalid stored override applied: META_WORKERS = %d", got)
	}
	if got := m.Duration(KeyMetaTimeout); got != 30*time.Second {
		t.Fatalf("valid stored override ignored: META_TIMEOUT = %v", got)
	}
	if got := m.Get(KeyHTTPAddr); got != ":8080" {
		t.Fatalf("read-only override applied: HTTP_ADDR = %q", got)
	}
}

func TestCanonicalForms(t *testing.T) {
	m := newManager(t, nil, nil, &fakeStorage{})
	if _, _, err := m.Update(map[string]*string{
		KeyTrackers:     strptr(" udp://a:1/x , udp://b:2/y "),
		KeyMetaTimeout:  strptr("90"), // bare seconds, like the env parser
		KeyTrendingEnabled: strptr("TRUE"),
	}); err != nil {
		t.Fatal(err)
	}
	if got := m.Get(KeyTrackers); got != "udp://a:1/x,udp://b:2/y" {
		t.Fatalf("trackers canonical = %q", got)
	}
	if got := m.Duration(KeyMetaTimeout); got != 90*time.Second {
		t.Fatalf("duration = %v", got)
	}
	if !m.Bool(KeyTrendingEnabled) {
		t.Fatal("bool canonicalization failed")
	}
}

func TestEnumAndListValidation(t *testing.T) {
	m := newManager(t, nil, nil, &fakeStorage{})
	if _, _, err := m.Update(map[string]*string{KeyThemeColor: strptr("pink")}); err == nil {
		t.Fatal("invalid enum value accepted")
	}
	if _, _, err := m.Update(map[string]*string{KeyThemeColor: strptr("violet")}); err != nil {
		t.Fatalf("valid enum value rejected: %v", err)
	}
	if _, _, err := m.Update(map[string]*string{KeyTrackers: strptr("not-a-url")}); err == nil {
		t.Fatal("tracker without scheme accepted")
	}
	if _, _, err := m.Update(map[string]*string{KeyFeeds: strptr("ftp://x/y")}); err == nil {
		t.Fatal("non-http feed accepted")
	}
	if _, _, err := m.Update(map[string]*string{KeyFeedsKeywordURL: strptr("https://x/rss")}); err == nil {
		t.Fatal("keyword URL without placeholder accepted")
	}
}
func TestRateLimitFloors(t *testing.T) {
	m := newManager(t, nil, nil, &fakeStorage{})
	// Zero is a sentinel on both knobs (limiter off / burst auto) and
	// must stay valid; positive values below the floor are the ones
	// that starved the admin UI in the field (0.01 rps, burst 1).
	for _, tc := range []struct {
		key, val string
		ok       bool
	}{
		{KeyRateLimitRPS, "0", true},
		{KeyRateLimitRPS, "0.01", false},
		{KeyRateLimitRPS, "0.09", false},
		{KeyRateLimitRPS, "0.1", true},
		{KeyRateLimitRPS, "3", true},
		{KeyRateLimitBurst, "0", true},
		{KeyRateLimitBurst, "1", false},
		{KeyRateLimitBurst, "9", false},
		{KeyRateLimitBurst, "10", true},
		{KeyRateLimitBurst, "30", true},
	} {
		_, _, err := m.Update(map[string]*string{tc.key: strptr(tc.val)})
		if tc.ok && err != nil {
			t.Errorf("%s=%s: rejected: %v", tc.key, tc.val, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%s=%s: accepted, want floor rejection", tc.key, tc.val)
		}
	}
}

// A rate-limit override stored before the floors existed (e.g. the
// 0.01/1 pair from the lockout report) must be ignored on load, not
// resurrected: the manager falls back to the base layer.
func TestRateLimitFloorHealsStoredOverride(t *testing.T) {
	st := &fakeStorage{data: map[string]string{
		KeyRateLimitRPS:   "0.01",
		KeyRateLimitBurst: "1",
	}}
	m := newManager(t, nil, nil, st)
	if got := m.Float(KeyRateLimitRPS); got != 3 {
		t.Fatalf("RPS = %v, want default 3 (poisoned override ignored)", got)
	}
	if got := m.Int(KeyRateLimitBurst); got != 30 {
		t.Fatalf("burst = %d, want default 30 (poisoned override ignored)", got)
	}
}
