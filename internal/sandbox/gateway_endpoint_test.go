package sandbox

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestContainerMarkersExist_NoneExist(t *testing.T) {
	dir := t.TempDir()
	if containerMarkersExist([]string{filepath.Join(dir, "a"), filepath.Join(dir, "b")}) {
		t.Fatal("expected false when no marker files exist")
	}
}

func TestContainerMarkersExist_FirstExists(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "dockerenv")
	if err := writeEmptyFile(marker); err != nil {
		t.Fatal(err)
	}
	if !containerMarkersExist([]string{marker, filepath.Join(dir, "missing")}) {
		t.Fatal("expected true when the first marker exists")
	}
}

func TestContainerMarkersExist_SecondExists(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "containerenv")
	if err := writeEmptyFile(marker); err != nil {
		t.Fatal(err)
	}
	if !containerMarkersExist([]string{filepath.Join(dir, "missing"), marker}) {
		t.Fatal("expected true when the second marker exists")
	}
}

func writeEmptyFile(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	return f.Close()
}

func TestParseGatewayEndpointInfo_StripsANSIAndExtracts(t *testing.T) {
	// Captured verbatim from a real `openshell gateway info` run — this CLI
	// emits ANSI color codes unconditionally, even when stdout isn't a TTY.
	const output = "\x1b[1m\x1b[36mGateway Info\x1b[39m\x1b[0m\n\n  \x1b[2mGateway:\x1b[0m openshell\n  \x1b[2mGateway endpoint:\x1b[0m https://127.0.0.1:17670\n"

	got := parseGatewayEndpointInfo(output)
	want := "https://127.0.0.1:17670"
	if got != want {
		t.Fatalf("parseGatewayEndpointInfo() = %q, want %q", got, want)
	}
}

func TestParseGatewayEndpointInfo_NoMatch(t *testing.T) {
	if got := parseGatewayEndpointInfo("Gateway Info\n\n  Gateway: openshell\n"); got != "" {
		t.Fatalf("expected empty string, got %q", got)
	}
}

func TestParseGatewayEndpointInfo_Empty(t *testing.T) {
	if got := parseGatewayEndpointInfo(""); got != "" {
		t.Fatalf("expected empty string, got %q", got)
	}
}

func TestStripANSI(t *testing.T) {
	got := stripANSI("\x1b[2mGateway endpoint:\x1b[0m https://127.0.0.1:17670")
	want := "Gateway endpoint: https://127.0.0.1:17670"
	if got != want {
		t.Fatalf("stripANSI() = %q, want %q", got, want)
	}
}

func TestStripANSI_NoEscapeCodes(t *testing.T) {
	const plain = "Gateway endpoint: https://127.0.0.1:17670"
	if got := stripANSI(plain); got != plain {
		t.Fatalf("stripANSI() = %q, want %q (unchanged)", got, plain)
	}
}

func TestResolveContainerGatewayOverride_NotInContainer(t *testing.T) {
	// runningInContainer() checks the real /.dockerenv and /run/.containerenv
	// paths, which are absent in this test environment (a Go test binary),
	// so this exercises the early-return path with no subprocess or network
	// calls — the same guarantee that protects native (non-containerized)
	// fullsend runs from ever invoking the gateway-info/dial logic at all.
	if got := resolveContainerGatewayOverride(context.Background()); got != "" {
		t.Fatalf("expected no override outside a container, got %q", got)
	}
}

// resetGatewayOverrideOnce isolates gatewayOverrideOnce (a package-level
// sync.Once) to the calling test, since ensureContainerGatewayOverride only
// runs its body the first time any test calls it per process — without this,
// one test's call would silently no-op the override logic for every test
// that runs after it.
func resetGatewayOverrideOnce(t *testing.T) {
	t.Helper()
	original := gatewayOverrideOnce
	t.Cleanup(func() { gatewayOverrideOnce = original })
	gatewayOverrideOnce = &sync.Once{}
}

func TestEnsureContainerGatewayOverride_NotInContainer_NoPanic(t *testing.T) {
	resetGatewayOverrideOnce(t)
	// Guards against a regression where this panics or blocks when not
	// running in a container — the common case for every `go test` run.
	ensureContainerGatewayOverride(context.Background())
}

func TestEnsureContainerGatewayOverride_RespectsExplicitEnv(t *testing.T) {
	resetGatewayOverrideOnce(t)
	t.Setenv("OPENSHELL_GATEWAY_ENDPOINT", "https://user-chosen.example.com:9999")

	ensureContainerGatewayOverride(context.Background())

	if got := os.Getenv("OPENSHELL_GATEWAY_ENDPOINT"); got != "https://user-chosen.example.com:9999" {
		t.Fatalf("OPENSHELL_GATEWAY_ENDPOINT = %q, want the user-supplied value left untouched", got)
	}
}

func TestEnsureContainerGatewayOverride_SetsEnvWhenOverrideFound(t *testing.T) {
	resetGatewayOverrideOnce(t)
	clearEnv(t, "OPENSHELL_GATEWAY_ENDPOINT")

	originalFn := resolveOverrideFn
	t.Cleanup(func() { resolveOverrideFn = originalFn })
	resolveOverrideFn = func(context.Context) string { return "https://host.containers.internal:17670" }

	ensureContainerGatewayOverride(context.Background())

	if got := os.Getenv("OPENSHELL_GATEWAY_ENDPOINT"); got != "https://host.containers.internal:17670" {
		t.Fatalf("OPENSHELL_GATEWAY_ENDPOINT = %q, want the resolved override to have been set", got)
	}
}

// clearEnv unsets an env var for the test and restores whatever value (or
// absence) it had beforehand, regardless of what the test does to it.
func clearEnv(t *testing.T, key string) {
	t.Helper()
	original, wasSet := os.LookupEnv(key)
	t.Cleanup(func() {
		if wasSet {
			os.Setenv(key, original) //nolint:errcheck
		} else {
			os.Unsetenv(key) //nolint:errcheck
		}
	})
	os.Unsetenv(key) //nolint:errcheck
}

func alwaysInContainer() bool { return true }

func fakeInfo(output string, err error) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return output, err }
}

// fakeDial returns a dial func reachable only for the given set of hostports
// — deterministic stand-in for real TCP/DNS so resolveOverride's branching
// can be tested without depending on what actually resolves or listens on
// the test machine.
func fakeDial(reachable ...string) func(context.Context, string, time.Duration) bool {
	set := make(map[string]bool, len(reachable))
	for _, hp := range reachable {
		set[hp] = true
	}
	return func(_ context.Context, hostport string, _ time.Duration) bool { return set[hostport] }
}

func TestResolveOverride_NotInContainer(t *testing.T) {
	called := false
	fetchInfo := func(context.Context) (string, error) {
		called = true
		return "", nil
	}
	got := resolveOverride(context.Background(), func() bool { return false }, fetchInfo, fakeDial())
	if got != "" {
		t.Fatalf("resolveOverride() = %q, want empty", got)
	}
	if called {
		t.Fatal("fetchInfo should not be called when not in a container")
	}
}

func TestResolveOverride_FetchInfoError(t *testing.T) {
	got := resolveOverride(context.Background(), alwaysInContainer, fakeInfo("", errFake), fakeDial())
	if got != "" {
		t.Fatalf("resolveOverride() = %q, want empty on fetch error", got)
	}
}

func TestResolveOverride_NonLoopbackEndpoint(t *testing.T) {
	const output = "  Gateway endpoint: https://gateway.example.com:17670\n"
	got := resolveOverride(context.Background(), alwaysInContainer, fakeInfo(output, nil), fakeDial())
	if got != "" {
		t.Fatalf("resolveOverride() = %q, want empty for a real hostname", got)
	}
}

func TestResolveOverride_LoopbackAlreadyReachable(t *testing.T) {
	const output = "  Gateway endpoint: https://127.0.0.1:17670\n"
	got := resolveOverride(context.Background(), alwaysInContainer, fakeInfo(output, nil), fakeDial("127.0.0.1:17670"))
	if got != "" {
		t.Fatalf("resolveOverride() = %q, want empty when the configured endpoint is already reachable", got)
	}
}

func TestResolveOverride_RewritesToPodmanHost(t *testing.T) {
	const output = "  Gateway endpoint: https://127.0.0.1:17670\n"
	got := resolveOverride(context.Background(), alwaysInContainer, fakeInfo(output, nil),
		fakeDial("host.containers.internal:17670"))
	want := "https://host.containers.internal:17670"
	if got != want {
		t.Fatalf("resolveOverride() = %q, want %q", got, want)
	}
}

func TestResolveOverride_FallsBackToDockerHost(t *testing.T) {
	const output = "  Gateway endpoint: https://127.0.0.1:17670\n"
	// host.containers.internal (tried first) is deliberately left unreachable
	// so this exercises falling through to host.docker.internal.
	got := resolveOverride(context.Background(), alwaysInContainer, fakeInfo(output, nil),
		fakeDial("host.docker.internal:17670"))
	want := "https://host.docker.internal:17670"
	if got != want {
		t.Fatalf("resolveOverride() = %q, want %q", got, want)
	}
}

func TestResolveOverride_NoHostReachable(t *testing.T) {
	const output = "  Gateway endpoint: https://127.0.0.1:17670\n"
	got := resolveOverride(context.Background(), alwaysInContainer, fakeInfo(output, nil), fakeDial())
	if got != "" {
		t.Fatalf("resolveOverride() = %q, want empty when no host-gateway candidate is reachable", got)
	}
}

var errFake = errors.New("fake exec failure")

func TestParseLoopbackEndpoint_RecognizesLoopbackWithPort(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
		wantHost string
	}{
		{"ip loopback", "https://127.0.0.1:17670", "127.0.0.1:17670"},
		{"localhost", "http://localhost:8080", "localhost:8080"},
		{"ipv6 loopback", "https://[::1]:17670", "[::1]:17670"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, ok := parseLoopbackEndpoint(tc.endpoint)
			if !ok {
				t.Fatalf("parseLoopbackEndpoint(%q) ok = false, want true", tc.endpoint)
			}
			if u.Host != tc.wantHost {
				t.Errorf("Host = %q, want %q", u.Host, tc.wantHost)
			}
		})
	}
}

func TestParseLoopbackEndpoint_LeavesNonLoopbackAlone(t *testing.T) {
	cases := []string{
		"https://gateway.example.com:17670", // real hostname — already reachable, don't touch
		"https://192.168.1.5:17670",         // real LAN IP — not a loopback
		"https://127.0.0.1",                 // no port — can't safely rewrite
		"not a url",                         // unparseable
		"",                                  // empty
	}
	for _, endpoint := range cases {
		if _, ok := parseLoopbackEndpoint(endpoint); ok {
			t.Errorf("parseLoopbackEndpoint(%q) ok = true, want false", endpoint)
		}
	}
}

func TestRewriteHost(t *testing.T) {
	u, ok := parseLoopbackEndpoint("https://127.0.0.1:17670")
	if !ok {
		t.Fatal("parseLoopbackEndpoint failed unexpectedly")
	}
	cases := []struct {
		host string
		want string
	}{
		{"host.containers.internal", "https://host.containers.internal:17670"},
		{"host.docker.internal", "https://host.docker.internal:17670"},
	}
	for _, tc := range cases {
		if got := rewriteHost(u, tc.host); got != tc.want {
			t.Errorf("rewriteHost(_, %q) = %q, want %q", tc.host, got, tc.want)
		}
	}
}

func TestDialReachable_ListeningPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start listener: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	if !dialReachable(context.Background(), ln.Addr().String(), time.Second) {
		t.Fatalf("expected %s to be reachable", ln.Addr().String())
	}
}

func TestDialReachable_ClosedPort(t *testing.T) {
	// Bind, learn the address, then close immediately — nothing listens on
	// this port afterward, mirroring the connection-refused behavior of a
	// container's own loopback when the real gateway is on the host instead.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start listener: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()

	if dialReachable(context.Background(), addr, 200*time.Millisecond) {
		t.Fatalf("expected %s to be unreachable after closing the listener", addr)
	}
}
