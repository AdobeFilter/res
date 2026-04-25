package tunnel

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
)

// ExitNode is a parsed VLESS+Reality URL pointing at the user's own xray
// server. When set on the XrayClient, the relay outbound is chained through
// it via xray's proxySettings.tag, so the only thing DPI sees on the wire is
// TLS to the exit-node's SNI — the inner Reality handshake to the Valhalla
// relay rides inside that tunnel.
type ExitNode struct {
	Address     string
	Port        int
	UUID        string
	Flow        string
	Network     string
	Security    string
	PublicKey   string
	SNI         string
	ShortID     string
	Fingerprint string
	SpiderX     string
}

// ParseVLESSURL parses a standard vless:// share URL of the form:
//
//	vless://UUID@HOST:PORT?type=tcp&security=reality&pbk=...&sni=...&sid=...&fp=chrome&flow=xtls-rprx-vision&spx=%2F#name
//
// Defaults: type=tcp, fp=chrome. Other params are taken verbatim from the
// query — empty values pass through to xray (which treats them as defaults).
func ParseVLESSURL(s string) (*ExitNode, error) {
	u, err := url.Parse(s)
	if err != nil {
		return nil, fmt.Errorf("parse vless url: %w", err)
	}
	if u.Scheme != "vless" {
		return nil, fmt.Errorf("expected vless:// scheme, got %q", u.Scheme)
	}
	if u.User == nil || u.User.Username() == "" {
		return nil, fmt.Errorf("missing UUID before @ in vless url")
	}
	host, portStr, err := net.SplitHostPort(u.Host)
	if err != nil {
		return nil, fmt.Errorf("vless url host:port: %w", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("vless url port: %w", err)
	}
	q := u.Query()
	return &ExitNode{
		Address:     host,
		Port:        port,
		UUID:        u.User.Username(),
		Flow:        q.Get("flow"),
		Network:     defaultStr(q.Get("type"), "tcp"),
		Security:    defaultStr(q.Get("security"), "reality"),
		PublicKey:   q.Get("pbk"),
		SNI:         q.Get("sni"),
		ShortID:     q.Get("sid"),
		Fingerprint: defaultStr(q.Get("fp"), "chrome"),
		SpiderX:     q.Get("spx"),
	}, nil
}

func defaultStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
