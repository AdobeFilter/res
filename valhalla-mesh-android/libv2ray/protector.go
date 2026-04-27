package libv2ray

import (
	"syscall"

	internet "github.com/xtls/xray-core/transport/internet"
)

// VpnProtector lets the Android side mark every outbound socket xray-core
// opens so the kernel routes that socket OUTSIDE the VpnService TUN. Without
// this, an outbound to the user-exit IP would loop back through the TUN
// (when the app addRoute's 0.0.0.0/0), the splitter dispatches it to the
// non-mesh forwarder, which dials xray's exit-in SOCKS5, which opens another
// outbound, ad infinitum. VpnService.protect(fd) breaks the loop at the
// kernel routing layer (Android tags the socket with a special mark that
// the VPN intercept skips).
type VpnProtector interface {
	// Protect must call android.net.VpnService.protect(fd) and return true
	// on success. fd is a raw socket file descriptor as int.
	Protect(fd int) bool
}

// RegisterVpnProtector wires a Kotlin-supplied VpnService.protect bridge
// into xray-core's transport/internet dialer controller. Every socket
// xray-core creates as part of an outbound (VLESS+Reality, freedom, etc.)
// flows through here right before the connect syscall.
//
// Pass nil to clear (registered controllers persist for the process
// lifetime in xray-core, so unregistering isn't really supported — calling
// with a no-op Protect is the closest equivalent).
func RegisterVpnProtector(p VpnProtector) {
	if p == nil {
		return
	}
	_ = internet.RegisterDialerController(func(network, address string, c syscall.RawConn) error {
		return c.Control(func(fd uintptr) {
			p.Protect(int(fd))
		})
	})
}
