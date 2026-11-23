package sandbox

import "testing"

func TestGatewayEndpointIsRemote(t *testing.T) {
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
		{"https://gateway.example.com:17670", true},
		{"gateway.example.com:17670", true},
		{"http://10.1.2.3:17670", true},
		{"http://[2001:db8::1]:17670", true},
		{"http://localhost.example.com:17670", true},
		{"://bad", true},
	}
	for _, tc := range tests {
		t.Run(tc.endpoint, func(t *testing.T) {
			t.Setenv("OPENSHELL_GATEWAY_ENDPOINT", tc.endpoint)
			if got := GatewayEndpointIsRemote(); got != tc.want {
				t.Errorf("GatewayEndpointIsRemote(%q) = %v, want %v", tc.endpoint, got, tc.want)
			}
		})
	}
}
