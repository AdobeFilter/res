package stun

import (
	"encoding/binary"
	"net"

	"go.uber.org/zap"
)

// STUN message types
const (
	bindingRequest  = 0x0001
	bindingResponse = 0x0101
)

// STUN attribute types
const (
	attrMappedAddress    = 0x0001
	attrXORMappedAddress = 0x0020
)

const magicCookie = 0x2112A442

// Server is a minimal embedded STUN server.
// It handles Binding Requests and returns the client's reflexive address.
type Server struct {
	logger *zap.Logger
}

func NewServer(logger *zap.Logger) *Server {
	return &Server{logger: logger}
}

// ListenAndServe starts the STUN UDP listener. Blocks until conn is closed.
func (s *Server) ListenAndServe(addr string) error {
	conn, err := net.ListenPacket("udp4", addr)
	if err != nil {
		return err
	}
	defer conn.Close()

	s.logger.Info("embedded STUN server listening", zap.String("addr", addr))

	buf := make([]byte, 1500)
	for {
		n, remoteAddr, err := conn.ReadFrom(buf)
		if err != nil {
			return err
		}
		if n < 20 {
			continue
		}
		go s.handle(conn, remoteAddr, buf[:n])
	}
}

func (s *Server) handle(conn net.PacketConn, addr net.Addr, data []byte) {
	// Parse STUN header: type(2) + length(2) + magic(4) + txnID(12) = 20 bytes
	msgType := binary.BigEndian.Uint16(data[0:2])
	if msgType != bindingRequest {
		return
	}

	// Extract transaction ID (bytes 8-20)
	var txnID [12]byte
	copy(txnID[:], data[8:20])

	udpAddr, ok := addr.(*net.UDPAddr)
	if !ok {
		return
	}

	ip4 := udpAddr.IP.To4()
	if ip4 == nil {
		return
	}

	// Build XOR-MAPPED-ADDRESS attribute
	xorAddr := buildXORMappedAddress(ip4, udpAddr.Port, txnID)
	// Build MAPPED-ADDRESS attribute
	mappedAddr := buildMappedAddress(ip4, udpAddr.Port)

	attrLen := len(xorAddr) + len(mappedAddr)

	// Build response: header(20) + attributes
	resp := make([]byte, 20+attrLen)
	binary.BigEndian.PutUint16(resp[0:2], bindingResponse)
	binary.BigEndian.PutUint16(resp[2:4], uint16(attrLen))
	binary.BigEndian.PutUint32(resp[4:8], magicCookie)
	copy(resp[8:20], txnID[:])
	copy(resp[20:], xorAddr)
	copy(resp[20+len(xorAddr):], mappedAddr)

	conn.WriteTo(resp, addr)

	s.logger.Debug("STUN response sent", zap.String("client", addr.String()))
}

func buildXORMappedAddress(ip net.IP, port int, txnID [12]byte) []byte {
	// XOR port with magic cookie high 16 bits
	xPort := uint16(port) ^ uint16(magicCookie>>16)
	// XOR IP with magic cookie
	xIP := make([]byte, 4)
	mcBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(mcBytes, magicCookie)
	for i := 0; i < 4; i++ {
		xIP[i] = ip[i] ^ mcBytes[i]
	}

	attr := make([]byte, 4+8) // type(2) + length(2) + reserved(1) + family(1) + port(2) + ip(4)
	binary.BigEndian.PutUint16(attr[0:2], attrXORMappedAddress)
	binary.BigEndian.PutUint16(attr[2:4], 8) // value length
	attr[4] = 0                              // reserved
	attr[5] = 0x01                           // IPv4
	binary.BigEndian.PutUint16(attr[6:8], xPort)
	copy(attr[8:12], xIP)
	return attr
}

func buildMappedAddress(ip net.IP, port int) []byte {
	attr := make([]byte, 4+8)
	binary.BigEndian.PutUint16(attr[0:2], attrMappedAddress)
	binary.BigEndian.PutUint16(attr[2:4], 8)
	attr[4] = 0
	attr[5] = 0x01
	binary.BigEndian.PutUint16(attr[6:8], uint16(port))
	copy(attr[8:12], ip)
	return attr
}
