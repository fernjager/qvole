package app

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"
)

// resolveTimeout bounds hostname resolution when resolving a tunnel target or
// an -aF allowlist host. Keeps a stalled/hostile DNS from blocking a tunnel.
const resolveTimeout = 3 * time.Second

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

	// forwardRules is the address-aware form of Forward, built once by
	// Resolve() so the dial path can pin to allowed IPs without re-resolving
	// the allowlist on every connection. A nil ip means the ':port' wildcard.
	forwardRules []ipRule
	// resolved records that Resolve ran. It keeps matchForwardIPs from
	// re-resolving an entry that failed at startup (which would violate the
	// documented fail-closed behavior).
	resolved bool
}

// ipRule is one resolution-aware -aF entry. Nil ip (with a non-empty port)
// matches any host on that port.
type ipRule struct {
	port string
	ip   net.IP
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

// Resolve builds the address-aware forward rules by resolving each -aF hostname
// once, at startup. Hostnames that fail to resolve are dropped (they simply will
// not match, so the dial path fails closed rather than over-permitting). IP
// literals and ':port' wildcards are kept as-is. Call after Validate.
func (a *TunnelAllow) Resolve() {
	a.forwardRules = buildForwardRules(a.Forward)
	a.resolved = true
}

// AllowsListen reports whether the peer may ask this side to listen on addr.
func (a TunnelAllow) AllowsListen(addr string) bool {
	return a.All || allowMatch(a.Listen, addr)
}

// AllowsForward reports whether the peer may reach addr through this side.
//
// This is a string/address pre-filter on the exact host the peer sent; it does
// NOT resolve the peer's address. The authoritative, address-aware check used
// for actually dialing a peer-initiated forward target is ForwardIPs, which
// resolves both sides and pins to an allowed IP literal.
func (a TunnelAllow) AllowsForward(addr string) bool {
	return a.All || allowMatch(a.Forward, addr)
}

// ForwardIPs resolves the peer-supplied forward target to its addresses and
// returns the subset allowed by the -aF allowlist (plus the port to dial).
//
// Dialing one of these IP literals keeps the allowlist check and the connect on
// the same address, closing the DNS-rebinding / host-alias window and treating
// aliases such as localhost vs 127.0.0.1 consistently. ok is false when the
// target resolves to no allowed address, in which case the caller must reject.
func (a TunnelAllow) ForwardIPs(ctx context.Context, addr string) (ips []net.IP, port string, ok bool) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, "", false
	}
	all := resolveAllIPs(ctx, host)
	if len(all) == 0 {
		return nil, "", false
	}
	if a.All {
		return all, port, true
	}
	allowed := make([]net.IP, 0, len(all))
	for _, ip := range all {
		if a.matchForwardIPs(ip, port) {
			allowed = append(allowed, ip)
		}
	}
	if len(allowed) == 0 {
		return nil, "", false
	}
	return allowed, port, true
}

// matchForwardIPs reports whether ip:port is permitted by the -aF allowlist.
func (a TunnelAllow) matchForwardIPs(ip net.IP, port string) bool {
	rules := a.forwardRules
	if len(rules) == 0 && len(a.Forward) > 0 {
		// Fallback for callers that construct a TunnelAllow without Resolve()
		// (e.g. tests): build the rules lazily.
		rules = buildForwardRules(a.Forward)
	}
	for _, r := range rules {
		if r.port != port {
			continue
		}
		if r.ip == nil || r.ip.Equal(ip) {
			return true
		}
	}
	return false
}

// buildForwardRules converts -aF entries into address-aware rules. Empty-host
// entries (":port") become wildcards (nil IP). Hostnames are resolved to all
// their addresses; entries that fail to resolve are dropped.
func buildForwardRules(entries []string) []ipRule {
	var out []ipRule
	for _, e := range entries {
		host, port, err := net.SplitHostPort(e)
		if err != nil {
			continue // malformed entries rejected by Validate
		}
		if host == "" {
			out = append(out, ipRule{port: port})
			continue
		}
		for _, ip := range resolveAllIPs(context.Background(), host) {
			out = append(out, ipRule{port: port, ip: ip})
		}
	}
	return out
}

// resolveAllIPs returns the addresses for host: an IP literal resolves to
// itself, a hostname is resolved (bounded by resolveTimeout). Returns nil on
// failure or for an empty host.
func resolveAllIPs(ctx context.Context, host string) []net.IP {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}
	}
	if host == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		ips = append(ips, a.IP)
	}
	return ips
}

// allowMatch reports whether addr matches any allowlist entry. Ports must
// match exactly; an entry host of "" (e.g. ":8081") matches any host. Hosts
// are compared case-insensitively. This remains a textual comparison: it is a
// pre-filter, not a resolution-aware authority (see ForwardIPs).
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
