package native

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/anacrolix/utp"

	"dhtsearch/server/internal/metadata"
)

// This sandbox (and some CI) blocks outbound UDP even on loopback,
// so the uTP path cannot be exercised through real sockets here.
// Instead the fetcher's and the test peer's uTP sockets are joined
// by an in-memory PacketConn pair: the full uTP integration — dial,
// handshake, BEP 9 over the uTP stream — runs for real, only the
// wire is fake. On an unrestricted host the same code path runs over
// the real UDP socket created in New.

// pipePacketConn is one end of a connected in-memory packet pipe.
type pipePacketConn struct {
	local  *net.UDPAddr
	peer   *pipePacketConn
	inbox  chan pipePacket
	closed chan struct{}
	once   sync.Once
}

type pipePacket struct {
	data []byte
	from net.Addr
}

func newPipePair(a, b *net.UDPAddr) (ea, eb *pipePacketConn) {
	ea = &pipePacketConn{local: a, inbox: make(chan pipePacket, 64), closed: make(chan struct{})}
	eb = &pipePacketConn{local: b, inbox: make(chan pipePacket, 64), closed: make(chan struct{})}
	ea.peer = eb
	eb.peer = ea
	return ea, eb
}

func (c *pipePacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	select {
	case pkt := <-c.inbox:
		return copy(b, pkt.data), pkt.from, nil
	case <-c.closed:
		return 0, nil, io.ErrClosedPipe
	}
}

func (c *pipePacketConn) WriteTo(b []byte, _ net.Addr) (int, error) {
	pkt := pipePacket{data: append([]byte(nil), b...), from: c.local}
	select {
	case c.peer.inbox <- pkt:
		return len(b), nil
	case <-c.closed:
		return 0, io.ErrClosedPipe
	case <-c.peer.closed:
		return 0, io.ErrClosedPipe
	}
}

func (c *pipePacketConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func (c *pipePacketConn) LocalAddr() net.Addr              { return c.local }
func (c *pipePacketConn) SetDeadline(time.Time) error      { return nil }
func (c *pipePacketConn) SetReadDeadline(time.Time) error  { return nil }
func (c *pipePacketConn) SetWriteDeadline(time.Time) error { return nil }

// uTP end to end: the fetcher's TCP dial to the peer address is
// refused (nothing listens on TCP there), it falls back to uTP, and
// the metadata arrives over the uTP stream.
func TestFetchOverUTP(t *testing.T) {
	_, raw, hash := makeInfo(t, "utp-fixture", 40)

	fetcherAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 41001}
	peerAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 41002}
	endA, endB := newPipePair(fetcherAddr, peerAddr)

	peerSock, err := utp.NewSocketFromPacketConn(endB)
	if err != nil {
		t.Fatal(err)
	}
	defer peerSock.Close()
	peer := &testPeer{raw: raw, handshake: hash}
	go func() {
		for {
			conn, err := peerSock.Accept()
			if err != nil {
				return
			}
			peer.wg.Add(1)
			go func() {
				defer peer.wg.Done()
				peer.serve(conn)
			}()
		}
	}()
	defer peer.wg.Wait()

	f := newTestFetcher(t, metadata.Config{
		Workers: 2, Timeout: 15 * time.Second, MaxFiles: 50,
	}, staticSource{addrs: []string{peerAddr.String()}})
	// Swap the real UDP socket for the pipe end.
	f.utpSock.Close()
	sockA, err := utp.NewSocketFromPacketConn(endA)
	if err != nil {
		t.Fatal(err)
	}
	f.utpSock = sockA

	rec, ok := f.fetch(context.Background(), hash.HexString())
	if !ok {
		t.Fatal("fetch over uTP failed")
	}
	if rec.Name != "utp-fixture" || rec.FileCount != 40 {
		t.Fatalf("record = %+v", rec)
	}
	if got, _, _ := f.Stats(); got != 1 {
		t.Fatalf("fetched = %d, want 1", got)
	}
}
