// Package libtorrent is the libtorrent metadata-fetch foundation: an
// alternative to the anacrolix-based metadata.Fetcher (and to the
// native fetcher) selected with META_FETCHER=libtorrent.
//
// The Go server stays a pure-Go static binary, so libtorrent itself
// never links into it. A small C++ sidecar process (see sidecar/ in
// the repo root) hosts a libtorrent-rasterbar 2.x session configured
// for metadata-only crawling; this package supervises that process
// and speaks the framed-JSON protocol documented in
// sidecar/PROTOCOL.md. The fetcher contract — same jobs in, same
// metadata.Record out, same Stats/PrescreenDropped outcomes — matches
// metadata.Fetcher, so main.go, the admin pipeline and the stall
// watchdog cannot tell the foundations apart.
//
// Semantic deltas from the anacrolix fetcher (also in the PR body):
//
//   - Prescreen: the incumbent samples Torrent.Stats().ActivePeers
//     from inside its own client; here the sidecar reports peer counts
//     as protocol events (~100ms cadence), so the prescreen decision
//     can lag a connection by up to one report interval. The window,
//     budget and classification are otherwise identical.
//   - Duplicate concurrent fetches of one hash: anacrolix dedups per
//     client and both workers share the torrent; the sidecar keys
//     jobs by infohash and rejects a second concurrent add as an
//     error (counted as failed). The fetch queue dedups queued
//     hashes, so this only bites when the same hash is submitted
//     twice in flight from different sources.
//   - uTP/TCP behaviour, tracker handling and PEX are libtorrent's
//     own, not anacrolix's; peer-supply inputs (the magnet and its
//     tracker suffix) are constructed exactly as the incumbent does.
package libtorrent

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"dhtsearch/server/internal/filter"
	"dhtsearch/server/internal/metadata"
)

// BinaryName is the sidecar executable's file name. Releases ship it
// as an optional extra asset; install.sh places it next to
// dhtsearch-server.
const BinaryName = "dhtsearch-lt-sidecar"

// Config is the fetcher configuration: the shared metadata.Config
// plus the sidecar location. Shards is ignored (there is exactly one
// sidecar session); Workers caps concurrent sidecar torrents because
// the worker pool bounds in-flight jobs.
type Config struct {
	metadata.Config
	// SidecarPath is the explicit sidecar binary path (META_LT_SIDECAR).
	// Empty selects discovery: the directory of the running server
	// binary first, then PATH.
	SidecarPath string
}

// The libtorrent fetcher is a full metadata.Backend: same contract,
// same counter meanings as the anacrolix and native foundations.
var _ metadata.Backend = (*Fetcher)(nil)

// Fetcher proxies metadata fetches to the libtorrent sidecar.
type Fetcher struct {
	cfg          metadata.Config
	logger       *log.Logger
	magnetSuffix string
	sup          *supervisor

	wg     sync.WaitGroup
	cancel context.CancelFunc

	mu               sync.Mutex
	fetched          int64
	timedOut         int64
	failed           int64
	prescreenDropped int64
}

// New starts the sidecar and waits for its readiness handshake. It
// fails loudly when the binary is missing or cannot start: the
// operator explicitly selected this foundation, so there is no
// silent fallback to another one.
func New(cfg Config) (*Fetcher, error) {
	mc := cfg.Config
	if mc.Workers <= 0 {
		mc.Workers = 16
	}
	if mc.Timeout <= 0 {
		mc.Timeout = 45 * time.Second
	}
	if mc.MaxFiles <= 0 {
		mc.MaxFiles = 50
	}
	if mc.PrescreenTimeout <= 0 {
		mc.PrescreenTimeout = metadata.DefaultPrescreenTimeout
	}
	logger := mc.Logger
	if logger == nil {
		logger = log.Default()
	}
	path, err := resolveSidecar(cfg.SidecarPath)
	if err != nil {
		return nil, err
	}
	sup := newSupervisor(path, logger)
	if err := sup.start(); err != nil {
		sup.close()
		return nil, fmt.Errorf("META_FETCHER=libtorrent: %w", err)
	}
	logger.Printf("metadata foundation: libtorrent (sidecar %s)", path)
	return &Fetcher{
		cfg:          mc,
		logger:       logger,
		magnetSuffix: magnetTrackerParams(mc.Trackers),
		sup:          sup,
	}, nil
}

// resolveSidecar locates the sidecar binary or explains exactly what
// is missing.
func resolveSidecar(explicit string) (string, error) {
	if explicit != "" {
		fi, err := os.Stat(explicit)
		if err != nil {
			return "", fmt.Errorf("META_FETCHER=libtorrent: sidecar binary %s (from META_LT_SIDECAR) is not usable: %w — the libtorrent foundation needs the %s binary; install a release that ships it or fix META_LT_SIDECAR", explicit, err, BinaryName)
		}
		if fi.IsDir() || fi.Mode()&0o111 == 0 {
			return "", fmt.Errorf("META_FETCHER=libtorrent: sidecar binary %s (from META_LT_SIDECAR) is not an executable file", explicit)
		}
		return explicit, nil
	}
	var tried []string
	if exe, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(exe), BinaryName)
		tried = append(tried, cand)
		if fi, err := os.Stat(cand); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return cand, nil
		}
	}
	if p, err := exec.LookPath(BinaryName); err == nil {
		return p, nil
	}
	tried = append(tried, "$PATH")
	return "", fmt.Errorf("META_FETCHER=libtorrent: sidecar binary %s not found (looked in: %s) — the libtorrent foundation runs as a separate process; install a release that ships %s (install.sh places it next to dhtsearch-server) or set META_LT_SIDECAR to its path", BinaryName, strings.Join(tried, ", "), BinaryName)
}

// magnetTrackerParams encodes announce URLs as magnet &tr=
// parameters. It must produce exactly what
// metadata.magnetTrackerParams produces — the sidecar's peer supply
// is the incumbent's tracker set, by construction.
func magnetTrackerParams(trackers []string) string {
	var b strings.Builder
	for _, tr := range trackers {
		tr = strings.TrimSpace(tr)
		if tr == "" {
			continue
		}
		b.WriteString("&tr=")
		b.WriteString(url.QueryEscape(tr))
	}
	return b.String()
}

// Run mirrors metadata.Fetcher.Run.
func (f *Fetcher) Run(ctx context.Context, in <-chan string, onRecord func(metadata.Record)) {
	ctx, f.cancel = context.WithCancel(ctx)
	for i := 0; i < f.cfg.Workers; i++ {
		f.wg.Add(1)
		go func() {
			defer f.wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case hexHash, ok := <-in:
					if !ok {
						return
					}
					if rec, ok := f.fetch(ctx, hexHash); ok {
						onRecord(rec)
					}
				}
			}
		}()
	}
}

// RunQueue mirrors metadata.Fetcher.RunQueue.
func (f *Fetcher) RunQueue(ctx context.Context, q *metadata.Queue, onRecord func(metadata.Record)) {
	ctx, f.cancel = context.WithCancel(ctx)
	for i := 0; i < f.cfg.Workers; i++ {
		f.wg.Add(1)
		go func() {
			defer f.wg.Done()
			for {
				it, ok := q.Pop(ctx)
				if !ok {
					return
				}
				if rec, ok := f.fetch(ctx, it.Hash); ok {
					onRecord(rec)
				}
			}
		}()
	}
}

// fetch runs one job through the sidecar and classifies the outcome
// into the shared stats, mirroring metadata.Fetcher.fetch.
func (f *Fetcher) fetch(ctx context.Context, hexHash string) (metadata.Record, bool) {
	rec := metadata.Record{InfoHash: hexHash}
	if !validInfohash(hexHash) {
		f.count(&f.failed)
		f.report(hexHash, metadata.OutcomeFailed)
		return rec, false
	}
	timeout := f.cfg.Timeout
	if f.cfg.TimeoutFunc != nil {
		if d := f.cfg.TimeoutFunc(); d > 0 {
			timeout = d
		}
	}
	prescreen := f.prescreenWindow(timeout)

	id, job, err := f.sup.submit(ctx, "magnet:?xt=urn:btih:"+hexHash+f.magnetSuffix, timeout, f.cfg.MaxFiles)
	if err != nil {
		if ctx.Err() != nil {
			return rec, false // aborted, like the incumbent's waitAborted
		}
		f.logger.Printf("libtorrent fetch %s: %v", hexHash, err)
		f.count(&f.failed)
		f.report(hexHash, metadata.OutcomeFailed)
		return rec, false
	}

	outcome := f.wait(ctx, id, job, timeout, prescreen)
	switch outcome.kind {
	case waitGotInfo:
		res := outcome.result
		rec.Name = res.Name
		if rec.Name == "" {
			rec.Name = hexHash // incumbent: Info.BestName falls back to the hash
		}
		rec.TotalSize = res.TotalSize
		rec.FileCount = res.FileCount
		files := res.Files
		if len(files) > f.cfg.MaxFiles {
			files = files[:f.cfg.MaxFiles]
		}
		for _, wf := range files {
			rec.Files = append(rec.Files, filter.File{Path: wf.Path, Size: wf.Size})
		}
		f.count(&f.fetched)
		f.report(hexHash, metadata.OutcomeFetched)
		return rec, true
	case waitPrescreened:
		f.count(&f.prescreenDropped)
		f.report(hexHash, metadata.OutcomePrescreened)
	case waitTimedOut:
		f.count(&f.timedOut)
		f.report(hexHash, metadata.OutcomeTimedOut)
	case waitFailed:
		f.count(&f.failed)
		f.report(hexHash, metadata.OutcomeFailed)
	case waitAborted:
	}
	return rec, false
}

// report delivers a fetch outcome to Config.ResultFunc, when set,
// mirroring the incumbent fetcher's contract (metadata.FetchOutcome).
// Aborted attempts (context cancelled) are never reported, so a
// shutdown does not push live hashes into the failure backoff.
func (f *Fetcher) report(hash string, outcome metadata.FetchOutcome) {
	if f.cfg.ResultFunc != nil {
		f.cfg.ResultFunc(hash, outcome)
	}
}

// waitOutcome kinds, mirroring the incumbent's classification.
type waitKind int

const (
	waitGotInfo waitKind = iota
	waitTimedOut
	waitPrescreened
	waitFailed
	waitAborted
)

type waitOutcome struct {
	kind   waitKind
	result *frame
}

// wait runs the prescreen/timeout state machine over a job's event
// stream. The sidecar enforces the same deadline on its side; the
// local timers exist for the prescreen phase (a local decision: the
// job is cancelled and the worker freed) and as a backstop if the
// sidecar stops answering.
func (f *Fetcher) wait(ctx context.Context, id uint64, job *jobHandle, timeout, prescreen time.Duration) waitOutcome {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	finish := func(kind waitKind, res *frame) waitOutcome {
		if kind != waitGotInfo {
			f.sup.cancel(id)
		}
		return waitOutcome{kind: kind, result: res}
	}

	if prescreen > 0 && prescreen < timeout {
		pre := time.NewTimer(prescreen)
		connected := false
	phase1:
		for !connected {
			select {
			case <-ctx.Done():
				pre.Stop()
				return finish(waitAborted, nil)
			case <-deadline.C:
				pre.Stop()
				return finish(waitTimedOut, nil)
			case <-pre.C:
				break phase1
			case ev := <-job.events:
				switch {
				case ev.err != nil:
					pre.Stop()
					return finish(waitFailed, nil)
				case ev.result != nil:
					pre.Stop()
					return classify(ev.result)
				case ev.peers > 0:
					connected = true
				}
			}
		}
		pre.Stop()
		if !connected {
			return finish(waitPrescreened, nil)
		}
	}

	for {
		select {
		case <-ctx.Done():
			return finish(waitAborted, nil)
		case <-deadline.C:
			return finish(waitTimedOut, nil)
		case ev := <-job.events:
			switch {
			case ev.err != nil:
				return finish(waitFailed, nil)
			case ev.result != nil:
				return classify(ev.result)
			}
		}
	}
}

// classify maps a sidecar result frame to a wait outcome.
func classify(res *frame) waitOutcome {
	switch res.Outcome {
	case outcomeOK:
		return waitOutcome{kind: waitGotInfo, result: res}
	case outcomeTimeout:
		return waitOutcome{kind: waitTimedOut}
	default:
		return waitOutcome{kind: waitFailed}
	}
}

// prescreenWindow resolves the prescreen phase length for one fetch —
// the same rules as metadata.Fetcher.prescreenWindow.
func (f *Fetcher) prescreenWindow(timeout time.Duration) time.Duration {
	enabled := f.cfg.PrescreenEnabled
	if f.cfg.PrescreenEnabledFunc != nil {
		enabled = f.cfg.PrescreenEnabledFunc()
	}
	if !enabled {
		return 0
	}
	d := f.cfg.PrescreenTimeout
	if f.cfg.PrescreenTimeoutFunc != nil {
		if v := f.cfg.PrescreenTimeoutFunc(); v > 0 {
			d = v
		}
	}
	if d <= 0 {
		d = metadata.DefaultPrescreenTimeout
	}
	if d >= timeout {
		return 0
	}
	return d
}

// validInfohash mirrors metadata.validInfohash (40 hex chars, not all
// zero): the last check before an untrusted value reaches the
// sidecar.
func validInfohash(s string) bool {
	if len(s) != 40 {
		return false
	}
	zero := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
		if c != '0' {
			zero = false
		}
	}
	return !zero
}

func (f *Fetcher) count(p *int64) {
	f.mu.Lock()
	*p++
	f.mu.Unlock()
}

// Stats mirrors metadata.Fetcher.Stats.
func (f *Fetcher) Stats() (fetched, timedOut, failed int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fetched, f.timedOut, f.failed
}

// PrescreenDropped mirrors metadata.Fetcher.PrescreenDropped.
func (f *Fetcher) PrescreenDropped() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.prescreenDropped
}

// Close stops the workers and shuts the sidecar down.
func (f *Fetcher) Close() {
	if f.cancel != nil {
		f.cancel()
	}
	f.wg.Wait()
	f.sup.close()
}
