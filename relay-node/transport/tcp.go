package transport

import (
	"context"
	"io"
	"net"
	"time"

	"go.uber.org/zap"
)

// TCPRelay provides a TCP fallback transport for UDP-blocked networks.
// Clients send framed packets: [2-byte length][payload].
type TCPRelay struct {
	logger *zap.Logger
}

func NewTCPRelay(logger *zap.Logger) *TCPRelay {
	return &TCPRelay{logger: logger}
}

// ListenAndServe starts the TCP relay on the given address.
func (t *TCPRelay) ListenAndServe(ctx context.Context, addr string) error {
	listener, err := net.Listen("tcp4", addr)
	if err != nil {
		return err
	}

	t.logger.Info("TCP relay listening", zap.String("addr", addr))

	go func() {
		<-ctx.Done()
		listener.Close()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				t.logger.Error("accept error", zap.Error(err))
				continue
			}
		}

		go t.handleConnection(ctx, conn)
	}
}

func (t *TCPRelay) handleConnection(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	// Read destination header: [4 bytes IP][2 bytes port]
	header := make([]byte, 6)
	if _, err := io.ReadFull(conn, header); err != nil {
		t.logger.Debug("read dest header failed", zap.Error(err))
		return
	}

	dstIP := net.IPv4(header[0], header[1], header[2], header[3])
	dstPort := int(header[4])<<8 | int(header[5])
	dstAddr := &net.UDPAddr{IP: dstIP, Port: dstPort}

	t.logger.Debug("TCP relay connection",
		zap.String("src", conn.RemoteAddr().String()),
		zap.String("dst", dstAddr.String()))

	// Open UDP connection to destination
	udpConn, err := net.DialUDP("udp4", nil, dstAddr)
	if err != nil {
		t.logger.Error("dial destination failed", zap.Error(err))
		return
	}
	defer udpConn.Close()

	// Bidirectional relay: TCP <-> UDP
	// TCP -> UDP
	go func() {
		buf := make([]byte, 65535)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			conn.SetReadDeadline(time.Now().Add(60 * time.Second))

			// Read framed packet: [2 bytes length][payload]
			lenBuf := make([]byte, 2)
			if _, err := io.ReadFull(conn, lenBuf); err != nil {
				return
			}
			pktLen := int(lenBuf[0])<<8 | int(lenBuf[1])
			if pktLen > len(buf) {
				return
			}

			if _, err := io.ReadFull(conn, buf[:pktLen]); err != nil {
				return
			}

			udpConn.Write(buf[:pktLen])
		}
	}()

	// UDP -> TCP
	buf := make([]byte, 65535)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		udpConn.SetReadDeadline(time.Now().Add(60 * time.Second))
		n, err := udpConn.Read(buf)
		if err != nil {
			return
		}

		// Write framed: [2 bytes length][payload]
		frame := make([]byte, 2+n)
		frame[0] = byte(n >> 8)
		frame[1] = byte(n)
		copy(frame[2:], buf[:n])

		conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if _, err := conn.Write(frame); err != nil {
			return
		}
	}
}
