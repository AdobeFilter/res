package tunnel

import (
	"encoding/hex"
	"errors"
	"net/netip"
	"strings"

	"golang.zx2c4.com/wireguard/conn"
)

// meshEndpoint identifies a peer by its WG public key. wireguard-go treats
// Endpoint values as opaque tokens — it hands them to our Bind.Send and
// gets them back from Bind.Receive, but never interprets the underlying
// address itself. We piggy-back on that opacity to carry a pubkey instead
// of the usual ip:port tuple.
//
// Endpoint strings follow the form "vmesh:<64-hex-chars>" so a human
// inspecting a wg-quick style config or device dump can tell at a glance
// that this peer runs over the mesh relay, not bare UDP.
type meshEndpoint struct {
	pubkey [32]byte
}

const endpointPrefix = "vmesh:"

var _ conn.Endpoint = (*meshEndpoint)(nil)

func (m *meshEndpoint) ClearSrc()              {}
func (m *meshEndpoint) SrcToString() string    { return "" }
func (m *meshEndpoint) SrcIP() netip.Addr      { return netip.Addr{} }
func (m *meshEndpoint) DstIP() netip.Addr      { return netip.Addr{} }
func (m *meshEndpoint) DstToString() string    { return endpointPrefix + hex.EncodeToString(m.pubkey[:]) }
func (m *meshEndpoint) DstToBytes() []byte {
	out := make([]byte, 32)
	copy(out, m.pubkey[:])
	return out
}

// parseMeshEndpoint accepts either the "vmesh:HEX" string we generate
// ourselves or a bare 64-char hex pubkey (the wg-quick style config form
// some operators prefer). Anything else is rejected — we never want the
// device to open a real UDP socket to an ip:port that might leak traffic.
func parseMeshEndpoint(s string) (*meshEndpoint, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, endpointPrefix)
	if len(s) != 64 {
		return nil, errors.New("mesh endpoint must be a 32-byte hex pubkey")
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, err
	}
	var ep meshEndpoint
	copy(ep.pubkey[:], b)
	return &ep, nil
}
