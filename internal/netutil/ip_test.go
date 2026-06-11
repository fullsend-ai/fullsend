package netutil

import (
	"net"
	"testing"
)

func TestCheckIP(t *testing.T) {
	tests := []struct {
		name     string
		ip       string
		internal bool
	}{
		// Loopback
		{"loopback_v4", "127.0.0.1", true},
		{"loopback_v6", "::1", true},

		// RFC 1918 private ranges
		{"rfc1918_10", "10.0.0.1", true},
		{"rfc1918_172", "172.16.0.1", true},
		{"rfc1918_192", "192.168.1.1", true},

		// Link-local
		{"link_local_v4", "169.254.1.1", true},
		{"link_local_v6", "fe80::1", true},

		// CGNAT (RFC 6598)
		{"cgnat", "100.64.0.1", true},
		{"cgnat_end", "100.127.255.254", true},

		// Benchmark (RFC 2544)
		{"benchmark", "198.18.0.1", true},
		{"benchmark_end", "198.19.255.254", true},

		// Unspecified
		{"unspecified_v4", "0.0.0.0", true},
		{"unspecified_v6", "::", true},

		// "This" network
		{"this_network", "0.1.2.3", true},

		// Documentation / TEST-NET (RFC 5737)
		{"doc_test_net_1", "192.0.2.1", true},
		{"doc_test_net_2", "198.51.100.1", true},
		{"doc_test_net_3", "203.0.113.1", true},

		// Multicast
		{"multicast_v4", "224.0.0.1", true},
		{"multicast_v6", "ff02::1", true},

		// IPv4-mapped IPv6
		{"mapped_loopback", "::ffff:127.0.0.1", true},
		{"mapped_private", "::ffff:10.0.0.1", true},
		{"mapped_public", "::ffff:8.8.8.8", false},

		// Public IPs (should NOT be internal)
		{"public_google_dns", "8.8.8.8", false},
		{"public_cloudflare", "1.1.1.1", false},
		{"public_v6", "2001:4860:4860::8888", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ip := net.ParseIP(tc.ip)
			if ip == nil {
				t.Fatalf("failed to parse IP: %s", tc.ip)
			}
			got := IsInternal(ip)
			if got != tc.internal {
				t.Errorf("IsInternal(%s) = %v, want %v", tc.ip, got, tc.internal)
			}
			reason := CheckIP(ip)
			if tc.internal && reason == "" {
				t.Errorf("CheckIP(%s) returned empty reason for internal IP", tc.ip)
			}
			if !tc.internal && reason != "" {
				t.Errorf("CheckIP(%s) returned reason %q for public IP", tc.ip, reason)
			}
		})
	}
}
