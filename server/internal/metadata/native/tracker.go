// Tracker announces: the native fetcher's peer supply. It announces
// to exactly the tracker list the anacrolix fetcher's magnets carry
// (Config.Trackers, from the TRACKERS setting), over both transports
// present in that list — UDP (BEP 15) and HTTP — and nowhere else.
// Like the incumbent, peer discovery beyond trackers comes from
// ut_pex messages sent by connected peers (see peer.go); unlike the
// incumbent there is no listener, so peers cannot connect inbound —
// see the package documentation for that deliberate divergence.
package native

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anacrolix/torrent/bencode"
)

// peerSource streams candidate peer addresses ("host:port") for one
// infohash into out. It returns when it has no more peers to offer;
// ctx cancellation ends it early. Implementations must not block on
// out without also watching ctx.
type peerSource interface {
	Peers(ctx context.Context, ih [20]byte, out chan<- string)
}

// trackerTimeout bounds one tracker exchange (a UDP connect+announce
// round trip, or one HTTP GET). Trackers that cannot answer inside
// it are dead weight in a 45s fetch budget; the other trackers and
// PEX provide redundancy.
const trackerTimeout = 5 * time.Second

// announcePort is the port we claim in announces. We run no listener
// (outbound-only fetcher), so the value is nominal — it lands in
// other clients' peer lists but nothing needs to reach us on it for
// the fetch to work. 6881 is the conventional BitTorrent port.
const announcePort = 6881

// trackerSource announces to a fixed tracker list.
type trackerSource struct {
	trackers []string
	peerID   [20]byte
	http     *http.Client
	// sem bounds tracker exchanges in flight across all fetches: a
	// full worker pool announcing to the whole list at once would
	// otherwise open thousands of transient UDP sockets.
	sem chan struct{}
}

func newTrackerSource(trackers []string, peerID [20]byte) *trackerSource {
	return &trackerSource{
		trackers: trackers,
		peerID:   peerID,
		http:     &http.Client{Timeout: trackerTimeout + time.Second},
		sem:      make(chan struct{}, 512),
	}
}

// Peers announces to every tracker concurrently and streams the
// returned peers into out, deduplicated within this announce round.
func (s *trackerSource) Peers(ctx context.Context, ih [20]byte, out chan<- string) {
	var wg sync.WaitGroup
	var mu sync.Mutex
	sent := make(map[string]struct{})
	emit := func(peers []string) {
		for _, p := range peers {
			mu.Lock()
			if _, dup := sent[p]; dup {
				mu.Unlock()
				continue
			}
			sent[p] = struct{}{}
			mu.Unlock()
			select {
			case out <- p:
			case <-ctx.Done():
				return
			}
		}
	}
	for _, raw := range s.trackers {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		wg.Add(1)
		go func(raw string) {
			defer wg.Done()
			select {
			case s.sem <- struct{}{}:
				defer func() { <-s.sem }()
			case <-ctx.Done():
				return
			}
			var peers []string
			var err error
			if strings.HasPrefix(raw, "udp://") {
				peers, err = announceUDP(ctx, raw, ih, s.peerID)
			} else {
				peers, err = announceHTTP(ctx, s.http, raw, ih, s.peerID)
			}
			if err == nil {
				emit(peers)
			}
		}(raw)
	}
	wg.Wait()
}

// --- UDP tracker protocol (BEP 15) ---

const (
	udpConnectMagic = 0x41727101980
	actionConnect   = 0
	actionAnnounce  = 1
	actionError     = 3
	eventStarted    = 2
)

func randomTxn() uint32 {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return binary.BigEndian.Uint32(b[:])
}

// announceUDP runs one connect+announce exchange against a udp://
// tracker and returns its compact peer list.
func announceUDP(ctx context.Context, raw string, ih, peerID [20]byte) ([]string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Port() == "" {
		return nil, fmt.Errorf("tracker %s: no port", raw)
	}
	d := net.Dialer{Timeout: trackerTimeout}
	conn, err := d.DialContext(ctx, "udp", u.Host)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(trackerTimeout)); err != nil {
		return nil, err
	}

	txn := randomTxn()
	req := make([]byte, 16)
	binary.BigEndian.PutUint64(req[0:8], udpConnectMagic)
	binary.BigEndian.PutUint32(req[8:12], actionConnect)
	binary.BigEndian.PutUint32(req[12:16], txn)
	if _, err := conn.Write(req); err != nil {
		return nil, err
	}
	buf := make([]byte, 8192)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, err
	}
	if n < 16 {
		return nil, fmt.Errorf("tracker %s: short connect response", raw)
	}
	resp := buf[:n]
	if binary.BigEndian.Uint32(resp[0:4]) != actionConnect ||
		binary.BigEndian.Uint32(resp[4:8]) != txn {
		return nil, fmt.Errorf("tracker %s: bad connect response", raw)
	}
	connID := binary.BigEndian.Uint64(resp[8:16])

	txn = randomTxn()
	req = make([]byte, 98)
	binary.BigEndian.PutUint64(req[0:8], connID)
	binary.BigEndian.PutUint32(req[8:12], actionAnnounce)
	binary.BigEndian.PutUint32(req[12:16], txn)
	copy(req[16:36], ih[:])
	copy(req[36:56], peerID[:])
	// downloaded (56:64), left (64:72), uploaded (72:80) stay zero:
	// we hold nothing and want nothing but peers.
	binary.BigEndian.PutUint32(req[80:84], eventStarted)
	// ip (84:88) zero = tracker uses the source address.
	var key [4]byte
	_, _ = rand.Read(key[:])
	binary.BigEndian.PutUint32(req[88:92], binary.BigEndian.Uint32(key[:]))
	binary.BigEndian.PutUint32(req[92:96], uint32(0xFFFFFFFF)) // numwant: default
	binary.BigEndian.PutUint16(req[96:98], announcePort)
	if _, err := conn.Write(req); err != nil {
		return nil, err
	}
	n, err = conn.Read(buf)
	if err != nil {
		return nil, err
	}
	return parseUDPAnnounceResponse(buf[:n], txn)
}

// parseUDPAnnounceResponse validates and decodes a BEP 15 announce
// response. Split out of announceUDP so it is testable without a
// live tracker.
func parseUDPAnnounceResponse(resp []byte, txn uint32) ([]string, error) {
	if len(resp) < 8 {
		return nil, fmt.Errorf("short announce response")
	}
	switch action := binary.BigEndian.Uint32(resp[0:4]); action {
	case actionError:
		return nil, fmt.Errorf("tracker error: %s", string(resp[8:]))
	case actionAnnounce:
	default:
		return nil, fmt.Errorf("unexpected action %d", action)
	}
	if binary.BigEndian.Uint32(resp[4:8]) != txn {
		return nil, fmt.Errorf("announce response txn mismatch")
	}
	if len(resp) < 20 {
		return nil, fmt.Errorf("short announce response")
	}
	return parseCompactPeers(resp[20:], 4), nil
}

// --- HTTP tracker announce ---

// announceHTTP runs one HTTP(S) announce against an http:// tracker
// and returns its peer list (compact "peers", dict-model "peers", and
// "peers6" forms are all understood).
func announceHTTP(ctx context.Context, client *http.Client, raw string, ih, peerID [20]byte) ([]string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("info_hash", string(ih[:]))
	q.Set("peer_id", string(peerID[:]))
	q.Set("port", strconv.Itoa(announcePort))
	q.Set("uploaded", "0")
	q.Set("downloaded", "0")
	q.Set("left", "0")
	q.Set("compact", "1")
	q.Set("event", "started")
	q.Set("numwant", "200")
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tracker %s: HTTP %s", raw, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 128<<10))
	if err != nil {
		return nil, err
	}
	return parseHTTPAnnounceResponse(body)
}

// parseHTTPAnnounceResponse extracts peers from a bencoded HTTP
// announce response. Split out of announceHTTP so it is testable
// without a server.
func parseHTTPAnnounceResponse(body []byte) ([]string, error) {
	var dict map[string]interface{}
	if err := bencode.Unmarshal(body, &dict); err != nil {
		return nil, err
	}
	if failure, ok := dict["failure reason"]; ok {
		return nil, fmt.Errorf("tracker failure: %s", asString(failure))
	}
	var out []string
	switch peers := dict["peers"].(type) {
	case []byte: // compact model
		out = append(out, parseCompactPeers(peers, 4)...)
	case string:
		out = append(out, parseCompactPeers([]byte(peers), 4)...)
	case []interface{}: // dict model
		for _, item := range peers {
			m, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			ip := asString(m["ip"])
			port, ok := asInt(m["port"])
			if ip == "" || !ok || port <= 0 || port > 65535 {
				continue
			}
			out = append(out, net.JoinHostPort(ip, strconv.Itoa(port)))
		}
	}
	switch peers6 := dict["peers6"].(type) {
	case []byte:
		out = append(out, parseCompactPeers(peers6, 16)...)
	case string:
		out = append(out, parseCompactPeers([]byte(peers6), 16)...)
	}
	return out, nil
}

func asString(v interface{}) string {
	switch s := v.(type) {
	case []byte:
		return string(s)
	case string:
		return s
	}
	return ""
}

func asInt(v interface{}) (int, bool) {
	switch n := v.(type) {
	case int64:
		return int(n), true
	case uint64:
		return int(n), true
	case int:
		return n, true
	}
	return 0, false
}
