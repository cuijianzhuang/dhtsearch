package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"dhtsearch/server/internal/settings"
)

// System update: version display, update checks and panel-requested
// upgrades.
//
// The API process runs as the unprivileged service user and must
// never execute an upgrade itself. Instead the panel and the
// root-side installer script (scripts/install.sh) communicate
// through small key=value contract files in the data dir, which the
// API can write (its systemd unit grants ReadWritePaths there) and
// the script reads when its per-minute upgrade-agent timer fires:
//
//	VERSION         script -> panel: installed version, written by
//	                install.sh on every install/upgrade.
//	autoupdate      script-owned CLI state (ENABLED/FREQ/LAST_CHECK/
//	                LAST_RESULT) from `dhtsearch auto-update`.
//	update-panel    panel -> script: the AUTO_UPDATE_ENABLED /
//	                AUTO_UPDATE_TIME settings, synced here whenever an
//	                admin saves either key. Its mere presence hands
//	                control of those two values to the panel.
//	update-request  panel -> script: an upgrade request flag
//	                (REQUEST_ID/REQUESTED_AT/REQUESTED_BY). The agent
//	                consumes it exactly once and deletes it.
//	update-status   script -> panel: the agent's record of the last
//	                requested upgrade (STATE/REQUEST_ID/timestamps/
//	                FROM_VERSION/TO_VERSION/MESSAGE, plus PROGRESS/
//	                PHASE stage markers while a run is in flight —
//	                written by install.sh at real stage boundaries,
//	                absent on status files from older scripts).
//
// Precedence for the effective auto-update switch, mirrored on both
// sides: update-panel wins when present; otherwise the CLI state in
// autoupdate; otherwise disabled. The CLI takes control back by
// deleting update-panel (au_set does, with a log line).

const (
	versionFileName       = "VERSION"
	autoupdateFileName    = "autoupdate"
	updatePanelFileName   = "update-panel"
	updateRequestFileName = "update-request"
	updateStatusFileName  = "update-status"
)

// githubLatestReleaseURL is the only external endpoint the update
// check talks to. A variable so tests can point it at a mock server.
var githubLatestReleaseURL = "https://api.github.com/repos/cuijianzhuang/dhtsearch/releases/latest"

const (
	// updateCheckTTL: a successful (or failed) check is served from
	// cache for this long unless the admin forces a refresh.
	updateCheckTTL = time.Hour
	// updateCheckTimeout bounds one GitHub lookup.
	updateCheckTimeout = 5 * time.Second
)

// --- key=value contract files ---

// parseKVFile reads a contract file of KEY=value lines. The second
// return value reports whether the file exists at all (the panel
// file's presence is itself meaningful). Malformed lines are
// skipped; later keys win.
func parseKVFile(path string) (map[string]string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out, true
}

// writeKVFileAtomic writes pairs as KEY=value lines via a temp file
// in the same directory plus rename, so a reader (the other process)
// never sees a half-written file.
func writeKVFileAtomic(dir, name string, pairs [][2]string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var sb strings.Builder
	for _, p := range pairs {
		v := strings.NewReplacer("\n", " ", "\r", " ").Replace(p[1])
		fmt.Fprintf(&sb, "%s=%s\n", p[0], v)
	}
	tmp, err := os.CreateTemp(dir, name+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(sb.String()); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, filepath.Join(dir, name))
}

// --- version comparison ---

// parseSemver splits a version like "v0.1.6" (the "v" is optional)
// into numeric components. ok=false when any component is not a
// plain non-negative integer.
func parseSemver(v string) (parts []int, ok bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if v == "" {
		return nil, false
	}
	for _, s := range strings.Split(v, ".") {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			return nil, false
		}
		parts = append(parts, n)
	}
	return parts, true
}

// compareVersions orders two version strings: -1 when a < b, 0 when
// equal, +1 when a > b. Both sides parse as dotted numbers in
// practice (release tags); when either does not, it falls back to a
// plain string comparison so the answer stays deterministic.
func compareVersions(a, b string) int {
	pa, oka := parseSemver(a)
	pb, okb := parseSemver(b)
	if !oka || !okb {
		return strings.Compare(a, b)
	}
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// --- GitHub release check (cached) ---

type updateChecker struct {
	mu          sync.Mutex
	client      *http.Client
	url         string
	latest      string    // tag of the latest release, "" until a check succeeds
	publishedAt string    // RFC3339, as GitHub reports it
	checkedAt   time.Time // last successful check
	attemptAt   time.Time // last attempt, success or failure
	lastErr     string    // error of the last failed attempt, "" after a success
}

func newUpdateChecker() *updateChecker {
	return &updateChecker{
		client: &http.Client{Timeout: updateCheckTimeout},
		url:    githubLatestReleaseURL,
	}
}

type checkResult struct {
	Latest      string
	PublishedAt string
	CheckedAt   time.Time // zero when no check has ever succeeded
	Err         string    // last attempt's error, "" when it succeeded
}

// check returns the latest-release info, querying GitHub unless a
// recent attempt (younger than updateCheckTTL) can be served from
// cache. force always queries. A failed query keeps the previous
// successful result and reports the error alongside it.
func (c *updateChecker) check(ctx context.Context, force bool) checkResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !force && !c.attemptAt.IsZero() && time.Since(c.attemptAt) < updateCheckTTL {
		return checkResult{Latest: c.latest, PublishedAt: c.publishedAt, CheckedAt: c.checkedAt, Err: c.lastErr}
	}
	c.attemptAt = time.Now()
	res := checkResult{Latest: c.latest, PublishedAt: c.publishedAt, CheckedAt: c.checkedAt}
	fail := func(err error) checkResult {
		c.lastErr = err.Error()
		res.Err = c.lastErr
		return res
	}
	reqCtx, cancel := context.WithTimeout(ctx, updateCheckTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, c.url, nil)
	if err != nil {
		return fail(err)
	}
	// Only the official GitHub API host is ever contacted (the URL is
	// a build-time constant, overridable only by tests).
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "dhtsearch-update-check")
	resp, err := c.client.Do(req)
	if err != nil {
		return fail(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fail(fmt.Errorf("github API returned %s", resp.Status))
	}
	var body struct {
		TagName     string `json:"tag_name"`
		PublishedAt string `json:"published_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return fail(fmt.Errorf("parse github response: %w", err))
	}
	if body.TagName == "" {
		return fail(errors.New("github response has no tag_name"))
	}
	c.latest = body.TagName
	c.publishedAt = body.PublishedAt
	c.checkedAt = time.Now()
	c.lastErr = ""
	res.Latest, res.PublishedAt, res.CheckedAt, res.Err = c.latest, c.publishedAt, c.checkedAt, ""
	return res
}

// --- payload ---

// currentVersion reads the installed version from the script-owned
// VERSION file in the data dir. "unknown" when the file is missing
// (source builds, pre-installer deployments).
func (s *Server) currentVersion() string {
	if s.opts.DataDir == "" {
		return "unknown"
	}
	// VERSION is a bare version line, not key=value.
	b, err := os.ReadFile(filepath.Join(s.opts.DataDir, versionFileName))
	if err != nil {
		return "unknown"
	}
	if v := strings.TrimSpace(string(b)); v != "" {
		return v
	}
	return "unknown"
}

// autoUpdateState resolves the effective auto-update configuration
// the same way the script does (panel file > CLI state > disabled)
// and reports where the answer came from.
type autoUpdateState struct {
	Enabled    bool   `json:"enabled"`
	Time       string `json:"time"`
	Freq       string `json:"freq"`
	Source     string `json:"source"` // "panel" | "script" | "none"
	LastCheck  string `json:"last_check"`
	LastResult string `json:"last_result"`
}

func (s *Server) autoUpdateState() autoUpdateState {
	st := autoUpdateState{Time: "03:37", Freq: "daily", Source: "none"}
	if s.opts.DataDir == "" {
		return st
	}
	cli, cliOK := parseKVFile(filepath.Join(s.opts.DataDir, autoupdateFileName))
	if cliOK {
		if f := cli["FREQ"]; f == "weekly" || f == "daily" {
			st.Freq = f
		}
		st.LastCheck = cli["LAST_CHECK"]
		st.LastResult = cli["LAST_RESULT"]
	}
	if panel, ok := parseKVFile(filepath.Join(s.opts.DataDir, updatePanelFileName)); ok {
		st.Source = "panel"
		st.Enabled = panel["ENABLED"] == "1"
		if settings.ValidClockTime(panel["TIME"]) {
			st.Time = panel["TIME"]
		}
		return st
	}
	if cliOK {
		st.Source = "script"
		st.Enabled = cli["ENABLED"] == "1"
	}
	return st
}

// upgradeState is the panel-visible state of the last requested
// upgrade, merged from the request flag and the agent's status file.
type upgradeState struct {
	State       string `json:"state"` // idle | requested | running | succeeded | failed
	Pending     bool   `json:"pending"`
	RequestID   string `json:"request_id"`
	RequestedAt string `json:"requested_at"`
	RequestedBy string `json:"requested_by"`
	StartedAt   string `json:"started_at"`
	FinishedAt  string `json:"finished_at"`
	FromVersion string `json:"from_version"`
	ToVersion   string `json:"to_version"`
	Message     string `json:"message"`
	// Progress/Phase mirror the PROGRESS/PHASE stage markers the
	// installer writes into update-status at real stage boundaries
	// (percent = stages completed, never a time estimate). Nil —
	// serialized as null — when the status file carries none: an
	// older script, a run that has not started, or the already-latest
	// no-op, whose outcome the message alone describes.
	Progress *int    `json:"progress"`
	Phase    *string `json:"phase"`
}

func (s *Server) upgradeState() upgradeState {
	out := upgradeState{State: "idle"}
	if s.opts.DataDir == "" {
		return out
	}
	status, _ := parseKVFile(filepath.Join(s.opts.DataDir, updateStatusFileName))
	if status != nil {
		out.State = status["STATE"]
		if out.State == "" {
			out.State = "idle"
		}
		out.RequestID = status["REQUEST_ID"]
		out.RequestedAt = status["REQUESTED_AT"]
		out.StartedAt = status["STARTED_AT"]
		out.FinishedAt = status["FINISHED_AT"]
		out.FromVersion = status["FROM_VERSION"]
		out.ToVersion = status["TO_VERSION"]
		out.Message = status["MESSAGE"]
		if raw := status["PROGRESS"]; raw != "" {
			if n, err := strconv.Atoi(raw); err == nil {
				n = max(0, min(100, n))
				out.Progress = &n
			}
		}
		if ph := status["PHASE"]; ph != "" {
			out.Phase = &ph
		}
	}
	if req, ok := parseKVFile(filepath.Join(s.opts.DataDir, updateRequestFileName)); ok {
		out.Pending = true
		// A request the agent has not picked up yet (its status file
		// does not carry this request id) reads as "requested" even
		// when an older run's status is still on disk.
		if out.RequestID != req["REQUEST_ID"] && out.State != "running" {
			out.State = "requested"
			out.RequestID = req["REQUEST_ID"]
			out.RequestedAt = req["REQUESTED_AT"]
			out.RequestedBy = req["REQUESTED_BY"]
			out.StartedAt, out.FinishedAt = "", ""
			out.FromVersion, out.ToVersion, out.Message = "", "", ""
			out.Progress, out.Phase = nil, nil
		}
	}
	if out.State == "running" {
		out.Pending = true
	}
	return out
}

func (s *Server) updatePayload(ctx context.Context, forceCheck bool) map[string]any {
	current := s.currentVersion()
	chk := s.upd.check(ctx, forceCheck)
	available := chk.Latest != "" && current != "unknown" &&
		compareVersions(chk.Latest, current) > 0
	checkedAt := int64(0)
	if !chk.CheckedAt.IsZero() {
		checkedAt = chk.CheckedAt.Unix()
	}
	return map[string]any{
		"current_version":     current,
		"latest_version":      chk.Latest,
		"latest_published_at": chk.PublishedAt,
		"update_available":    available,
		"checked_at":          checkedAt,
		"check_error":         chk.Err,
		"auto_update":         s.autoUpdateState(),
		"upgrade":             s.upgradeState(),
		"contract_ready":      s.opts.DataDir != "",
	}
}

// --- handlers (admin role only: update actions steer the host's
// installer, so reviewers never reach them) ---

func (s *Server) handleAdminUpdateGet(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireRole(w, r, roleAdmin); !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.updatePayload(r.Context(), false))
}

func (s *Server) handleAdminUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireRole(w, r, roleAdmin); !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.updatePayload(r.Context(), true))
}

func (s *Server) handleAdminUpdateRequest(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireRole(w, r, roleAdmin)
	if !ok {
		return
	}
	if s.opts.DataDir == "" {
		writeError(w, http.StatusServiceUnavailable, "update contract unavailable (no data dir)")
		return
	}
	state := s.upgradeState()
	if state.Pending {
		writeError(w, http.StatusConflict, "已有升级请求在等待执行，请等它完成")
		return
	}
	if state.State == "running" {
		writeError(w, http.StatusConflict, "升级正在执行中")
		return
	}
	id := strconv.FormatInt(time.Now().UnixNano(), 10)
	err := writeKVFileAtomic(s.opts.DataDir, updateRequestFileName, [][2]string{
		{"REQUEST_ID", id},
		{"REQUESTED_AT", time.Now().Format("2006-01-02 15:04:05")},
		{"REQUESTED_BY", p.Username},
	})
	if err != nil {
		s.logger.Printf("api: write update request: %v", err)
		writeError(w, http.StatusInternalServerError, "写入升级请求失败")
		return
	}
	s.logger.Printf("api: upgrade requested from admin panel by %s (request %s)", p.Username, id)
	writeJSON(w, http.StatusAccepted, s.updatePayload(r.Context(), false))
}

// syncUpdatePanelFile mirrors the AUTO_UPDATE_* settings into the
// panel contract file the installer script reads. Called after a
// settings save that touched either key; until the first such save
// the file does not exist and the script follows its CLI state.
func (s *Server) syncUpdatePanelFile() error {
	if s.opts.DataDir == "" {
		return errors.New("data dir not configured")
	}
	if s.opts.Settings == nil {
		return errors.New("settings unavailable")
	}
	enabled := "0"
	if s.opts.Settings.Bool(settings.KeyAutoUpdateEnabled) {
		enabled = "1"
	}
	t := s.opts.Settings.Get(settings.KeyAutoUpdateTime)
	if !settings.ValidClockTime(t) {
		t = "03:37"
	}
	return writeKVFileAtomic(s.opts.DataDir, updatePanelFileName, [][2]string{
		{"ENABLED", enabled},
		{"TIME", t},
		{"UPDATED_AT", time.Now().Format("2006-01-02 15:04:05")},
	})
}
