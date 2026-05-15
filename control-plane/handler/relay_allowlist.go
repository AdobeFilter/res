package handler

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strings"
)

// maxRelayCapacity caps the self-declared capacity a relay reports at
// registration. Without it, a relay claiming an enormous slot count would
// always win GetBestAvailable's (capacity - active_sessions) ranking.
const maxRelayCapacity = 5000

// loadRelayAllowlist reads the allowlist file. Each non-empty non-comment
// line is one IPv4 (or IPv6) that may self-register a relay. A missing file
// is treated as an empty allowlist (closed by default) — operators must
// explicitly add entries.
func loadRelayAllowlist(path string) (map[string]struct{}, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]struct{}{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	allow := map[string]struct{}{}
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		allow[line] = struct{}{}
	}
	return allow, s.Err()
}

// checkRelayAllowed enforces two-sided verification:
//  1. the address declared in the registration body must appear in the
//     allowlist file (operator-controlled trust list);
//  2. the TCP source IP must match that declared address — otherwise a
//     rogue host that learned an allowlisted IP could still register.
//
// Disabled (returns nil) when allowlistPath is empty, to keep dev/test
// environments unchanged.
func checkRelayAllowed(r *http.Request, declared, allowlistPath string) error {
	if allowlistPath == "" {
		return nil
	}
	allow, err := loadRelayAllowlist(allowlistPath)
	if err != nil {
		return fmt.Errorf("load allowlist: %w", err)
	}
	if _, ok := allow[declared]; !ok {
		return fmt.Errorf("address %q not in allowlist", declared)
	}
	host, _, splitErr := net.SplitHostPort(r.RemoteAddr)
	if splitErr != nil {
		host = r.RemoteAddr
	}
	if host != declared {
		return fmt.Errorf("source ip %q does not match declared address %q", host, declared)
	}
	return nil
}
