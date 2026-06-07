package app

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"time"
)

const (
	dialTimeout         = 10 * time.Second
	scannerMaxTokenSize = 4096
	maxTunnelRequests   = 100
	streamHeaderTimeout = 15 * time.Second
	streamConfigTimeout = 30 * time.Second
)

// TunnelRequest represents a single port-forwarding tunnel specification.
type TunnelRequest struct {
	Type       string // "L" or "R"
	ListenAddr string
	TargetAddr string
}

func readRequestsFromStream(scanner *bufio.Scanner) ([]TunnelRequest, error) {
	var reqs []TunnelRequest
	for scanner.Scan() {
		line := scanner.Text()
		if line == "END" {
			break
		}
		if len(reqs) >= maxTunnelRequests*2 {
			return nil, fmt.Errorf("too many tunnel requests from peer")
		}
		parts := strings.SplitN(line, " ", 3)
		if len(parts) != 3 {
			return nil, fmt.Errorf("invalid request line: %q", line)
		}
		if parts[0] != "L" && parts[0] != "R" {
			return nil, fmt.Errorf("invalid tunnel type %q in line: %q", parts[0], line)
		}
		reqs = append(reqs, TunnelRequest{
			Type:       parts[0],
			ListenAddr: parts[1],
			TargetAddr: parts[2],
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read requests: %w", err)
	}
	return reqs, nil
}

// SplitTunnelRequest splits a tunnel spec string by colons, respecting bracketed
// IPv6 addresses. Returns nil for malformed input.
func SplitTunnelRequest(spec string) []string {
	var parts []string
	depth := 0
	start := 0
	for i, c := range spec {
		switch c {
		case '[':
			depth++
		case ']':
			depth--
			if depth < 0 {
				return nil
			}
		case ':':
			if depth == 0 {
				parts = append(parts, spec[start:i])
				start = i + 1
			}
		}
	}
	if depth != 0 {
		return nil
	}
	parts = append(parts, spec[start:])
	return parts
}

func stripBrackets(s string) string {
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		return s[1 : len(s)-1]
	}
	return s
}

// ParseTunnelRequest parses a tunnel spec string into a TunnelRequest.
// Accepts the forms [addr:]port:host:port.
func ParseTunnelRequest(spec, typ string) (*TunnelRequest, error) {
	parts := SplitTunnelRequest(spec)

	var listenAddr, targetAddr string

	switch len(parts) {
	case 3:
		listenAddr = net.JoinHostPort("127.0.0.1", parts[0])
		targetAddr = net.JoinHostPort(stripBrackets(parts[1]), parts[2])
	case 4:
		listenAddr = net.JoinHostPort(stripBrackets(parts[0]), parts[1])
		targetAddr = net.JoinHostPort(stripBrackets(parts[2]), parts[3])
	default:
		return nil, fmt.Errorf("expected [addr:]port:host:port format")
	}

	return &TunnelRequest{Type: typ, ListenAddr: listenAddr, TargetAddr: targetAddr}, nil
}
