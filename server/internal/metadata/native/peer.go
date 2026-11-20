// Peer sessions: one goroutine per connection attempt, speaking just
// enough of the peer protocol to pull the metadata (BEP 9) out of a
// peer. A session owns its connection outright — no state is shared
// between sessions except through the attempt's mutex-guarded
// callbacks — so a stuck or hostile peer can only ever cost its own
// goroutine, never another fetch's.
package native

import (
	"context"
	"net"
	"time"
)

// dialTimeout bounds one connection attempt (TCP, then uTP). Peers
// that cannot be reached inside it are not going to serve metadata
// inside the fetch budget either.
const dialTimeout = 5 * time.Second

// handshakeTimeout bounds the post-connect handshake exchange.
const handshakeTimeout = 10 * time.Second

// idleTimeout bounds the gap between messages from an established
// peer. A peer that handshakes and then goes silent would otherwise
// hold a fan-out slot for the rest of the budget; 16 KiB of metadata
// takes a live peer well under a second to send, so 20s of silence
// means the peer is done helping.
const idleTimeout = 20 * time.Second

// dialPeer connects to addr, preferring TCP and falling back to uTP
// when the fetcher has a uTP socket and TCP failed. Many swarm peers
// listen only on uTP; the incumbent reaches them through the torrent
// client's uTP transport, so the native fetcher needs the same reach
// to keep the peer supply equivalent.
func (f *Fetcher) dialPeer(ctx context.Context, addr string) (net.Conn, error) {
	dctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	conn, err := f.tcpDialer.DialContext(dctx, "tcp", addr)
	if err == nil {
		return conn, nil
	}
	if f.utpSock != nil {
		if uconn, uerr := f.utpSock.DialContext(dctx, "udp", addr); uerr == nil {
			return uconn, nil
		}
	}
	return nil, err
}

// peerSession is one connection's metadata pull. The zero fields are
// the assembly state, filled in once the peer's extended handshake
// (or its first data message) reveals the metadata size.
type peerSession struct {
	a    *attempt
	conn net.Conn
	addr string // the pool address this session was dialed for (hint attribution)

	ext       *remoteExtensions
	size      int
	pieces    int
	buf       []byte
	have      []bool
	got       int
	setupDone bool
}

// runSession executes one connection attempt end to end: dial,
// handshake, metadata pull. It reports through the attempt's
// callbacks and always releases its fan-out slot on return.
func (a *attempt) runSession(ctx context.Context, addr string) {
	defer a.sessionEnded()
	conn, err := a.f.dialPeer(ctx, addr)
	if err != nil {
		return
	}
	a.registerConn(conn)
	defer a.unregisterConn(conn)
	defer conn.Close()
	s := &peerSession{a: a, conn: conn, addr: addr}
	s.run(ctx)
}

func (s *peerSession) run(ctx context.Context) {
	a := s.a
	// The handshake exchange gets one hard deadline; afterwards the
	// read loop runs on a rolling idle deadline, and the attempt's
	// context (via conn close at shutdown) bounds everything.
	_ = s.conn.SetDeadline(time.Now().Add(handshakeTimeout))
	if _, err := s.conn.Write(buildHandshake(a.ih, a.f.peerID)); err != nil {
		return
	}
	remote, err := readHandshake(s.conn)
	if err != nil {
		return
	}
	if remote.infoHash != a.ih {
		return // connected to something that is not serving this torrent
	}
	// The handshake is complete: this is the prescreen's "a peer
	// connected" signal, the equivalent of the incumbent's
	// ActivePeers() > 0.
	a.sessionConnected(s.addr)
	if !remote.supportsExtensions() {
		return
	}
	hs, err := buildExtendedHandshake()
	if err != nil {
		return
	}
	payload := append([]byte{extHandshakeID}, hs...)
	if err := writeMessage(s.conn, msgExtended, payload); err != nil {
		return
	}
	_ = s.conn.SetDeadline(time.Time{})

	for {
		if ctx.Err() != nil {
			return
		}
		_ = s.conn.SetReadDeadline(time.Now().Add(idleTimeout))
		id, msg, err := readMessage(s.conn)
		if err != nil {
			return
		}
		if id == 0xFF {
			continue // keepalive
		}
		if id != msgExtended || len(msg) == 0 {
			continue // bitfield/have/choke/etc. carry nothing we need
		}
		switch msg[0] {
		case extHandshakeID:
			if !s.onExtHandshake(msg[1:]) {
				return
			}
		case extMetadataID:
			if !s.onMetadata(msg[1:]) {
				return
			}
		case extPexID:
			if peers, err := parsePex(msg[1:]); err == nil {
				a.addPeers(peers)
			}
		}
	}
}

// onExtHandshake records the peer's extension offer and kicks off
// piece requests. It reports whether the session should continue.
func (s *peerSession) onExtHandshake(payload []byte) bool {
	if s.ext != nil {
		return true // a second handshake changes nothing
	}
	ext, err := parseExtendedHandshake(payload)
	if err != nil {
		return false
	}
	s.ext = ext
	if !ext.hasMetadata || ext.utMetadataID < 0 {
		return false // no ut_metadata: this peer cannot serve us
	}
	if ext.metadataSize >= 0 {
		if !s.setup(ext.metadataSize) {
			return false
		}
		return s.requestMissing()
	}
	// The peer offers ut_metadata but did not state a size (it may
	// not have the metadata itself, or predates the convention).
	// Ask for piece 0 blind: a data reply carries total_size, which
	// sets the assembly up; a reject ends the session just as fast.
	return s.requestPieces(0)
}

// setup sizes the assembly for a metadata blob of total bytes. A
// size outside (0, maxMetadataSize] is a peer lying about the
// torrent: it poisons the attempt (see attempt.markPoisoned) and
// ends the session.
func (s *peerSession) setup(total int) bool {
	if total <= 0 || total > maxMetadataSize {
		s.a.markPoisoned()
		return false
	}
	s.size = total
	s.pieces = (total + metadataPieceSize - 1) / metadataPieceSize
	s.buf = make([]byte, total)
	s.have = make([]bool, s.pieces)
	s.setupDone = true
	return true
}

// requestMissing asks for every piece not yet received.
func (s *peerSession) requestMissing() bool {
	for i := 0; i < s.pieces; i++ {
		if s.have[i] {
			continue
		}
		if !s.requestPieces(i) {
			return false
		}
	}
	return true
}

func (s *peerSession) requestPieces(indices ...int) bool {
	for _, i := range indices {
		req, err := buildMetaRequest(i)
		if err != nil {
			return false
		}
		payload := append([]byte{byte(s.ext.utMetadataID)}, req...)
		if err := writeMessage(s.conn, msgExtended, payload); err != nil {
			return false
		}
	}
	return true
}

// onMetadata handles one ut_metadata message addressed to us. It
// reports whether the session should continue.
func (s *peerSession) onMetadata(payload []byte) bool {
	hdr, data, err := splitDict(payload)
	if err != nil {
		return false
	}
	switch hdr.MsgType {
	case metaReject:
		return false // the peer will not serve metadata; try another
	case metaRequest:
		return true // we serve nothing; ignore
	case metaData:
	default:
		return true
	}
	if !s.setupDone {
		// The reply to our blind piece-0 request: total_size sets up
		// the assembly, then we ask for everything else.
		if !s.setup(hdr.TotalSize) {
			return false
		}
		if !s.storePiece(hdr.Piece, data) {
			return false
		}
		if s.got == s.pieces {
			s.a.complete(s.buf)
			return false
		}
		return s.requestMissing()
	}
	if hdr.TotalSize != s.size {
		// The peer disagrees with its own handshake about the size
		// of this torrent's metadata: nothing it sends can be
		// trusted to hash to the requested infohash.
		s.a.markPoisoned()
		return false
	}
	if !s.storePiece(hdr.Piece, data) {
		return false
	}
	if s.got == s.pieces {
		s.a.complete(s.buf)
		return false
	}
	return true
}

// storePiece records one metadata piece. A piece whose length does
// not match the size-derived expectation is a protocol violation:
// the session ends, but the attempt is not poisoned — the peer may
// just be sloppy, and other peers are unaffected.
func (s *peerSession) storePiece(piece int, data []byte) bool {
	if piece < 0 || piece >= s.pieces {
		return false
	}
	off := piece * metadataPieceSize
	want := metadataPieceSize
	if rem := s.size - off; rem < want {
		want = rem
	}
	if len(data) != want {
		return false
	}
	if !s.have[piece] {
		copy(s.buf[off:off+want], data)
		s.have[piece] = true
		s.got++
	}
	return true
}
