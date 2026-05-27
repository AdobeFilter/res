package mesh

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"
)

// issueTestHello builds a direct-connection HELLO payload
// [pubkey || expiry || hmac] the way the control-plane issues mesh-tokens.
// Mirrors the issuance side so the verifier is exercised end-to-end.
func issueTestHello(key []byte, pk [PubkeyLen]byte, expiry time.Time) []byte {
	payload := make([]byte, PubkeyLen+8)
	copy(payload, pk[:])
	binary.BigEndian.PutUint64(payload[PubkeyLen:], uint64(expiry.Unix()))
	h := hmac.New(sha256.New, key)
	h.Write(payload)
	return append(payload, h.Sum(nil)...)
}

// Two mock clients register under distinct pubkeys, one sends a DATAGRAM
// for the other, and we assert the recipient gets the payload with the
// sender's pubkey prefix (the dispatcher's rewrite step).
func TestDispatcherForwardsBetweenPeers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	d := New(addr, nil, zaptest.NewLogger(t))
	go func() {
		if err := d.ListenAndServe(ctx); err != nil {
			t.Log("dispatcher stopped:", err)
		}
	}()

	// Wait briefly for the listener to come up.
	waitForListener(t, addr)

	pkA := fillPK(0xAA)
	pkB := fillPK(0xBB)

	connA := dial(t, addr)
	defer connA.Close()
	connB := dial(t, addr)
	defer connB.Close()

	if err := writeFrame(connA, FrameHello, pkA[:]); err != nil {
		t.Fatal(err)
	}
	if err := writeFrame(connB, FrameHello, pkB[:]); err != nil {
		t.Fatal(err)
	}

	// Let registrations settle.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if d.ActiveSessions() == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if d.ActiveSessions() != 2 {
		t.Fatalf("expected 2 sessions, got %d", d.ActiveSessions())
	}

	// A sends a DATAGRAM to B with fake WG ciphertext.
	wgPayload := []byte("ciphertext-from-A-to-B")
	frame := append(pkB[:], wgPayload...)
	if err := writeFrame(connA, FrameDatagram, frame); err != nil {
		t.Fatal(err)
	}

	// B should see a DATAGRAM with pkA prefix + wgPayload.
	_ = connB.SetReadDeadline(time.Now().Add(2 * time.Second))
	ft, payload, err := readTestFrame(connB)
	if err != nil {
		t.Fatal(err)
	}
	if ft != FrameDatagram {
		t.Fatalf("want DATAGRAM, got type 0x%x", ft)
	}
	if !bytes.Equal(payload[:PubkeyLen], pkA[:]) {
		t.Fatalf("want src prefix %x, got %x", pkA[:], payload[:PubkeyLen])
	}
	if !bytes.Equal(payload[PubkeyLen:], wgPayload) {
		t.Fatalf("payload mismatch: want %q, got %q", wgPayload, payload[PubkeyLen:])
	}
}

// Multi-stream: a client opens several streams under the same pubkey (that's
// how MeshBind parallelises the relay leg). All must coexist as separate
// sessions — not evict each other.
func TestDispatcherAllowsMultipleStreamsPerPubkey(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()

	d := New(addr, nil, zaptest.NewLogger(t))
	go d.ListenAndServe(ctx)
	waitForListener(t, addr)

	pk := fillPK(0x42)

	first := dial(t, addr)
	_ = writeFrame(first, FrameHello, pk[:])
	defer first.Close()
	waitFor(t, func() bool { return d.ActiveSessions() == 1 })

	second := dial(t, addr)
	_ = writeFrame(second, FrameHello, pk[:])
	defer second.Close()

	// Both streams stay registered, and the first is NOT force-closed.
	waitFor(t, func() bool { return d.ActiveSessions() == 2 })
	_ = first.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	_, err := first.Read(make([]byte, 1))
	if ne, ok := err.(net.Error); !ok || !ne.Timeout() {
		t.Fatalf("first stream should stay open (timeout on read), got err=%v", err)
	}
}

// A flood of streams under one pubkey is capped; excess evicts the oldest.
func TestDispatcherCapsSessionsPerPubkey(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()

	d := New(addr, nil, zaptest.NewLogger(t))
	go d.ListenAndServe(ctx)
	waitForListener(t, addr)

	pk := fillPK(0x42)
	for i := 0; i < maxSessionsPerPubkey+5; i++ {
		c := dial(t, addr)
		_ = writeFrame(c, FrameHello, pk[:])
		defer c.Close()
	}

	// Never exceeds the cap.
	waitFor(t, func() bool { return d.ActiveSessions() == maxSessionsPerPubkey })
	time.Sleep(100 * time.Millisecond)
	if got := d.ActiveSessions(); got != maxSessionsPerPubkey {
		t.Fatalf("expected cap %d, got %d", maxSessionsPerPubkey, got)
	}
}

// Token verification: a well-formed token for a pubkey passes; tampering with
// the pubkey, the key, or the expiry fails.
func TestVerifyMeshToken(t *testing.T) {
	key := []byte("shared-mesh-secret")
	pk := fillPK(0x42)
	now := time.Unix(1_700_000_000, 0)

	good := issueTestHello(key, pk, now.Add(time.Hour))
	if _, err := verifyMeshToken(key, good, now); err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}

	// Expired.
	expired := issueTestHello(key, pk, now.Add(-time.Hour))
	if _, err := verifyMeshToken(key, expired, now); err == nil {
		t.Fatal("expired token accepted")
	}

	// Wrong key.
	if _, err := verifyMeshToken([]byte("other-secret"), good, now); err == nil {
		t.Fatal("token verified under wrong key")
	}

	// Tampered pubkey (attacker swaps in a victim's pubkey but keeps the mac).
	tampered := append([]byte(nil), good...)
	tampered[0] ^= 0xFF
	if _, err := verifyMeshToken(key, tampered, now); err == nil {
		t.Fatal("token accepted after pubkey tamper")
	}

	// Missing token bytes.
	if _, err := verifyMeshToken(key, pk[:], now); err == nil {
		t.Fatal("bare pubkey accepted as a token")
	}
}

// DATAGRAMs destined for a peer with no active session are silently dropped.
func TestDispatcherDropsUnroutableDatagram(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()

	d := New(addr, nil, zaptest.NewLogger(t))
	go d.ListenAndServe(ctx)
	waitForListener(t, addr)

	pkA := fillPK(0x11)
	pkGhost := fillPK(0x99)

	conn := dial(t, addr)
	defer conn.Close()
	_ = writeFrame(conn, FrameHello, pkA[:])
	waitFor(t, func() bool { return d.ActiveSessions() == 1 })

	// Send DATAGRAM to pkGhost who was never connected. Expect no panic,
	// no reply, dispatcher keeps running.
	frame := append(pkGhost[:], []byte("lost-packet")...)
	if err := writeFrame(conn, FrameDatagram, frame); err != nil {
		t.Fatal(err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	buf := make([]byte, 1)
	n, _ := conn.Read(buf)
	if n != 0 {
		t.Fatalf("expected no reply, got %d bytes", n)
	}
	if d.ActiveSessions() != 1 {
		t.Fatalf("sender session should remain, got %d", d.ActiveSessions())
	}
}

// --- helpers ---

func writeFrame(w io.Writer, ft byte, payload []byte) error {
	var hdr [3]byte
	binary.BigEndian.PutUint16(hdr[0:2], uint16(1+len(payload)))
	hdr[2] = ft
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func readTestFrame(r io.Reader) (byte, []byte, error) {
	var lenBuf [2]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return 0, nil, err
	}
	n := int(binary.BigEndian.Uint16(lenBuf[:]))
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return 0, nil, err
	}
	return body[0], body[1:], nil
}

func dial(t *testing.T, addr string) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func waitForListener(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			c.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("dispatcher not listening on %s", addr)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition never satisfied")
}

func fillPK(b byte) [PubkeyLen]byte {
	var pk [PubkeyLen]byte
	for i := range pk {
		pk[i] = b
	}
	return pk
}
