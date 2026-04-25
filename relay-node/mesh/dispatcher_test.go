package mesh

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"
)

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

	d := New(addr, zaptest.NewLogger(t))
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

// A reconnects under the same pubkey; the old session must be evicted so
// there's exactly one entry in the table.
func TestDispatcherReplacesExistingSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()

	d := New(addr, zaptest.NewLogger(t))
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

	// Old conn must be force-closed; the count stays at 1.
	waitFor(t, func() bool {
		_ = first.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		_, err := first.Read(make([]byte, 1))
		return err != nil && d.ActiveSessions() == 1
	})
}

// DATAGRAMs destined for a peer with no active session are silently dropped.
func TestDispatcherDropsUnroutableDatagram(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()

	d := New(addr, zaptest.NewLogger(t))
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
