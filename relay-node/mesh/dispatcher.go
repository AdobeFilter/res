package mesh

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// maxSessionsPerPubkey bounds how many concurrent streams one client may open.
// Multi-stream MeshBind opens a handful (N=4-8) to parallelise the relay leg;
// the cap keeps a buggy or hostile client from exhausting memory. Excess
// sessions evict the oldest.
const maxSessionsPerPubkey = 16

// Dispatcher accepts TCP streams carrying length-prefixed mesh frames and
// forwards WG datagrams between peers keyed by destination pubkey. It never
// decrypts WG payloads.
//
// Two ingress paths land here on the same listener:
//   - loopback: xray's freedom outbound bridges a (Reality-fronted) VLESS
//     stream to 127.0.0.1 — already authenticated by xray's VLESS UUID, so
//     these skip the mesh-token check.
//   - direct public: a client reaches the listener straight through its
//     exit-node's freedom outbound, with no relay-side VLESS. These must
//     present a valid mesh-token in HELLO (when authKey is configured),
//     because the port is open to the internet.
type Dispatcher struct {
	listenAddr string
	authKey    []byte
	logger     *zap.Logger

	mu       sync.RWMutex
	sessions map[[PubkeyLen]byte][]*session
	rr       atomic.Uint64 // round-robin cursor for spreading forwards across a peer's streams
}

type session struct {
	pubkey [PubkeyLen]byte
	conn   net.Conn
	// writeMu guards concurrent writes from other sessions' forwarders.
	// Reads happen on the session's own goroutine so they need no lock.
	writeMu sync.Mutex
}

// New builds a dispatcher. authKey is the shared HMAC secret used to verify
// mesh-tokens on direct connections; an empty key disables token enforcement
// (dogfood mode — anyone reaching the port may register a pubkey).
func New(listenAddr string, authKey []byte, logger *zap.Logger) *Dispatcher {
	return &Dispatcher{
		listenAddr: listenAddr,
		authKey:    authKey,
		logger:     logger,
		sessions:   make(map[[PubkeyLen]byte][]*session),
	}
}

// ListenAndServe blocks until ctx is cancelled or the listener errors.
// Accepts TCP connections, handles each in its own goroutine.
func (d *Dispatcher) ListenAndServe(ctx context.Context) error {
	lc := net.ListenConfig{}
	ln, err := lc.Listen(ctx, "tcp", d.listenAddr)
	if err != nil {
		return fmt.Errorf("mesh dispatcher listen %s: %w", d.listenAddr, err)
	}
	defer ln.Close()

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	enforced := len(d.authKey) > 0
	d.logger.Info("mesh dispatcher listening",
		zap.String("addr", d.listenAddr),
		zap.Bool("token_auth", enforced))
	if !enforced {
		d.logger.Warn("MESH_AUTH_KEY unset: direct connections accepted without a token — set it before exposing the relay publicly")
	}

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			d.logger.Warn("mesh accept error", zap.Error(err))
			continue
		}
		go d.handleConn(ctx, conn)
	}
}

func (d *Dispatcher) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	// HELLO must arrive within a short window — the xray + reality handshake
	// (or the direct dial) already happened upstream.
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))

	frameType, payload, err := readFrame(conn)
	if err != nil {
		d.logger.Debug("hello read failed", zap.Error(err))
		return
	}
	if frameType != FrameHello {
		d.logger.Debug("first frame not HELLO", zap.Int("type", int(frameType)))
		return
	}

	pk, err := d.authenticate(conn, payload)
	if err != nil {
		d.logger.Debug("hello rejected",
			zap.String("remote", conn.RemoteAddr().String()), zap.Error(err))
		return
	}

	sess := &session{pubkey: pk, conn: conn}
	d.register(sess)
	defer d.unregister(sess)

	d.logger.Info("mesh session open",
		zap.String("peer", shortHex(pk[:])),
		zap.String("remote", conn.RemoteAddr().String()))

	// Datagrams from here on: no read deadline (WG keepalive carries the
	// session). We let the caller (or remote) decide when to disconnect.
	_ = conn.SetReadDeadline(time.Time{})

	for {
		if ctx.Err() != nil {
			return
		}
		frameType, payload, err := readFrame(conn)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				d.logger.Debug("mesh read error",
					zap.String("peer", shortHex(pk[:])), zap.Error(err))
			}
			return
		}
		if frameType != FrameDatagram {
			d.logger.Debug("unexpected frame",
				zap.Int("type", int(frameType)),
				zap.String("peer", shortHex(pk[:])))
			continue
		}
		if len(payload) < PubkeyLen+1 {
			continue
		}

		var dstPK [PubkeyLen]byte
		copy(dstPK[:], payload[:PubkeyLen])
		wg := payload[PubkeyLen:]

		d.forward(sess, dstPK, wg)
	}
}

// authenticate validates a HELLO payload and returns the client pubkey.
//
// Loopback connections are trusted (xray already enforced its VLESS UUID) and
// only need the bare 32-byte pubkey. Direct connections must additionally
// carry a mesh-token — [pubkey(32) || expiry(8, big-endian unix) || hmac(32)]
// — verified against authKey, unless authKey is empty (token enforcement off).
func (d *Dispatcher) authenticate(conn net.Conn, payload []byte) ([PubkeyLen]byte, error) {
	var pk [PubkeyLen]byte
	if len(payload) < PubkeyLen {
		return pk, fmt.Errorf("hello too short: %d", len(payload))
	}
	copy(pk[:], payload[:PubkeyLen])

	if isLoopback(conn.RemoteAddr()) || len(d.authKey) == 0 {
		return pk, nil
	}
	// Direct connection with enforcement on: require and verify the token.
	return verifyMeshToken(d.authKey, payload, time.Now())
}

// verifyMeshToken checks a HELLO payload of the form
// [pubkey(32) || expiry(8, big-endian unix seconds) || hmac-sha256(32)] against
// the shared key. The HMAC covers pubkey||expiry, binding the token to a
// specific pubkey so a holder of one client's token can't register another's.
func verifyMeshToken(authKey, payload []byte, now time.Time) ([PubkeyLen]byte, error) {
	var pk [PubkeyLen]byte
	const tokenLen = 8 + sha256.Size // expiry || hmac
	if len(payload) < PubkeyLen+tokenLen {
		return pk, errors.New("missing mesh-token on direct connection")
	}
	copy(pk[:], payload[:PubkeyLen])

	signed := payload[:PubkeyLen+8] // pubkey || expiry — the HMAC input
	expiry := int64(binary.BigEndian.Uint64(payload[PubkeyLen : PubkeyLen+8]))
	mac := payload[PubkeyLen+8 : PubkeyLen+8+sha256.Size]

	if now.Unix() > expiry {
		return pk, errors.New("mesh-token expired")
	}
	h := hmac.New(sha256.New, authKey)
	h.Write(signed)
	if !hmac.Equal(mac, h.Sum(nil)) {
		return pk, errors.New("mesh-token signature mismatch")
	}
	return pk, nil
}

func isLoopback(addr net.Addr) bool {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (d *Dispatcher) register(s *session) {
	d.mu.Lock()
	defer d.mu.Unlock()
	list := d.sessions[s.pubkey]
	// Evict the oldest if a client floods us with streams.
	if len(list) >= maxSessionsPerPubkey {
		_ = list[0].conn.Close()
		list = list[1:]
	}
	d.sessions[s.pubkey] = append(list, s)
}

func (d *Dispatcher) unregister(s *session) {
	d.mu.Lock()
	defer d.mu.Unlock()
	list := d.sessions[s.pubkey]
	for i, cur := range list {
		if cur == s {
			list = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(list) == 0 {
		delete(d.sessions, s.pubkey)
	} else {
		d.sessions[s.pubkey] = list
	}
}

// forward writes a DATAGRAM frame to one of the destination peer's sessions,
// round-robined so a multi-stream peer receives across all its TCP streams
// (independent congestion windows = the throughput win). The pubkey prefix is
// rewritten to the sender's so the recipient learns who sent it.
func (d *Dispatcher) forward(src *session, dst [PubkeyLen]byte, wg []byte) {
	d.mu.RLock()
	list := d.sessions[dst]
	var target *session
	if n := len(list); n > 0 {
		target = list[int(d.rr.Add(1)%uint64(n))]
	}
	d.mu.RUnlock()
	if target == nil {
		// Silently drop: peer offline. WG will retransmit.
		return
	}

	// Assemble header then body under the mutex with two writes to avoid
	// copying the whole WG ciphertext.
	hdr := make([]byte, 3+PubkeyLen)
	frameLen := 1 + PubkeyLen + len(wg)
	binary.BigEndian.PutUint16(hdr[0:2], uint16(frameLen))
	hdr[2] = FrameDatagram
	copy(hdr[3:], src.pubkey[:])

	target.writeMu.Lock()
	defer target.writeMu.Unlock()
	_ = target.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := target.conn.Write(hdr); err != nil {
		_ = target.conn.Close()
		return
	}
	if _, err := target.conn.Write(wg); err != nil {
		_ = target.conn.Close()
		return
	}
	_ = target.conn.SetWriteDeadline(time.Time{})
}

// ActiveSessions returns the current session count (for metrics/logging).
func (d *Dispatcher) ActiveSessions() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	n := 0
	for _, list := range d.sessions {
		n += len(list)
	}
	return n
}

// readFrame pulls one length-prefixed frame from conn.
func readFrame(conn net.Conn) (byte, []byte, error) {
	var lenBuf [2]byte
	if _, err := io.ReadFull(conn, lenBuf[:]); err != nil {
		return 0, nil, err
	}
	frameLen := int(binary.BigEndian.Uint16(lenBuf[:]))
	if frameLen < 1 || frameLen > MaxFrame {
		return 0, nil, fmt.Errorf("frame length out of range: %d", frameLen)
	}
	body := make([]byte, frameLen)
	if _, err := io.ReadFull(conn, body); err != nil {
		return 0, nil, err
	}
	return body[0], body[1:], nil
}

func shortHex(b []byte) string {
	const hex = "0123456789abcdef"
	n := 4
	if len(b) < n {
		n = len(b)
	}
	out := make([]byte, n*2)
	for i := 0; i < n; i++ {
		out[i*2] = hex[b[i]>>4]
		out[i*2+1] = hex[b[i]&0x0f]
	}
	return string(out)
}
