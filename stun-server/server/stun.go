package server

import (
	"net"

	"github.com/pion/stun/v2"
	"go.uber.org/zap"
)

// STUNServer handles STUN Binding Requests and returns the client's reflexive address.
type STUNServer struct {
	logger *zap.Logger
}

func New(logger *zap.Logger) *STUNServer {
	return &STUNServer{logger: logger}
}

// ListenAndServe starts the STUN server on the given UDP address.
func (s *STUNServer) ListenAndServe(addr string) error {
	conn, err := net.ListenPacket("udp4", addr)
	if err != nil {
		return err
	}
	defer conn.Close()

	s.logger.Info("STUN server listening", zap.String("addr", addr))

	buf := make([]byte, 1500)
	for {
		n, remoteAddr, err := conn.ReadFrom(buf)
		if err != nil {
			s.logger.Error("read error", zap.Error(err))
			continue
		}

		go s.handlePacket(conn, remoteAddr, buf[:n])
	}
}

func (s *STUNServer) handlePacket(conn net.PacketConn, remoteAddr net.Addr, data []byte) {
	msg := &stun.Message{Raw: data}
	if err := msg.Decode(); err != nil {
		s.logger.Debug("non-STUN packet received", zap.Error(err))
		return
	}

	if msg.Type != stun.BindingRequest {
		s.logger.Debug("ignoring non-binding request", zap.String("type", msg.Type.String()))
		return
	}

	udpAddr, ok := remoteAddr.(*net.UDPAddr)
	if !ok {
		return
	}

	// Build STUN Binding Response with XOR-MAPPED-ADDRESS
	resp, err := stun.Build(
		stun.TransactionID,
		stun.BindingSuccess,
		&stun.XORMappedAddress{
			IP:   udpAddr.IP,
			Port: udpAddr.Port,
		},
		&stun.MappedAddress{
			IP:   udpAddr.IP,
			Port: udpAddr.Port,
		},
		stun.Fingerprint,
	)
	if err != nil {
		s.logger.Error("build STUN response failed", zap.Error(err))
		return
	}

	// Copy transaction ID from request
	copy(resp.Raw[4:20], msg.Raw[4:20])

	if _, err := conn.WriteTo(resp.Raw, remoteAddr); err != nil {
		s.logger.Error("write STUN response failed", zap.Error(err))
		return
	}

	s.logger.Debug("STUN binding response sent",
		zap.String("client", remoteAddr.String()),
		zap.String("mapped", udpAddr.String()),
	)
}
