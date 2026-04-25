// Package mesh implements the L3-mesh dispatcher. Clients reach it via the
// relay's VLESS+Reality inbound: each VLESS-tunneled TCP stream is a single
// logical session — one client — and carries length-prefixed frames the
// dispatcher uses to route WG ciphertext between peers.
//
// Wire format (big-endian, per frame):
//
//	[2B length][1B type][payload...]
//
// length = len(type) + len(payload). Max 65535, which dwarfs any WG packet
// (<1500B with IPv4 MTU). The dispatcher never decrypts WG payloads — it
// only reads the destination pubkey prefix on DATAGRAM frames to look up
// the matching peer's TCP session and forwards the frame verbatim.
package mesh

const (
	// FrameHello — first frame on every new session. Payload = 32B WG pubkey
	// that identifies the connecting client. Replaces any existing session
	// registered under that pubkey (keeps behavior sane when a client
	// reconnects after a network blip).
	FrameHello byte = 0x01

	// FrameDatagram — a WG packet for another peer. Payload = 32B dst_pubkey
	// || wg_ciphertext. On ingress the dispatcher looks up dst_pubkey's
	// session; on egress it rewrites the pubkey prefix to the sender's so
	// the peer receives [src_pubkey || wg_ciphertext].
	FrameDatagram byte = 0x02

	// PubkeyLen is the fixed size of a WG x25519 public key.
	PubkeyLen = 32

	// MaxFrame bounds frame payload size. Any larger frame is treated as a
	// protocol violation and the session is dropped.
	MaxFrame = 65535
)
