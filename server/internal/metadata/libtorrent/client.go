package libtorrent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"sync"
	"time"
)

// This file is the sidecar supervisor: it owns the child process
// lifecycle (spawn, readiness handshake, restart with backoff, clean
// shutdown) and routes protocol frames between fetch workers and the
// process. The Go server stays a pure-Go static binary; libtorrent
// lives entirely on the other side of the pipe.

// readyTimeout bounds how long a submitted job waits for a (re)started
// sidecar to become ready before the job fails as a transport error.
// Kept short on purpose: a fetch has a 45s budget and a process that
// is down for most of it cannot serve the job anyway — failing fast
// keeps the failure classification honest instead of letting the job
// drift into a spurious timeout.
const readyTimeout = 3 * time.Second

// readyHandshakeTimeout bounds the wait for the sidecar's ready frame
// after spawn, at fetcher construction and on every restart.
const readyHandshakeTimeout = 15 * time.Second

// startupWait is how long start() tolerates a crash-looping sidecar
// before giving up. A var so tests can shrink it.
var startupWait = readyHandshakeTimeout + backoffMax

// downGrace is how long the sidecar must be continuously unready
// before unavailableReason reports it: a normal restart (backoff
// starts at 1s) must not flap the admin warning. A var so tests can
// shrink it.
var downGrace = 10 * time.Second

const (
	backoffMin = 1 * time.Second
	backoffMax = 30 * time.Second
	// stableAfter is how long a process generation must live for the
	// restart backoff to reset — a sidecar that ran fine for a minute
	// and then died once is not crash-looping.
	stableAfter = 60 * time.Second
)

// jobEvent is one thing that happened to a submitted job: a peer-count
// update, the final result, or a terminal transport error (the
// process died or the write failed).
type jobEvent struct {
	peers  int
	result *frame // outcome frame; nil for peer updates
	err    error  // terminal: no further events will arrive
}

type jobHandle struct {
	events chan jobEvent
}

type procState struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	writeMu sync.Mutex
	started time.Time
	dead    chan struct{} // closed when the reader hits EOF/error

	mu    sync.Mutex
	ready bool
	jobs  map[uint64]*jobHandle

	// Latest DHT status frame from this generation (zero until the
	// first one arrives, shortly after ready).
	dhtRunning bool
	dhtNodes   int
}

// failAll delivers a terminal error to every pending job. Called once
// per process generation, after the process is gone.
func (p *procState) failAll(err error) {
	p.mu.Lock()
	jobs := p.jobs
	p.jobs = make(map[uint64]*jobHandle)
	p.mu.Unlock()
	for _, j := range jobs {
		select {
		case j.events <- jobEvent{err: err}:
		default:
		}
	}
}

type supervisor struct {
	path   string
	logger *log.Logger

	mu       sync.Mutex
	changed  chan struct{} // closed & replaced on every lifecycle change
	proc     *procState    // nil while down / restarting
	closed   bool
	nextID   uint64
	wg       sync.WaitGroup
	procDone chan struct{} // closed when runLoop exits

	// downSince is when the current continuously-unready episode
	// began (zero while a generation is ready); lastDownErr is the
	// most recent spawn/exit failure in it. Together they power
	// Fetcher.UnavailableReason: the supervisor restarts a dead
	// sidecar forever and never gives up, so a permanent mid-run
	// death needs this explicit signal instead.
	downSince   time.Time
	lastDownErr string
}

func newSupervisor(path string, logger *log.Logger) *supervisor {
	return &supervisor{
		path:     path,
		logger:   logger,
		changed:  make(chan struct{}),
		procDone: make(chan struct{}),
	}
}

// broadcastLocked wakes every goroutine waiting on lifecycle changes.
// s.mu must be held.
func (s *supervisor) broadcastLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}

// start launches the supervisor loop and blocks until the first
// process generation is ready (or fails to become ready), so fetcher
// construction can fail loudly when the operator picked this
// foundation but the sidecar cannot run.
func (s *supervisor) start() error {
	s.wg.Add(1)
	go s.runLoop()
	deadline := time.Now().Add(startupWait)
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return errors.New("supervisor closed during startup")
		}
		ps := s.proc
		changed := s.changed
		s.mu.Unlock()
		if ps != nil {
			ps.mu.Lock()
			ready := ps.ready
			ps.mu.Unlock()
			if ready {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("sidecar %s did not become ready within %s", s.path, readyHandshakeTimeout)
		}
		select {
		case <-changed:
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// unavailableReason describes the current unready episode once it
// has lasted past downGrace, or returns "" — while a generation is
// ready, during a brief restart, and after Close (shutdown is not
// an outage). The supervisor never stops restarting, so this is the
// only "the sidecar is gone for good" signal: without it a mid-run
// permanent death (binary deleted, crash loop) just fails fetches
// forever, visible only as a climbing counter.
func (s *supervisor) unavailableReason() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.downSince.IsZero() {
		return ""
	}
	down := time.Since(s.downSince)
	if down < downGrace {
		return ""
	}
	reason := s.lastDownErr
	if reason == "" {
		reason = "sidecar not ready"
	}
	return fmt.Sprintf("libtorrent sidecar unavailable for %s (%s); fetches fail until it recovers — check the %s binary", down.Round(time.Second), reason, BinaryName)
}

func (s *supervisor) runLoop() {
	defer s.wg.Done()
	defer close(s.procDone)
	backoff := backoffMin
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()

		ps, err := s.spawn()
		if err != nil {
			s.logger.Printf("libtorrent sidecar: spawn: %v (retry in %s)", err, backoff)
			s.mu.Lock()
			if s.downSince.IsZero() {
				s.downSince = time.Now()
			}
			s.lastDownErr = fmt.Sprintf("spawn: %v", err)
			s.mu.Unlock()
			if !s.sleep(backoff) {
				return
			}
			backoff = min(backoff*2, backoffMax)
			continue
		}

		// If Close raced the spawn (it looked before s.proc was set),
		// kill this generation ourselves instead of waiting on it.
		s.mu.Lock()
		closedEarly := s.closed
		s.mu.Unlock()
		if closedEarly {
			_ = ps.cmd.Process.Kill()
			<-ps.dead
			ps.failAll(errors.New("libtorrent fetcher closed"))
			_ = ps.cmd.Wait()
			return
		}

		// Wait for this generation to die (reader EOF) or for Close.
		<-ps.dead
		lived := time.Since(ps.started)
		ps.failAll(errors.New("libtorrent sidecar process exited"))
		s.mu.Lock()
		if s.proc == ps {
			s.proc = nil
		}
		closed := s.closed
		if !closed && s.downSince.IsZero() {
			s.downSince = time.Now()
			s.lastDownErr = "sidecar process exited"
		}
		s.broadcastLocked()
		s.mu.Unlock()
		_ = ps.cmd.Wait()
		if closed {
			return
		}
		if lived >= stableAfter {
			backoff = backoffMin
		}
		s.logger.Printf("libtorrent sidecar: process exited after %s; restarting in %s", lived.Round(time.Millisecond), backoff)
		if !s.sleep(backoff) {
			return
		}
		backoff = min(backoff*2, backoffMax)
	}
}

// sleep waits d, returning false when the supervisor was closed.
func (s *supervisor) sleep(d time.Duration) bool {
	s.mu.Lock()
	changed := s.changed
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return false
	}
	select {
	case <-time.After(d):
		return true
	case <-changed:
		s.mu.Lock()
		defer s.mu.Unlock()
		return !s.closed
	}
}

// spawn starts one process generation and its frame reader. It does
// not wait for the ready frame; readiness is marked by the reader.
func (s *supervisor) spawn() (*procState, error) {
	cmd := exec.Command(s.path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	ps := &procState{
		cmd:     cmd,
		stdin:   stdin,
		started: time.Now(),
		dead:    make(chan struct{}),
		jobs:    make(map[uint64]*jobHandle),
	}
	go func() { // forward sidecar logs into the server log
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 64<<10), 64<<10)
		for sc.Scan() {
			s.logger.Printf("lt-sidecar: %s", sc.Text())
		}
	}()
	s.mu.Lock()
	s.proc = ps
	s.broadcastLocked()
	s.mu.Unlock()
	go s.readLoop(ps, stdout)
	return ps, nil
}

// readLoop decodes frames from one process generation and routes them.
func (s *supervisor) readLoop(ps *procState, stdout io.Reader) {
	defer close(ps.dead)
	for {
		f, err := readFrame(stdout)
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
				s.logger.Printf("libtorrent sidecar: read: %v", err)
			}
			return
		}
		switch f.Type {
		case "ready":
			ps.mu.Lock()
			ps.ready = true
			ps.mu.Unlock()
			s.logger.Printf("libtorrent sidecar: ready (sidecar %s, libtorrent %s)", f.Sidecar, f.Libtorrent)
			s.mu.Lock()
			s.downSince = time.Time{}
			s.lastDownErr = ""
			s.broadcastLocked()
			s.mu.Unlock()
		case "status":
			ps.mu.Lock()
			ps.dhtRunning = f.DHTRunning
			ps.dhtNodes = f.DHTNodes
			ps.mu.Unlock()
		case "peers":
			if j := ps.lookup(f.ID); j != nil {
				select {
				case j.events <- jobEvent{peers: f.Peers}:
				default:
				}
			}
		case "result":
			ps.mu.Lock()
			j := ps.jobs[f.ID]
			delete(ps.jobs, f.ID)
			ps.mu.Unlock()
			if j != nil {
				select {
				case j.events <- jobEvent{result: f}:
				default:
				}
			}
		default:
			s.logger.Printf("libtorrent sidecar: ignoring unknown frame type %q", f.Type)
		}
	}
}

func (p *procState) lookup(id uint64) *jobHandle {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.jobs[id]
}

// dhtStatus reports the running generation's latest DHT status
// frame: whether the sidecar session's DHT is running and how many
// routing-table nodes it knows. (false, 0) while no generation is
// ready or none has reported yet — a fresh generation reports within
// a couple of seconds of ready.
func (s *supervisor) dhtStatus() (bool, int) {
	s.mu.Lock()
	ps := s.proc
	s.mu.Unlock()
	if ps == nil {
		return false, 0
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if !ps.ready {
		return false, 0
	}
	return ps.dhtRunning, ps.dhtNodes
}

// submit registers a fetch job with the running sidecar and returns
// its event handle. It waits briefly for a ready process (see
// readyTimeout) and errors out if none is available. peerHints are
// "host:port" addresses sent with the job for the sidecar to connect
// alongside its tracker/PEX/DHT supply (may be nil).
func (s *supervisor) submit(ctx context.Context, magnet string, deadline time.Duration, maxFiles int, peerHints []string) (uint64, *jobHandle, error) {
	deadlineTimer := time.NewTimer(readyTimeout)
	defer deadlineTimer.Stop()
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return 0, nil, errors.New("libtorrent fetcher is closed")
		}
		ps := s.proc
		changed := s.changed
		s.mu.Unlock()

		if ps != nil {
			ps.mu.Lock()
			ready := ps.ready
			ps.mu.Unlock()
			if ready {
				s.mu.Lock()
				s.nextID++
				id := s.nextID
				s.mu.Unlock()
				j := &jobHandle{events: make(chan jobEvent, 8)}
				ps.mu.Lock()
				ps.jobs[id] = j
				ps.mu.Unlock()
				err := ps.write(&frame{
					Type:      "fetch",
					ID:        id,
					Magnet:    magnet,
					Deadline:  deadline.Milliseconds(),
					MaxFiles:  maxFiles,
					PeerHints: peerHints,
				})
				if err != nil {
					ps.mu.Lock()
					delete(ps.jobs, id)
					ps.mu.Unlock()
					return 0, nil, fmt.Errorf("libtorrent sidecar: write fetch: %w", err)
				}
				return id, j, nil
			}
		}
		select {
		case <-ctx.Done():
			return 0, nil, ctx.Err()
		case <-deadlineTimer.C:
			return 0, nil, errors.New("libtorrent sidecar is not ready (down or restarting)")
		case <-changed:
		}
	}
}

func (p *procState) write(f *frame) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	return writeFrame(p.stdin, f)
}

// cancel tells the sidecar to drop a job (best effort) and forgets it
// locally so a late result is discarded.
func (s *supervisor) cancel(id uint64) {
	s.mu.Lock()
	ps := s.proc
	s.mu.Unlock()
	if ps == nil {
		return
	}
	ps.mu.Lock()
	delete(ps.jobs, id)
	ps.mu.Unlock()
	_ = ps.write(&frame{Type: "cancel", ID: id})
}

// close stops the supervisor: no restarts, pending jobs fail, and the
// process gets a graceful shutdown frame before being killed.
func (s *supervisor) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	ps := s.proc
	s.broadcastLocked()
	s.mu.Unlock()

	if ps != nil {
		_ = ps.write(&frame{Type: "shutdown"})
		select {
		case <-ps.dead:
		case <-time.After(2 * time.Second):
			if ps.cmd.Process != nil {
				_ = ps.cmd.Process.Kill()
			}
			<-ps.dead
		}
		ps.failAll(errors.New("libtorrent fetcher closed"))
	}
	// runLoop observes closed and exits; wait briefly for it so the
	// process is reaped before we return.
	select {
	case <-s.procDone:
	case <-time.After(3 * time.Second):
	}
	s.wg.Wait()
}
