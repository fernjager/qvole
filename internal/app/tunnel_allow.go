package app

import (
	"fmt"
	"net"
	"strings"
)

// TunnelAllow controls which peer tunnel requests this side will honor.
// An empty allowlist accepts nothing: peer -L and -R requests are ignored
// unless they match an entry (or All is set).
type TunnelAllow struct {
	// All, when true, accepts every peer tunnel request without matching.
	// This is the "allow anything" mode (-a, not safe): a peer may open
	// arbitrary listening ports and reach arbitrary addresses through this
	// machine. Prefer explicit Listen/Forward entries.
	All bool
	// Listen holds -aL addr:port entries: the peer may ask this side to open
	// a listening port on a matching address.
	Listen []string
	// Forward holds -aF addr:port entries: the peer may reach a matching
	// addr:port through this side.
	Forward []string
}

// Accepts reports whether this side is willing to accept any peer tunnel
// requests.
func (a TunnelAllow) Accepts() bool {
	return a.All || len(a.Listen) > 0 || len(a.Forward) > 0
}

// Validate parses every allowlist entry as addr:port, returning an error that
// lists malformed entries so typos fail fast instead of silently changing what
// is allowed.
func (a TunnelAllow) Validate() error {
	var bad []string
	for _, e := range a.Listen {
		if _, port, err := net.SplitHostPort(e); err != nil || port == "" {
			bad = append(bad, fmt.Sprintf("-aL %q", e))
		}
	}
	for _, e := range a.Forward {
		if _, port, err := net.SplitHostPort(e); err != nil || port == "" {
			bad = append(bad, fmt.Sprintf("-aF %q", e))
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("invalid tunnel allowlist entries (expected addr:port): %s", strings.Join(bad, ", "))
	}
	return nil
}

// AllowsListen reports whether the peer may ask this side to listen on addr.
func (a TunnelAllow) AllowsListen(addr string) bool {
	return a.All || allowMatch(a.Listen, addr)
}

// AllowsForward reports whether the peer may reach addr through this side.
func (a TunnelAllow) AllowsForward(addr string) bool {
	return a.All || allowMatch(a.Forward, addr)
}

// allowMatch reports whether addr matches any allowlist entry. Ports must
// match exactly; an entry host of "" (e.g. ":8081") matches any host. Hosts
// are compared case-insensitively.
func allowMatch(entries []string, addr string) bool {
	addrHost, addrPort, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	addrHost = strings.ToLower(addrHost)
	for _, e := range entries {
		eHost, ePort, err := net.SplitHostPort(e)
		if err != nil {
			continue // malformed entries are rejected by Validate
		}
		if ePort != addrPort {
			continue
		}
		if eHost == "" || strings.ToLower(eHost) == addrHost {
			return true
		}
	}
	return false
}
