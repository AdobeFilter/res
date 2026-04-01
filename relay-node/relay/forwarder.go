package relay

import (
	"context"
	"net"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// Forwarder relays encrypted packets between nodes.
// It operates at Layer 4 — packets are opaque encrypted blobs.
// The relay NEVER has access to WireGuard private keys.
type Forwarder struct {
	sessions    *SessionTable
	conn        *net.UDPConn
	logger      *zap.Logger
	totalBytes  atomic.Uint64
}

func NewForwarder(sessions *SessionTable, logger *zap.Logger) *Forwarder {
	return &Forwarder{
		sessions: sessions,
		logger:   logger,
	}
}

// ListenAndServe starts the UDP relay on the given address.
func (f *Forwarder) ListenAndServe(ctx context.Context, addr string) error {
	udpAddr, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		return err
	}

	conn, err := net.ListenUDP("udp4", udpAddr)
	if err != nil {
		return err
	}
	f.conn = conn

	f.logger.Info("UDP relay listening", zap.String("addr", addr))

	// Session cleanup goroutine
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				removed := f.sessions.CleanStale(2 * time.Minute)
				if removed > 0 {
					f.logger.Debug("cleaned stale sessions", zap.Int("removed", removed))
				}
			}
		}
	}()

	buf := make([]byte, 65535)
	for {
		select {
		case <-ctx.Done():
			conn.Close()
			return nil
		default:
		}

		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, remoteAddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			f.logger.Error("read error", zap.Error(err))
			continue
		}

		f.totalBytes.Add(uint64(n))
		go f.handlePacket(buf[:n], remoteAddr)
	}
}

func (f *Forwarder) handlePacket(data []byte, srcAddr *net.UDPAddr) {
	// The relay protocol: first 6 bytes of the payload contain the destination
	// IP (4 bytes) + port (2 bytes). The rest is the encrypted WireGuard packet.
	if len(data) < 7 {
		return
	}

	dstIP := net.IPv4(data[0], data[1], data[2], data[3])
	dstPort := int(data[4])<<8 | int(data[5])
	payload := data[6:]

	dstAddr := &net.UDPAddr{IP: dstIP, Port: dstPort}

	session, isNew := f.sessions.GetOrCreate(srcAddr, dstAddr)
	if session == nil {
		f.logger.Warn("relay at capacity, dropping packet")
		return
	}

	if isNew {
		f.logger.Debug("new relay session",
			zap.String("src", srcAddr.String()),
			zap.String("dst", dstAddr.String()))
	}

	// Forward the encrypted payload (without the 6-byte header)
	if _, err := f.conn.WriteToUDP(payload, dstAddr); err != nil {
		f.logger.Debug("forward failed", zap.Error(err))
	}

	session.BytesOut += uint64(len(payload))
}

// Stats returns relay statistics.
func (f *Forwarder) Stats() (activeSessions int, totalBytes uint64) {
	return f.sessions.Count(), f.totalBytes.Load()
}
