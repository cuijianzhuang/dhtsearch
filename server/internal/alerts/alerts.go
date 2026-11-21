// Package alerts delivers the watchdog's stall and recovery
// episodes to the operator over external channels, closing the
// gap the original incident exposed: the watchdog detected,
// logged and self-healed, but the process ran stalled for ~9
// hours before a human happened to look.
//
// Detection stays in internal/watchdog — this package owns only
// delivery. The watchdog reports episode transitions through its
// OnEvent hook (begin once per episode, recovery when ingest
// resumes) and, on an escalation exit, through BeforeExit, which
// this package answers by flushing any in-flight episode alert
// and sending an exit notice synchronously, inside a bounded
// budget, before the process dies.
//
// Two channel types, both plain HTTPS POSTs:
//
//   - a generic webhook receiving JSON {title, text, ts} — the
//     shape group bots (DingTalk/Feishu/WeCom/Slack-style custom
//     webhooks) and personal automation endpoints accept;
//   - Telegram, via the Bot API's sendMessage.
//
// A send goes to every configured channel; an unconfigured
// channel is skipped. Delivery is asynchronous off a bounded
// queue so a slow or dead channel can never block the watchdog's
// tick; failures are logged, never fatal. Each channel attempt
// has a 5s timeout and at most one retry.
//
// Episode semantics are enforced twice, deliberately: the
// watchdog emits one begin event per episode, and this service
// tracks its own alerted flag, so a recovery is only sent for an
// episode that was actually alerted and a duplicate begin (say,
// alerts enabled mid-episode) cannot double-send.
package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"dhtsearch/server/internal/watchdog"
)

// requestTimeout bounds one channel attempt. exitBudget bounds
// the whole BeforeExit flush + exit-notice operation: the
// watchdog calls it synchronously on its way to os.Exit, and the
// process must not linger on a dead channel.
const (
	requestTimeout = 5 * time.Second
	retryDelay     = 200 * time.Millisecond
	exitBudget     = 2 * time.Second
	queueSize      = 16
)

// Config is the notifier's configuration. With ConfigFunc set, a
// fresh Config is taken at every event and every send, so values
// saved through the admin settings API apply without a restart.
type Config struct {
	Enabled        bool
	WebhookURL     string
	TelegramToken  string
	TelegramChatID string
	Logger         *log.Logger
	ConfigFunc     func() Config

	// TelegramBaseURL overrides the Bot API origin
	// (https://api.telegram.org); tests point it at a local server.
	TelegramBaseURL string
	// HTTPClient, when non-nil, performs the channel POSTs.
	HTTPClient *http.Client
	// ExitBudget overrides the BeforeExit bound; tests shrink it.
	ExitBudget time.Duration
}

// Message is one notification as delivered to the channels.
type Message struct {
	Title string
	Text  string
	At    time.Time
}

// ChannelResult is one channel's outcome for a send, reported by
// Test so the operator sees exactly which leg of their config is
// broken.
type ChannelResult struct {
	Channel string `json:"channel"` // "webhook" | "telegram"
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
}

// TestResult is the admin test endpoint's payload: one entry per
// configured channel. An empty Channels means nothing is
// configured at all.
type TestResult struct {
	Channels []ChannelResult `json:"channels"`
}

// channel is one configured delivery target under a Config
// snapshot.
type channel struct {
	name string
	send func(ctx context.Context, msg Message) error
}

// Service is the alerting service. Safe for concurrent use.
type Service struct {
	cfg   Config
	cfgFn func() Config

	mu      sync.Mutex
	alerted bool // an episode alert was queued and not yet recovered
	pending int  // queued + in-flight episode messages

	queue chan Message
}

// New builds a Service; call Run to start its delivery worker.
func New(cfg Config) *Service {
	if cfg.Logger == nil {
		cfg.Logger = log.Default()
	}
	return &Service{
		cfg:   cfg,
		cfgFn: cfg.ConfigFunc,
		queue: make(chan Message, queueSize),
	}
}

func (s *Service) cfgNow() Config {
	cfg := s.cfg
	if s.cfgFn != nil {
		cfg = s.cfgFn()
	}
	if cfg.Logger == nil {
		cfg.Logger = s.cfg.Logger
	}
	return cfg
}

func (s *Service) logger() *log.Logger { return s.cfgNow().Logger }

func (s *Service) budget() time.Duration {
	if b := s.cfgNow().ExitBudget; b > 0 {
		return b
	}
	return exitBudget
}

// channels lists the delivery targets a Config configures, in a
// stable order (webhook first, then telegram).
func (s *Service) channels(cfg Config) []channel {
	var out []channel
	if strings.TrimSpace(cfg.WebhookURL) != "" {
		out = append(out, channel{"webhook", func(ctx context.Context, msg Message) error {
			return s.sendWebhook(ctx, cfg, msg)
		}})
	}
	if strings.TrimSpace(cfg.TelegramToken) != "" && strings.TrimSpace(cfg.TelegramChatID) != "" {
		out = append(out, channel{"telegram", func(ctx context.Context, msg Message) error {
			return s.sendTelegram(ctx, cfg, msg)
		}})
	}
	return out
}

// Configured reports whether at least one channel is configured
// under the current settings (regardless of the master switch).
func (s *Service) Configured() bool {
	return len(s.channels(s.cfgNow())) > 0
}

// Run is the delivery worker: it drains the queue until ctx ends.
// Episode sends are asynchronous so a wedged channel can stall
// only its own queue slot, never the watchdog.
func (s *Service) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-s.queue:
			s.deliverQueued(ctx, msg)
		}
	}
}

func (s *Service) deliverQueued(ctx context.Context, msg Message) {
	defer s.decPending()
	cfg := s.cfgNow()
	if !cfg.Enabled {
		// Disabled after the event was accepted: the episode is
		// over as far as delivery is concerned; drop quietly.
		return
	}
	for _, r := range s.deliver(ctx, cfg, msg) {
		if !r.OK {
			s.logger().Printf("alerts: %s: %v", r.Channel, r.Error)
		}
	}
}

// NotifyEvent is the watchdog's OnEvent hook. It only enqueues;
// all sending happens on the worker (or, for the exit path, in
// FlushBeforeExit). Events arriving while alerts are disabled are
// dropped without touching the episode state, so enabling alerts
// mid-episode cannot produce a recovery for an episode that was
// never alerted.
func (s *Service) NotifyEvent(ev watchdog.Event) {
	cfg := s.cfgNow()
	if !cfg.Enabled {
		return
	}
	s.mu.Lock()
	switch ev.Kind {
	case watchdog.EventStalled, watchdog.EventCrawling, watchdog.EventFrozen:
		if s.alerted {
			s.mu.Unlock()
			return
		}
		s.alerted = true
	case watchdog.EventRecovered:
		if !s.alerted {
			s.mu.Unlock()
			return
		}
		s.alerted = false
	default:
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	s.enqueue(formatEvent(ev))
}

// FlushBeforeExit is the watchdog's BeforeExit hook, called
// synchronously on the escalation path immediately before the
// process exits. It waits — bounded by the exit budget — for the
// episode's queued alert to finish delivering, then, if the
// episode was alerted, sends the exit notice synchronously
// inside whatever budget remains. A dead channel eats its share
// of the budget and is logged; the exit is never delayed past
// the budget.
func (s *Service) FlushBeforeExit(ev watchdog.Event) {
	cfg := s.cfgNow()
	if !cfg.Enabled {
		return
	}
	deadline := time.Now().Add(s.budget())
	s.waitIdle(deadline)
	s.mu.Lock()
	alerted := s.alerted
	s.mu.Unlock()
	if !alerted {
		return
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	for _, r := range s.deliver(ctx, cfg, formatEvent(ev)) {
		if !r.OK {
			s.logger().Printf("alerts: exit notice via %s: %v", r.Channel, r.Error)
		}
	}
}

// waitIdle blocks until no episode message is queued or in
// flight, or the deadline passes.
func (s *Service) waitIdle(deadline time.Time) {
	for {
		s.mu.Lock()
		p := s.pending
		s.mu.Unlock()
		if p == 0 || !time.Now().Before(deadline) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (s *Service) enqueue(msg Message) {
	s.mu.Lock()
	s.pending++
	s.mu.Unlock()
	select {
	case s.queue <- msg:
	default:
		// The worker is wedged behind slow channels for a full
		// queue's worth of episodes — pathological. Dropping is
		// still better than blocking the watchdog's tick.
		s.decPending()
		s.logger().Printf("alerts: queue full, dropping notification %q", msg.Title)
	}
}

func (s *Service) decPending() {
	s.mu.Lock()
	s.pending--
	s.mu.Unlock()
}

// Test sends a probe message to every configured channel,
// synchronously, and reports each channel's outcome. It is the
// operator's explicit action from the admin API, so it does not
// consult the master switch — it exists to verify a configuration
// before (or without) enabling it. No configured channel yields
// an empty TestResult.
func (s *Service) Test(ctx context.Context) TestResult {
	cfg := s.cfgNow()
	msg := Message{
		Title: "DHTSearch 告警测试",
		Text:  "这是一条测试消息：告警通道配置可用。入库停滞与恢复时将通过同一通道通知。",
		At:    time.Now(),
	}
	return TestResult{Channels: s.deliver(ctx, cfg, msg)}
}

// deliver sends msg to every configured channel under cfg and
// collects the per-channel outcomes. An unconfigured deployment
// returns nil.
func (s *Service) deliver(ctx context.Context, cfg Config, msg Message) []ChannelResult {
	chs := s.channels(cfg)
	if len(chs) == 0 {
		return nil
	}
	out := make([]ChannelResult, 0, len(chs))
	for _, ch := range chs {
		err := s.attempt(ctx, ch, msg)
		r := ChannelResult{Channel: ch.name, OK: err == nil}
		if err != nil {
			r.Error = err.Error()
		}
		out = append(out, r)
	}
	return out
}

// attempt performs one channel send with at most one retry. The
// retry covers a transient network wobble; a channel that answers
// with an error is retried too, since bot endpoints rate-limit
// and hiccup in ways a single retry clears.
func (s *Service) attempt(ctx context.Context, ch channel, msg Message) error {
	err := s.attemptOnce(ctx, ch, msg)
	if err == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return err
	case <-time.After(retryDelay):
	}
	if rerr := s.attemptOnce(ctx, ch, msg); rerr != nil {
		return fmt.Errorf("%v (retry: %v)", err, rerr)
	}
	return nil
}

func (s *Service) attemptOnce(ctx context.Context, ch channel, msg Message) error {
	actx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	return ch.send(actx, msg)
}

func (s *Service) httpClient(cfg Config) *http.Client {
	if cfg.HTTPClient != nil {
		return cfg.HTTPClient
	}
	return http.DefaultClient
}

// webhookPayload is the generic webhook's JSON body.
type webhookPayload struct {
	Title string `json:"title"`
	Text  string `json:"text"`
	TS    int64  `json:"ts"` // unix seconds
}

func (s *Service) sendWebhook(ctx context.Context, cfg Config, msg Message) error {
	body, err := json.Marshal(webhookPayload{Title: msg.Title, Text: msg.Text, TS: msg.At.Unix()})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.httpClient(cfg).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook answered %s", resp.Status)
	}
	return nil
}

// telegramRequest / telegramResponse are the Bot API sendMessage
// shapes this package uses.
type telegramRequest struct {
	ChatID string `json:"chat_id"`
	Text   string `json:"text"`
}

type telegramResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
}

func (s *Service) sendTelegram(ctx context.Context, cfg Config, msg Message) error {
	base := strings.TrimRight(cfg.TelegramBaseURL, "/")
	if base == "" {
		base = "https://api.telegram.org"
	}
	url := base + "/bot" + cfg.TelegramToken + "/sendMessage"
	body, err := json.Marshal(telegramRequest{
		ChatID: cfg.TelegramChatID,
		Text:   msg.Title + "\n" + msg.Text,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.httpClient(cfg).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram answered %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	var tr telegramResponse
	if err := json.Unmarshal(raw, &tr); err != nil {
		return fmt.Errorf("telegram: undecodable response: %w", err)
	}
	if !tr.OK {
		return fmt.Errorf("telegram: %s", tr.Description)
	}
	return nil
}

// formatEvent renders a watchdog episode transition as the
// message the operator reads. The numbers quoted are the
// watchdog's own probe at the transition — the same counters its
// log lines carry.
func formatEvent(ev watchdog.Event) Message {
	p := ev.Probe
	mins := int(ev.StalledFor.Round(time.Minute) / time.Minute)
	switch ev.Kind {
	case watchdog.EventStalled:
		return Message{
			Title: "DHTSearch 入库停滞",
			Text: fmt.Sprintf("入库停滞：已 %d 分钟无新增入库（自 %s 起），发现仍在工作。seen %d → %d，队列深度 %d，累计入库 %d。看门狗已打全量协程栈，持续停滞将退出进程由 systemd 拉起自愈。",
				mins, ev.Since.Format("15:04:05"), ev.SeenAtProgress, p.Seen, p.QueueDepth, p.Fetched),
			At: time.Now(),
		}
	case watchdog.EventCrawling:
		return Message{
			Title: "DHTSearch 入库近乎停滞",
			Text: fmt.Sprintf("入库爬行：最近 10 分钟仅新增 %d 条（发现在动、抓取队列未消化），队列深度 %d，累计入库 %d。看门狗已打全量协程栈，下一窗口仍爬行将退出进程自愈。",
				ev.WindowFetched, p.QueueDepth, p.Fetched),
			At: time.Now(),
		}
	case watchdog.EventFrozen:
		return Message{
			Title: "DHTSearch 管线冻结",
			Text: fmt.Sprintf("管线冻结：发现与入库已 %d 分钟均无进展（自 %s 起）。seen %d，队列深度 %d，累计入库 %d。多为外部网络问题，看门狗只告警、不退出自愈。",
				mins, ev.Since.Format("15:04:05"), p.Seen, p.QueueDepth, p.Fetched),
			At: time.Now(),
		}
	case watchdog.EventRecovered:
		return Message{
			Title: "DHTSearch 入库恢复",
			Text: fmt.Sprintf("入库已恢复：停滞约 %d 分钟后重新有新增入库。累计入库 %d，队列深度 %d。",
				mins, p.Fetched, p.QueueDepth),
			At: time.Now(),
		}
	case watchdog.EventExiting:
		return Message{
			Title: "DHTSearch 看门狗退出自愈",
			Text: fmt.Sprintf("入库停滞已持续约 %d 分钟，达到退出阈值：看门狗即将退出进程，由 systemd 拉起自愈。累计入库 %d，队列深度 %d。",
				mins, p.Fetched, p.QueueDepth),
			At: time.Now(),
		}
	}
	return Message{Title: "DHTSearch 告警", Text: string(ev.Kind), At: time.Now()}
}
