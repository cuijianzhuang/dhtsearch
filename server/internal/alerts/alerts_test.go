package alerts

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"dhtsearch/server/internal/watchdog"
)

// recorder is a fake channel endpoint: it captures every request
// body and answers with a scripted status/body. gate, when set,
// holds each response until the test closes it.
type recorder struct {
	mu     sync.Mutex
	bodies []string
	paths  []string
	status int
	body   string
	gate   chan struct{}
}

func (r *recorder) handler(w http.ResponseWriter, req *http.Request) {
	raw, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	r.bodies = append(r.bodies, string(raw))
	r.paths = append(r.paths, req.URL.Path)
	r.mu.Unlock()
	if r.gate != nil {
		<-r.gate
	}
	status := r.status
	if status == 0 {
		status = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, r.body)
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.bodies)
}

func (r *recorder) bodyAt(i int) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.bodies[i]
}

func (r *recorder) pathAt(i int) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.paths[i]
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func newService(t *testing.T, cfg Config) *Service {
	t.Helper()
	if cfg.Logger == nil {
		cfg.Logger = log.New(io.Discard, "", 0)
	}
	s := New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go s.Run(ctx)
	return s
}

var stallEvent = watchdog.Event{
	Kind:           watchdog.EventStalled,
	Since:          time.Date(2026, 11, 21, 3, 0, 0, 0, time.UTC),
	StalledFor:     15 * time.Minute,
	SeenAtProgress: 1000,
	Probe:          watchdog.Probe{Seen: 42000, Fetched: 900, QueueDepth: 16000},
}

func recoveredEvent() watchdog.Event {
	return watchdog.Event{
		Kind:       watchdog.EventRecovered,
		Since:      stallEvent.Since,
		StalledFor: 22 * time.Minute,
		Probe:      watchdog.Probe{Seen: 50000, Fetched: 950, QueueDepth: 15000},
	}
}

// The webhook receives exactly the documented JSON shape.
func TestWebhookPayloadShape(t *testing.T) {
	rec := &recorder{}
	ts := httptest.NewServer(http.HandlerFunc(rec.handler))
	defer ts.Close()
	s := newService(t, Config{Enabled: true, WebhookURL: ts.URL})

	s.NotifyEvent(stallEvent)
	waitFor(t, "webhook delivery", func() bool { return rec.count() == 1 })

	var payload struct {
		Title string `json:"title"`
		Text  string `json:"text"`
		TS    int64  `json:"ts"`
	}
	if err := json.Unmarshal([]byte(rec.bodyAt(0)), &payload); err != nil {
		t.Fatalf("payload not JSON: %v (%s)", err, rec.bodyAt(0))
	}
	if !strings.Contains(payload.Title, "停滞") {
		t.Fatalf("title = %q, want a stall title", payload.Title)
	}
	if !strings.Contains(payload.Text, "15 分钟") || !strings.Contains(payload.Text, "队列深度 16000") {
		t.Fatalf("text lacks the stall facts: %q", payload.Text)
	}
	if payload.TS <= 0 {
		t.Fatalf("ts = %d, want unix seconds", payload.TS)
	}
}

// Telegram goes to the Bot API path for the configured token,
// carrying chat_id and a combined text; the base URL override is
// what points it at the test server.
func TestTelegramSendMessage(t *testing.T) {
	rec := &recorder{body: `{"ok":true}`}
	ts := httptest.NewServer(http.HandlerFunc(rec.handler))
	defer ts.Close()
	s := newService(t, Config{
		Enabled:         true,
		TelegramToken:   "123:abc",
		TelegramChatID:  "-100999",
		TelegramBaseURL: ts.URL,
	})

	s.NotifyEvent(stallEvent)
	waitFor(t, "telegram delivery", func() bool { return rec.count() == 1 })

	if got := rec.pathAt(0); got != "/bot123:abc/sendMessage" {
		t.Fatalf("path = %q, want /bot123:abc/sendMessage", got)
	}
	var req struct {
		ChatID string `json:"chat_id"`
		Text   string `json:"text"`
	}
	if err := json.Unmarshal([]byte(rec.bodyAt(0)), &req); err != nil {
		t.Fatal(err)
	}
	if req.ChatID != "-100999" {
		t.Fatalf("chat_id = %q", req.ChatID)
	}
	if !strings.Contains(req.Text, "停滞") {
		t.Fatalf("text = %q", req.Text)
	}
}

// A Telegram ok:false answer is a delivery failure, surfaced by
// Test with the API's description.
func TestTelegramFailureSurfaced(t *testing.T) {
	rec := &recorder{body: `{"ok":false,"description":"chat not found"}`}
	ts := httptest.NewServer(http.HandlerFunc(rec.handler))
	defer ts.Close()
	s := newService(t, Config{
		Enabled:         false, // Test ignores the master switch by design
		TelegramToken:   "123:abc",
		TelegramChatID:  "42",
		TelegramBaseURL: ts.URL,
	})
	res := s.Test(context.Background())
	if len(res.Channels) != 1 {
		t.Fatalf("channels = %v, want one telegram result", res.Channels)
	}
	r := res.Channels[0]
	if r.Channel != "telegram" || r.OK || !strings.Contains(r.Error, "chat not found") {
		t.Fatalf("result = %+v, want a telegram failure carrying the description", r)
	}
}

// Episode dedup at the service layer: a second begin inside an
// episode sends nothing, a recovery with no alerted episode sends
// nothing, and a stall after a recovery alerts again.
func TestEpisodeDedup(t *testing.T) {
	rec := &recorder{}
	ts := httptest.NewServer(http.HandlerFunc(rec.handler))
	defer ts.Close()
	s := newService(t, Config{Enabled: true, WebhookURL: ts.URL})

	s.NotifyEvent(recoveredEvent()) // never alerted: dropped
	s.NotifyEvent(stallEvent)
	waitFor(t, "stall alert", func() bool { return rec.count() == 1 })

	s.NotifyEvent(stallEvent) // duplicate begin: dropped
	s.NotifyEvent(watchdog.Event{Kind: watchdog.EventFrozen, Since: stallEvent.Since, Probe: stallEvent.Probe})
	s.NotifyEvent(recoveredEvent())
	waitFor(t, "recovery alert", func() bool { return rec.count() == 2 })

	s.NotifyEvent(recoveredEvent()) // duplicate recovery: dropped
	s.NotifyEvent(stallEvent)       // new episode: alerts again
	waitFor(t, "second episode alert", func() bool { return rec.count() == 3 })

	if !strings.Contains(rec.bodyAt(1), "恢复") {
		t.Fatalf("second delivery should be the recovery, got %s", rec.bodyAt(1))
	}
}

// Disabled means nothing is sent and no episode state accrues: a
// stall seen while disabled does not make a later recovery send.
func TestDisabledSendsNothing(t *testing.T) {
	rec := &recorder{}
	ts := httptest.NewServer(http.HandlerFunc(rec.handler))
	defer ts.Close()
	on := false
	s := newService(t, Config{ConfigFunc: func() Config {
		return Config{Enabled: on, WebhookURL: ts.URL, Logger: log.New(io.Discard, "", 0)}
	}})

	s.NotifyEvent(stallEvent)
	on = true
	s.NotifyEvent(recoveredEvent())
	time.Sleep(150 * time.Millisecond)
	if rec.count() != 0 {
		t.Fatalf("deliveries = %d, want 0", rec.count())
	}
}

// The exit flush: with the episode alert still in flight behind a
// gated endpoint, FlushBeforeExit must not return until that
// alert has been delivered, and the exit notice follows it.
func TestFlushBeforeExitDeliversStallFirst(t *testing.T) {
	rec := &recorder{gate: make(chan struct{})}
	ts := httptest.NewServer(http.HandlerFunc(rec.handler))
	defer ts.Close()
	s := newService(t, Config{Enabled: true, WebhookURL: ts.URL, ExitBudget: 5 * time.Second})

	s.NotifyEvent(stallEvent)
	waitFor(t, "stall request in flight", func() bool { return rec.count() == 1 })

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.FlushBeforeExit(watchdog.Event{
			Kind:       watchdog.EventExiting,
			Since:      stallEvent.Since,
			StalledFor: 30 * time.Minute,
			Probe:      stallEvent.Probe,
		})
	}()
	select {
	case <-done:
		t.Fatal("FlushBeforeExit returned while the stall alert was still gated")
	case <-time.After(200 * time.Millisecond):
	}
	close(rec.gate) // let the stall POST complete; the exit notice follows
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("FlushBeforeExit did not return after the gate opened")
	}
	if rec.count() != 2 {
		t.Fatalf("deliveries = %d, want stall + exit notice", rec.count())
	}
	if !strings.Contains(rec.bodyAt(0), "停滞") || !strings.Contains(rec.bodyAt(1), "退出自愈") {
		t.Fatalf("order wrong: [0]=%s [1]=%s", rec.bodyAt(0), rec.bodyAt(1))
	}
}

// The flush is bounded: a channel that never answers cannot hold
// the process hostage past the exit budget.
func TestFlushBeforeExitBounded(t *testing.T) {
	rec := &recorder{gate: make(chan struct{})} // never opened
	ts := httptest.NewServer(http.HandlerFunc(rec.handler))
	defer ts.Close()
	s := newService(t, Config{Enabled: true, WebhookURL: ts.URL, ExitBudget: 300 * time.Millisecond})

	s.NotifyEvent(stallEvent)
	waitFor(t, "stall request in flight", func() bool { return rec.count() == 1 })

	start := time.Now()
	s.FlushBeforeExit(watchdog.Event{Kind: watchdog.EventExiting, Since: stallEvent.Since, StalledFor: 30 * time.Minute, Probe: stallEvent.Probe})
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("FlushBeforeExit took %v, want it bounded near the 300ms budget", elapsed)
	}
	close(rec.gate) // release the parked handler so the server can close
}

// Test reports per-channel outcomes across both channels at once,
// including the failing one.
func TestTestEndpointResults(t *testing.T) {
	bad := &recorder{status: http.StatusInternalServerError}
	badTS := httptest.NewServer(http.HandlerFunc(bad.handler))
	defer badTS.Close()
	good := &recorder{body: `{"ok":true}`}
	goodTS := httptest.NewServer(http.HandlerFunc(good.handler))
	defer goodTS.Close()

	s := newService(t, Config{
		Enabled:         true,
		WebhookURL:      badTS.URL,
		TelegramToken:   "t",
		TelegramChatID:  "1",
		TelegramBaseURL: goodTS.URL,
	})
	res := s.Test(context.Background())
	if len(res.Channels) != 2 {
		t.Fatalf("channels = %v, want webhook + telegram", res.Channels)
	}
	if res.Channels[0].Channel != "webhook" || res.Channels[0].OK {
		t.Fatalf("webhook result = %+v, want a failure", res.Channels[0])
	}
	if res.Channels[1].Channel != "telegram" || !res.Channels[1].OK {
		t.Fatalf("telegram result = %+v, want success", res.Channels[1])
	}
}

// With nothing configured, Test yields no channels (the API layer
// turns that into its 400) and Configured agrees.
func TestTestNoChannels(t *testing.T) {
	s := newService(t, Config{Enabled: true})
	if s.Configured() {
		t.Fatal("Configured() = true with no channels set")
	}
	if res := s.Test(context.Background()); len(res.Channels) != 0 {
		t.Fatalf("channels = %v, want none", res.Channels)
	}
}
