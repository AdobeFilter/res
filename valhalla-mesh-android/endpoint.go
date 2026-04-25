package mesh

import (
	"encoding/hex"
	"fmt"
	"net/netip"
	"strings"

	"golang.zx2c4.com/wireguard/conn"
)

// meshEndpoint identifies a peer by its WG public key. wireguard-go's IpcSet
// accepts the literal string "vmesh:HEX_PUBKEY" as the endpoint, which our
// ParseEndpoint turns into one of these.
type meshEndpoint struct {
	pubkey [pubkeyLen]byte
}

var _ conn.Endpoint = (*meshEndpoint)(nil)

func (e *meshEndpoint) ClearSrc()             {}
func (e *meshEndpoint) SrcIP() netip.Addr     { return netip.Addr{} }
func (e *meshEndpoint) DstIP() netip.Addr     { return netip.Addr{} }
func (e *meshEndpoint) SrcToString() string   { return "" }
func (e *meshEndpoint) DstToString() string   { return "vmesh:" + hex.EncodeToString(e.pubkey[:]) }
func (e *meshEndpoint) DstToBytes() []byte    { return e.pubkey[:] }

func parseMeshEndpoint(s string) (*meshEndpoint, error) {
	const prefix = "vmesh:"
	if !strings.HasPrefix(s, prefix) {
		return nil, fmt.Errorf("mesh endpoint must start with %q, got %q", prefix, s)
	}
	hexStr := strings.TrimPrefix(s, prefix)
	raw, err := hex.DecodeString(hexStr)
	if err != nil || len(raw) != pubkeyLen {
		return nil, fmt.Errorf("bad mesh endpoint pubkey %q", hexStr)
	}
	var ep meshEndpoint
	copy(ep.pubkey[:], raw)
	return &ep, nil
}
