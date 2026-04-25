package mesh

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Dispatcher listens on a loopback address and accepts TCP streams arriving
// from xray's freedom outbound (which bridges VLESS+Reality inbound to here).
// Each stream is a single client session identified by its WG pubkey from
// the HELLO frame. Datagrams are looked up by destination pubkey and
// forwarded to the matching peer's session.
type Dispatcher struct {
	listenAddr string
	logger     *zap.Logger

	mu       sync.RWMutex
	sessions map[[PubkeyLen]byte]*session
}

type session struct {
	pubkey [PubkeyLen]byte
	conn   net.Conn
	// writeMu guards concurrent writes from other sessions' forwarders.
	// Reads happen on the session's own goroutine so they need no lock.
	writeMu sync.Mutex
}

func New(listenAddr string, logger *zap.Logger) *Dispatcher {
	return &Dispatcher{
		listenAddr: listenAddr,
		logger:     logger,
		sessions:   make(map[[PubkeyLen]byte]*session),
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

	d.logger.Info("mesh dispatcher listening", zap.String("addr", d.listenAddr))

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

	// HELLO must arrive within a short window — xray + reality handshake
	// already happened upstream, so if a peer doesn't announce itself
	// quickly something is wrong.
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))

	frameType, payload, err := readFrame(conn)
	if err != nil {
		d.logger.Debug("hello read failed", zap.Error(err))
		return
	}
	if frameType != FrameHello || len(payload) != PubkeyLen {
		d.logger.Debug("invalid hello",
			zap.Int("type", int(frameType)), zap.Int("len", len(payload)))
		return
	}

	var pk [PubkeyLen]byte
	copy(pk[:], payload)

	sess := &session{pubkey: pk, conn: conn}
	d.register(sess)
	defer d.unregister(sess)

	d.logger.Info("mesh session open", zap.String("peer", shortHex(pk[:])))

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

func (d *Dispatcher) register(s *session) {
	d.mu.Lock()
	defer d.mu.Unlock()
	// Boot out an older session on the same pubkey — the client has
	// reconnected and the old conn is stale.
	if old, ok := d.sessions[s.pubkey]; ok {
		_ = old.conn.Close()
	}
	d.sessions[s.pubkey] = s
}

func (d *Dispatcher) unregister(s *session) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if cur, ok := d.sessions[s.pubkey]; ok && cur == s {
		delete(d.sessions, s.pubkey)
	}
}

// forward writes a DATAGRAM frame to the destination peer's session, with
// the pubkey prefix rewritten to the sender's pubkey so the recipient
// learns who sent it.
func (d *Dispatcher) forward(src *session, dst [PubkeyLen]byte, wg []byte) {
	d.mu.RLock()
	target, ok := d.sessions[dst]
	d.mu.RUnlock()
	if !ok {
		// Silently drop: peer offline. WG will retransmit.
		return
	}

	// Reuse a stack-size prefix + heap just for the payload body. The
	// frame is assembled with writev-style two writes under the mutex to
	// avoid allocating a full copy of the WG ciphertext.
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
	return len(d.sessions)
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
