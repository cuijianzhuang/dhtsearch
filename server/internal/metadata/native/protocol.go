// Wire-format helpers for the BitTorrent peer protocol (BEP 3), the
// extension protocol (BEP 10) and the metadata exchange (BEP 9). Only
// what a metadata fetcher needs: handshakes, message framing, and the
// bencoded dictionaries of the extended handshake / ut_metadata /
// ut_pex messages. Nothing here keeps state.
package native

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"

	"github.com/anacrolix/torrent/bencode"
)

// protocolName is the handshake's protocol string (BEP 3).
const protocolName = "BitTorrent protocol"

// Message IDs (BEP 3 / BEP 10).
const (
	msgChoke      = 0
	msgUnchoke    = 1
	msgInterested = 2
	msgBitfield   = 5
	msgExtended   = 20
)

// Extended message IDs inside an extended payload: 0 is the extended
// handshake itself; the values we advertise for the extensions we
// consume.
const (
	extHandshakeID = 0
	extMetadataID  = 1 // our advertised id for ut_metadata (BEP 9)
	extPexID       = 2 // our advertised id for ut_pex (BEP 11)
)

// ut_metadata message types (BEP 9).
const (
	metaRequest = 0
	metaData    = 1
	metaReject  = 2
)

// metadataPieceSize is the fixed BEP 9 piece length: metadata is
// transferred in 16 KiB blocks regardless of the torrent's own piece
// length.
const metadataPieceSize = 16384

// maxMetadataSize caps the metadata_size a peer may claim. Real info
// dicts run from a few hundred bytes to a few megabytes for torrents
// with tens of thousands of files; 16 MiB is far beyond any honest
// torrent and bounds what a lying peer can make us buffer.
const maxMetadataSize = 16 << 20

// maxMessageSize caps one peer-wire message. A metadata piece is
// 16 KiB plus a small dict; the largest legitimate other message is a
// bitfield, bounded by the torrent's piece count (a 1 TiB torrent at
// 256 KiB pieces still fits in ~512 KiB). 2 MiB leaves ample headroom
// while stopping a peer from making us allocate unbounded buffers.
const maxMessageSize = 2 << 20

// buildHandshake assembles our 68-byte peer handshake. The reserved
// bytes carry only the extension-protocol bit (byte 5, 0x10): like
// the anacrolix fetcher clients (NoDHT), we do not advertise the DHT
// port bit — this fetcher never serves the DHT.
func buildHandshake(infoHash [20]byte, peerID [20]byte) []byte {
	out := make([]byte, 0, 68)
	out = append(out, byte(len(protocolName)))
	out = append(out, protocolName...)
	reserved := make([]byte, 8)
	reserved[5] |= 0x10 // BEP 10 extension protocol
	out = append(out, reserved...)
	out = append(out, infoHash[:]...)
	out = append(out, peerID[:]...)
	return out
}

// peerHandshake is the parsed form of a remote peer's handshake.
type peerHandshake struct {
	reserved [8]byte
	infoHash [20]byte
	peerID   [20]byte
}

// supportsExtensions reports whether the peer set the BEP 10 bit;
// without it there is no ut_metadata and the connection is useless to
// us.
func (h *peerHandshake) supportsExtensions() bool { return h.reserved[5]&0x10 != 0 }

// readHandshake reads and parses exactly one peer handshake.
func readHandshake(r io.Reader) (*peerHandshake, error) {
	var lenBuf [1]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return nil, err
	}
	if int(lenBuf[0]) != len(protocolName) {
		return nil, fmt.Errorf("bad protocol string length %d", lenBuf[0])
	}
	rest := make([]byte, len(protocolName)+8+20+20)
	if _, err := io.ReadFull(r, rest); err != nil {
		return nil, err
	}
	if string(rest[:len(protocolName)]) != protocolName {
		return nil, fmt.Errorf("bad protocol string %q", rest[:len(protocolName)])
	}
	h := &peerHandshake{}
	copy(h.reserved[:], rest[len(protocolName):len(protocolName)+8])
	copy(h.infoHash[:], rest[len(protocolName)+8:len(protocolName)+28])
	copy(h.peerID[:], rest[len(protocolName)+28:])
	return h, nil
}

// writeMessage frames one message: 4-byte big-endian length covering
// the ID byte plus payload, then the ID, then the payload. A nil
// payload sends just the ID.
func writeMessage(w io.Writer, id byte, payload []byte) error {
	var hdr [5]byte
	binary.BigEndian.PutUint32(hdr[:4], uint32(1+len(payload)))
	hdr[4] = id
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if len(payload) > 0 {
		_, err := w.Write(payload)
		return err
	}
	return nil
}

// readMessage reads one framed message. It returns id == 0xFF for a
// keepalive (zero length). Payloads above maxMessageSize are a
// protocol error.
func readMessage(r io.Reader) (id byte, payload []byte, err error) {
	var lenBuf [4]byte
	if _, err = io.ReadFull(r, lenBuf[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(lenBuf[:])
	if n == 0 {
		return 0xFF, nil, nil // keepalive
	}
	if n > maxMessageSize {
		return 0, nil, fmt.Errorf("message length %d over cap %d", n, maxMessageSize)
	}
	buf := make([]byte, n)
	if _, err = io.ReadFull(r, buf); err != nil {
		return 0, nil, err
	}
	return buf[0], buf[1:], nil
}

// extendedHandshakeDict is what we send in our BEP 10 handshake: the
// extensions we consume, under the IDs the peer must use when sending
// them to us. We deliberately do not claim a metadata_size — we are
// the requester and do not serve metadata.
type extendedHandshakeDict struct {
	M map[string]int `bencode:"m"`
}

func buildExtendedHandshake() ([]byte, error) {
	return bencode.Marshal(extendedHandshakeDict{M: map[string]int{
		"ut_metadata": extMetadataID,
		"ut_pex":      extPexID,
	}})
}

// remoteExtensions is the part of a peer's extended handshake we act
// on: which ID to address ut_metadata requests to, which ID reaches
// its ut_pex, and the total metadata size.
type remoteExtensions struct {
	utMetadataID int
	utPexID      int
	metadataSize int
	hasMetadata  bool // peer advertised ut_metadata at all
}

// extHandshakeWire mirrors the peer's handshake dictionary for
// decoding. metadata_size is absent when the peer does not know the
// metadata yet (or is too old to send it).
type extHandshakeWire struct {
	M            map[string]int `bencode:"m"`
	MetadataSize *int           `bencode:"metadata_size"`
}

func parseExtendedHandshake(payload []byte) (*remoteExtensions, error) {
	var wire extHandshakeWire
	if err := bencode.Unmarshal(payload, &wire); err != nil {
		return nil, err
	}
	out := &remoteExtensions{utMetadataID: -1, utPexID: -1, metadataSize: -1}
	if id, ok := wire.M["ut_metadata"]; ok {
		out.utMetadataID = id
		out.hasMetadata = true
	}
	if id, ok := wire.M["ut_pex"]; ok {
		out.utPexID = id
	}
	if wire.MetadataSize != nil {
		out.metadataSize = *wire.MetadataSize
	}
	return out, nil
}

// metaHeader is the dictionary prefix of a ut_metadata message.
type metaHeader struct {
	MsgType   int `bencode:"msg_type"`
	Piece     int `bencode:"piece"`
	TotalSize int `bencode:"total_size"`
}

// buildMetaRequest encodes a ut_metadata request for one piece,
// addressed with the peer's advertised ut_metadata ID (the caller
// prepends that ID as the extended payload's first byte).
func buildMetaRequest(piece int) ([]byte, error) {
	return bencode.Marshal(metaHeader{MsgType: metaRequest, Piece: piece})
}

// splitDict splits an extended payload that starts with a bencoded
// dictionary into the decoded header and the trailing raw bytes (the
// piece data of a ut_metadata data message). bencode.Unmarshal does
// not report how many bytes it consumed; the streaming decoder does
// (its Offset), and it reads byte-exactly, so the split is precise.
func splitDict(payload []byte) (metaHeader, []byte, error) {
	var hdr metaHeader
	dec := bencode.NewDecoder(bytes.NewReader(payload))
	if err := dec.Decode(&hdr); err != nil {
		return hdr, nil, err
	}
	return hdr, payload[dec.Offset:], nil
}

// pexMessage is the part of a ut_pex message we consume: peers the
// remote side has seen for this torrent, in compact 6-byte form.
type pexMessage struct {
	Added []byte `bencode:"added"`
}

func parsePex(payload []byte) ([]string, error) {
	var msg pexMessage
	if err := bencode.Unmarshal(payload, &msg); err != nil {
		return nil, err
	}
	return parseCompactPeers(msg.Added, 4), nil
}

// parseCompactPeers decodes compact peer lists: ipLen-byte IP plus
// 2-byte big-endian port per entry (4 for IPv4 lists, 16 for IPv6
// "peers6" lists). Trailing partial entries are ignored.
func parseCompactPeers(b []byte, ipLen int) []string {
	stride := ipLen + 2
	if len(b) < stride {
		return nil
	}
	out := make([]string, 0, len(b)/stride)
	for off := 0; off+stride <= len(b); off += stride {
		ip := make(net.IP, ipLen)
		copy(ip, b[off:off+ipLen])
		port := binary.BigEndian.Uint16(b[off+ipLen : off+stride])
		if port == 0 {
			continue
		}
		out = append(out, net.JoinHostPort(ip.String(), strconv.Itoa(int(port))))
	}
	return out
}
