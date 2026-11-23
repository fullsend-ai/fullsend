package sandbox

import (
	"context"
	"errors"
	"net"
	"testing"
)

// stubGatewayLocation replaces the host lookups the classification uses.
func stubGatewayLocation(t *testing.T, hostname string, localIPs []string, dns map[string][]string) {
	t.Helper()
	origHost, origLocal, origLookup := hostnameFn, localIPsFn, lookupIPFn
	t.Cleanup(func() { hostnameFn, localIPsFn, lookupIPFn = origHost, origLocal, origLookup })
	hostnameFn = func() (string, error) { return hostname, nil }
	localIPsFn = func() ([]net.IP, error) {
		var ips []net.IP
		for _, s := range localIPs {
			ips = append(ips, net.ParseIP(s))
		}
		return ips, nil
	}
	lookupIPFn = func(_ context.Context, host string) ([]net.IP, error) {
		addrs, ok := dns[host]
		if !ok {
			return nil, errors.New("no such host")
		}
		var ips []net.IP
		for _, s := range addrs {
			ips = append(ips, net.ParseIP(s))
		}
		return ips, nil
	}
}

func TestGatewayEndpointIsRemote(t *testing.T) {
	stubGatewayLocation(t, "runner-1.corp.example", []string{"192.168.1.20", "fe80::1"}, map[string][]string{
		"gateway.example.com":  {"203.0.113.9"},
		"alias.corp.example":   {"192.168.1.20"},
		"split.corp.example":   {"192.168.1.20", "203.0.113.9"},
		"gw-remote.corp.local": {"10.9.9.9"},
	})
	tests := []struct {
		endpoint string
		want     bool
	}{
		{"", false},
		{"   ", false},
		{"http://127.0.0.1:17670", false},
		{"127.0.0.1:17670", false},
		{"https://127.0.0.2:17670", false},
		{"http://localhost:17670", false},
		{"https://LOCALHOST:17670", false},
		{"http://[::1]:17670", false},
		{"http://host.containers.internal:17670", false},
		{"https://host.docker.internal:17670", false},
		{"unix:///run/openshell/gateway.sock", false},
		// Local non-loopback endpoints: an interface address of this host,
		// this host's own name, or a name that resolves only to this host.
		{"https://192.168.1.20:17670", false},
		{"http://[fe80::1%25eth0]:17670", false},
		{"https://runner-1.corp.example:17670", false},
		{"https://RUNNER-1:17670", false},
		{"https://alias.corp.example:17670", false},
		// Remote endpoints.
		{"https://gateway.example.com:17670", true},
		{"gateway.example.com:17670", true},
		{"https://gw-remote.corp.local:17670", true},
		{"https://split.corp.example:17670", true},
		{"https://unresolvable.example:17670", true},
		{"http://10.1.2.3:17670", true},
		{"http://192.168.1.21:17670", true},
		{"http://[2001:db8::1]:17670", true},
		{"http://localhost.example.com:17670", true},
		{"://bad", true},
	}
	for _, tc := range tests {
		t.Run(tc.endpoint, func(t *testing.T) {
			if got := GatewayEndpointIsRemote(context.Background(), tc.endpoint); got != tc.want {
				t.Errorf("GatewayEndpointIsRemote(%q) = %v, want %v", tc.endpoint, got, tc.want)
			}
		})
	}
}

func TestGatewayEndpoint(t *testing.T) {
	origInfo := gatewayInfoFn
	t.Cleanup(func() { gatewayInfoFn = origInfo })
	infoCalls := 0
	info := "\x1b[1mGateway Info\x1b[0m\n  Gateway: remote-gw\n  Gateway endpoint: https://gateway.example.com:17670\n"
	var infoErr error
	gatewayInfoFn = func(context.Context) (string, error) {
		infoCalls++
		return info, infoErr
	}

	t.Run("environment wins and openshell is not asked", func(t *testing.T) {
		t.Setenv("OPENSHELL_GATEWAY_ENDPOINT", " http://127.0.0.1:17670 ")
		infoCalls = 0
		if got := GatewayEndpoint(context.Background()); got != "http://127.0.0.1:17670" {
			t.Errorf("GatewayEndpoint = %q", got)
		}
		if infoCalls != 0 {
			t.Errorf("openshell gateway info ran %d times", infoCalls)
		}
	})
	t.Run("registered gateway", func(t *testing.T) {
		t.Setenv("OPENSHELL_GATEWAY_ENDPOINT", "")
		if got := GatewayEndpoint(context.Background()); got != "https://gateway.example.com:17670" {
			t.Errorf("GatewayEndpoint = %q", got)
		}
	})
	t.Run("info failure names no endpoint", func(t *testing.T) {
		t.Setenv("OPENSHELL_GATEWAY_ENDPOINT", "")
		infoErr = errors.New("openshell not found")
		t.Cleanup(func() { infoErr = nil })
		if got := GatewayEndpoint(context.Background()); got != "" {
			t.Errorf("GatewayEndpoint = %q, want empty", got)
		}
	})
}

// A registered remote gateway is remote although nothing is exported; one
// whose info cannot be read stays local, as before.
func TestGatewayIsRemote_RegisteredGateway(t *testing.T) {
	stubGatewayLocation(t, "runner", nil, nil)
	origInfo := gatewayInfoFn
	t.Cleanup(func() { gatewayInfoFn = origInfo })
	t.Setenv("OPENSHELL_GATEWAY_ENDPOINT", "")

	gatewayInfoFn = func(context.Context) (string, error) {
		return "  Gateway endpoint: https://gateway.example.com:17670\n", nil
	}
	if !GatewayIsRemote(context.Background()) {
		t.Error("a registered remote gateway must be remote")
	}
	gatewayInfoFn = func(context.Context) (string, error) {
		return "  Gateway endpoint: https://127.0.0.1:17670\n", nil
	}
	if GatewayIsRemote(context.Background()) {
		t.Error("a registered loopback gateway must be local")
	}
	gatewayInfoFn = func(context.Context) (string, error) { return "", errors.New("boom") }
	if GatewayIsRemote(context.Background()) {
		t.Error("an unknown gateway location must stay local, as before")
	}
}

func TestLocalInterfaceIPs(t *testing.T) {
	if _, err := localInterfaceIPs(); err != nil {
		t.Skipf("no interface addresses available: %v", err)
	}
}
