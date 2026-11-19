package trending

import (
	"path/filepath"

	"context"
	"dhtsearch/server/internal/store"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stub mimics the two Douban endpoints: charts with Chinese titles, and
// per-subject abstracts carrying "中文 English‎ (year)" titles.
func stub(fail *atomic.Bool, abstractCalls *atomic.Int64) http.Handler {
	abstracts := map[string]string{
		"1": "痴迷 Obsession‎ (2025)",
		"2": "瑞克和莫蒂 第九季 Rick and Morty Season 9‎ (2026)",
		"3": "无名之辈‎ (2018)", // Chinese-only: no English name
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail != nil && fail.Load() {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/j/search_subjects":
			switch tag := r.URL.Query().Get("tag"); {
			case r.URL.Query().Get("type") == "movie":
				fmt.Fprint(w, `{"subjects":[{"id":"1","title":"痴迷"},{"id":"3","title":"无名之辈"}]}`)
			case tag == "日剧":
				fmt.Fprint(w, `{"subjects":[{"id":"4","title":"直到T恤干透"}]}`)
			case tag == "韩剧":
				fmt.Fprint(w, `{"subjects":[{"id":"5","title":"铁拳教育"}]}`)
			default:
				fmt.Fprint(w, `{"subjects":[{"id":"2","title":"瑞克和莫蒂 第九季"}]}`)
			}
		case "/j/subject_abstract":
			abstractCalls.Add(1)
			fmt.Fprintf(w, `{"subject":{"title":"%s"}}`, abstracts[r.URL.Query().Get("subject_id")])
		default:
			http.NotFound(w, r)
		}
	})
}

func TestFetchResolvesEnglishTitles(t *testing.T) {
	var fail atomic.Bool
	var calls atomic.Int64
	srv := httptest.NewServer(stub(&fail, &calls))
	defer srv.Close()

	svc := New(Config{BaseURL: srv.URL, Interval: time.Hour})
	svc.refresh(context.Background())

	snap := svc.Get()
	if got := snap.Movies; len(got) != 2 || got[0] != "Obsession" || got[1] != "无名之辈" {
		t.Fatalf("movies = %v", got)
	}
	if got := snap.TV; len(got) != 1 || got[0] != "Rick and Morty Season 9" {
		t.Fatalf("tv = %v", got)
	}
	// JP/KR charts keep Chinese titles and must not hit subject_abstract.
	if got := snap.TVJP; len(got) != 1 || got[0] != "直到T恤干透" {
		t.Fatalf("tv_jp = %v", got)
	}
	if got := snap.TVKR; len(got) != 1 || got[0] != "铁拳教育" {
		t.Fatalf("tv_kr = %v", got)
	}
	if calls.Load() != 3 {
		t.Fatalf("abstract calls = %d, want 3 (english charts only)", calls.Load())
	}
	if snap.UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt not set")
	}

	// Second refresh must serve names from the cache: no new abstract calls.
	before := calls.Load()
	svc.refresh(context.Background())
	if calls.Load() != before {
		t.Fatalf("abstract calls after cached refresh: %d -> %d", before, calls.Load())
	}

	// Upstream breaks: the previous snapshot must survive untouched.
	fail.Store(true)
	prev := svc.Get()
	svc.refresh(context.Background())
	after := svc.Get()
	if len(after.Movies) != 2 || len(after.TV) != 1 || len(after.TVJP) != 1 || len(after.TVKR) != 1 {
		t.Fatalf("snapshot lost after failed refresh: %+v", after)
	}
	if !after.UpdatedAt.Equal(prev.UpdatedAt) {
		t.Fatal("UpdatedAt advanced on a fully failed refresh")
	}
}

func TestEmptySubjectsIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"subjects":[]}`)
	}))
	defer srv.Close()

	svc := New(Config{BaseURL: srv.URL})
	if _, err := svc.fetchList(context.Background(), svc.cfgNow(), "movie", "欧美", true); err == nil {
		t.Fatal("expected error for empty subjects")
	}
}

func TestExtractEnglish(t *testing.T) {
	cases := []struct{ in, chinese, want string }{
		{"痴迷 Obsession‎ (2025)", "痴迷", "Obsession"},
		{"瑞克和莫蒂 第九季 Rick and Morty Season 9‎ (2026)", "瑞克和莫蒂 第九季", "Rick and Morty Season 9"},
		{"柯蒂斯总统 第一季 President Curtis Season 1‎ (2026)", "柯蒂斯总统 第一季", "President Curtis Season 1"},
		{"无名之辈‎ (2018)", "无名之辈", ""},     // no English name
		{"寒战1994‎ (1994)", "寒战1994", ""}, // digit-tailed Chinese title, nothing after
		{"疯狂动物城2 Zootopia 2‎ (2025)", "疯狂动物城2", "Zootopia 2"},
		{"V字仇杀队 V for Vendetta‎ (2005)", "V字仇杀队", "V for Vendetta"},
		// Chart/abstract mismatch falls back to the last-CJK heuristic.
		{"痴迷 Obsession‎ (2025)", "different", "Obsession"},
	}
	for _, c := range cases {
		if got := extractEnglish(c.in, c.chinese); got != c.want {
			t.Errorf("extractEnglish(%q, %q) = %q, want %q", c.in, c.chinese, got, c.want)
		}
	}
}

func TestReadyClosesAfterFirstRefresh(t *testing.T) {
	var fail atomic.Bool
	var calls atomic.Int64
	srv := httptest.NewServer(stub(&fail, &calls))
	defer srv.Close()
	svc := New(Config{BaseURL: srv.URL, Interval: time.Hour})
	select {
	case <-svc.Ready():
		t.Fatal("ready before any refresh")
	default:
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go svc.Run(ctx)
	select {
	case <-svc.Ready():
	case <-time.After(30 * time.Second):
		t.Fatal("ready never closed")
	}
	if len(svc.Get().TVJP) == 0 {
		t.Fatal("snapshot not populated when ready closed")
	}
}

// memStore is an in-memory StateStore for persistence tests.
type memStore struct {
	mu   sync.Mutex
	data []byte
}

func (m *memStore) LoadTrendingState(context.Context) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]byte(nil), m.data...), nil
}

func (m *memStore) SaveTrendingState(_ context.Context, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data = append([]byte(nil), data...)
	return nil
}

func TestPersistenceRoundTripAcrossRestart(t *testing.T) {
	var fail atomic.Bool
	var calls atomic.Int64
	srv := httptest.NewServer(stub(&fail, &calls))
	defer srv.Close()
	store := &memStore{}

	first := New(Config{BaseURL: srv.URL, Interval: time.Hour, Store: store})
	if ok, failed := first.refresh(context.Background()); ok != 4 || failed != 0 {
		t.Fatalf("first refresh: ok=%d failed=%d, want 4/0", ok, failed)
	}
	if len(store.data) == 0 {
		t.Fatal("refresh did not persist state")
	}

	// Simulated restart: a brand-new service over the same store must
	// serve the charts from Restore alone, before any fetch.
	second := New(Config{BaseURL: srv.URL, Interval: time.Hour, Store: store})
	second.Restore(context.Background())
	snap := second.Get()
	if len(snap.Movies) != 2 || len(snap.TV) != 1 || len(snap.TVJP) != 1 || len(snap.TVKR) != 1 {
		t.Fatalf("restored snapshot: %+v", snap)
	}
	if snap.UpdatedAt.IsZero() {
		t.Fatal("restored snapshot lost UpdatedAt")
	}

	// The name cache must have been persisted too: refreshing the
	// restored service costs no new subject_abstract calls.
	before := calls.Load()
	if _, failed := second.refresh(context.Background()); failed != 0 {
		t.Fatalf("restored refresh failed=%d", failed)
	}
	if calls.Load() != before {
		t.Fatalf("abstract calls after restored refresh: %d -> %d, name cache not persisted", before, calls.Load())
	}
}

func TestRestoreEmptyAndCorrupt(t *testing.T) {
	store := &memStore{}
	svc := New(Config{Store: store})
	svc.Restore(context.Background())
	if snap := svc.Get(); len(snap.Movies) != 0 || !snap.UpdatedAt.IsZero() {
		t.Fatalf("empty store restored a snapshot: %+v", snap)
	}

	store.data = []byte("{not json")
	svc.Restore(context.Background())
	if snap := svc.Get(); len(snap.Movies) != 0 {
		t.Fatalf("corrupt state restored a snapshot: %+v", snap)
	}
}

func TestRetryDelayBackoff(t *testing.T) {
	interval := time.Hour
	cases := []struct {
		streak int
		want   time.Duration
	}{
		{0, interval},
		{1, time.Minute},
		{2, 2 * time.Minute},
		{3, 4 * time.Minute},
		{6, 32 * time.Minute},
		{7, interval}, // capped
		{30, interval},
	}
	for _, c := range cases {
		if got := retryDelay(c.streak, interval); got != c.want {
			t.Errorf("retryDelay(%d, 1h) = %s, want %s", c.streak, got, c.want)
		}
	}
}

// TestRunRetriesAndRecovers: with Douban down at startup the service
// must retry on the backoff (not after a full interval), keep
// reporting a failing status, and populate once upstream recovers.
func TestRunRetriesAndRecovers(t *testing.T) {
	old := retryBase
	retryBase = 50 * time.Millisecond
	defer func() { retryBase = old }()

	var fail atomic.Bool
	fail.Store(true)
	var calls atomic.Int64
	srv := httptest.NewServer(stub(&fail, &calls))
	defer srv.Close()

	svc := New(Config{BaseURL: srv.URL, Interval: time.Hour})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go svc.Run(ctx)

	<-svc.Ready()
	if st := svc.Status(); !st.Failing || st.FailStreak < 1 {
		t.Fatalf("status after failed first cycle: %+v", st)
	}

	fail.Store(false)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if len(svc.Get().TVJP) == 1 && !svc.Status().Failing {
			return // recovered well before the hourly interval
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("service did not recover on backoff: snap=%+v status=%+v", svc.Get(), svc.Status())
}

// TestPersistenceWithRealStore is the end-to-end restart scenario
// against a real on-disk database: first process fetches and
// persists; second process starts with Douban unreachable and must
// still serve the charts from Restore alone.
func TestPersistenceWithRealStore(t *testing.T) {
	var fail atomic.Bool
	var calls atomic.Int64
	srv := httptest.NewServer(stub(&fail, &calls))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "restart.db")
	ctx := context.Background()

	st1, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	first := New(Config{BaseURL: srv.URL, Interval: time.Hour, Store: st1})
	if ok, failed := first.refresh(ctx); ok != 4 || failed != 0 {
		t.Fatalf("first refresh: ok=%d failed=%d, want 4/0", ok, failed)
	}
	st1.Close()

	// Restart with upstream down: fetches fail, Restore must carry.
	fail.Store(true)
	st2, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	second := New(Config{BaseURL: srv.URL, Interval: time.Hour, Store: st2})
	second.Restore(ctx)
	snap := second.Get()
	if len(snap.Movies) != 2 || len(snap.TV) != 1 || len(snap.TVJP) != 1 || len(snap.TVKR) != 1 {
		t.Fatalf("homepage would be blank after restart with Douban down: %+v", snap)
	}
	if ok, failed := second.refresh(ctx); ok != 0 || failed != 4 {
		t.Fatalf("down refresh: ok=%d failed=%d, want 0/4", ok, failed)
	}
	if got := second.Get(); len(got.Movies) != 2 {
		t.Fatalf("failed refresh blanked the restored snapshot: %+v", got)
	}

	// Upstream recovers: the next refresh updates the charts again.
	fail.Store(false)
	if ok, failed := second.refresh(ctx); ok != 4 || failed != 0 {
		t.Fatalf("recovery refresh: ok=%d failed=%d, want 4/0", ok, failed)
	}
}
