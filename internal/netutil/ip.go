// Package netutil provides shared network utilities for IP classification.
package netutil

import (
	"fmt"
	"net"
)

type reservedNet struct {
	network *net.IPNet
	reason  string
}

var reservedNets []reservedNet

func init() {
	for _, entry := range []struct {
		cidr   string
		reason string
	}{
		{"0.0.0.0/8", "\"this\" network (RFC 1122)"},
		{"100.64.0.0/10", "CGNAT address (RFC 6598)"},
		{"192.0.2.0/24", "documentation address (TEST-NET-1, RFC 5737)"},
		{"198.18.0.0/15", "benchmark testing (RFC 2544)"},
		{"198.51.100.0/24", "documentation address (TEST-NET-2, RFC 5737)"},
		{"203.0.113.0/24", "documentation address (TEST-NET-3, RFC 5737)"},
	} {
		_, network, err := net.ParseCIDR(entry.cidr)
		if err != nil {
			panic(fmt.Sprintf("netutil: bad CIDR %q: %v", entry.cidr, err))
		}
		reservedNets = append(reservedNets, reservedNet{network: network, reason: entry.reason})
	}
}

// CheckIP reports whether ip is a reserved address that should not be
// contacted by an outbound HTTP client. Returns an empty string if
// the IP is safe, or a human-readable reason if it is blocked.
func CheckIP(ip net.IP) string {
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}

	if ip.IsLoopback() {
		return "loopback address"
	}
	if ip.IsPrivate() {
		return "private address (RFC 1918)"
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return "link-local address"
	}
	if ip.IsMulticast() {
		return "multicast address"
	}
	if ip.IsUnspecified() {
		return "unspecified address"
	}

	for _, r := range reservedNets {
		if r.network.Contains(ip) {
			return r.reason
		}
	}

	return ""
}

// IsInternal is a convenience wrapper that returns true if ip is reserved.
func IsInternal(ip net.IP) bool {
	return CheckIP(ip) != ""
}
