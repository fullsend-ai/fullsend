package sandbox

import (
	"context"
	"net"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	gatewayInfoTimeout  = 5 * time.Second
	gatewayProbeTimeout = 500 * time.Millisecond
)

// containerGatewayHosts are the host-gateway DNS names tried, in order, as a
// replacement for a loopback endpoint that isn't reachable as configured.
// host.containers.internal is Podman/gvproxy's name; host.docker.internal is
// Docker Desktop's equivalent. Each candidate is dial-probed before use (see
// resolveOverride) rather than assumed, since only one of the two — or
// neither — resolves in a given container runtime.
var containerGatewayHosts = []string{"host.containers.internal", "host.docker.internal"}

// Seams for the gateway-location checks; tests replace them.
var (
	// gatewayInfoFn returns `openshell gateway info` output, which names the
	// endpoint of the active (registered) gateway.
	gatewayInfoFn = fetchGatewayInfo
	// localIPsFn returns the addresses assigned to this host's interfaces.
	localIPsFn = localInterfaceIPs
	// lookupIPFn resolves a hostname.
	lookupIPFn = func(ctx context.Context, host string) ([]net.IP, error) {
		return net.DefaultResolver.LookupIP(ctx, "ip", host)
	}
	// hostnameFn returns this host's name.
	hostnameFn = os.Hostname
)

// gatewayLookupTimeout bounds resolving a gateway hostname.
const gatewayLookupTimeout = 2 * time.Second

// GatewayEndpoint returns the endpoint of the gateway openshell uses:
// OPENSHELL_GATEWAY_ENDPOINT when set, otherwise the endpoint of the
// registered (active) gateway as reported by `openshell gateway info`. It
// returns "" when neither names one, including when openshell cannot be run.
func GatewayEndpoint(ctx context.Context) string {
	if endpoint := strings.TrimSpace(os.Getenv("OPENSHELL_GATEWAY_ENDPOINT")); endpoint != "" {
		return endpoint
	}
	out, err := gatewayInfoFn(ctx)
	if err != nil {
		return ""
	}
	return parseGatewayEndpointInfo(out)
}

// GatewayIsRemote reports whether the gateway openshell uses runs on
// another host, whose image store is not this host's. See
// GatewayEndpointIsRemote for how an endpoint is classified.
func GatewayIsRemote(ctx context.Context) bool {
	return GatewayEndpointIsRemote(ctx, GatewayEndpoint(ctx))
}

// GatewayEndpointIsRemote reports whether endpoint names a gateway on
// another host. An empty endpoint (nothing to tell), a unix socket, a
// loopback address or "localhost", a host-gateway name such as
// host.containers.internal (how a containerized CLI reaches the gateway on
// its own host), an address assigned to one of this host's interfaces, and
// this host's own name or a name that resolves only to this host's
// addresses all name a gateway on this host. Any other host, including a
// name that does not resolve, is remote.
func GatewayEndpointIsRemote(ctx context.Context, endpoint string) bool {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return false
	}
	if !strings.Contains(endpoint, "://") {
		endpoint = "//" + endpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return true
	}
	if u.Scheme == "unix" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return true
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return false
	}
	for _, h := range containerGatewayHosts {
		if host == h {
			return false
		}
	}
	if ip := parseHostIP(host); ip != nil {
		return !isLocalIP(ip)
	}
	if name, err := hostnameFn(); err == nil && name != "" {
		name = strings.ToLower(name)
		if host == name || host == strings.SplitN(name, ".", 2)[0] {
			return false
		}
	}
	lookupCtx, cancel := context.WithTimeout(ctx, gatewayLookupTimeout)
	defer cancel()
	ips, err := lookupIPFn(lookupCtx, host)
	if err != nil || len(ips) == 0 {
		return true
	}
	for _, ip := range ips {
		if !isLocalIP(ip) {
			return true
		}
	}
	return false
}

// parseHostIP parses a literal IP host, ignoring an IPv6 zone.
func parseHostIP(host string) net.IP {
	if i := strings.IndexByte(host, '%'); i >= 0 {
		host = host[:i]
	}
	return net.ParseIP(host)
}

// isLocalIP reports whether ip is a loopback address or assigned to one of
// this host's interfaces.
func isLocalIP(ip net.IP) bool {
	if ip.IsLoopback() {
		return true
	}
	ips, err := localIPsFn()
	if err != nil {
		return false
	}
	for _, local := range ips {
		if local.Equal(ip) {
			return true
		}
	}
	return false
}

// localInterfaceIPs returns the addresses assigned to this host's interfaces.
func localInterfaceIPs() ([]net.IP, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	var ips []net.IP
	for _, a := range addrs {
		switch v := a.(type) {
		case *net.IPNet:
			ips = append(ips, v.IP)
		case *net.IPAddr:
			ips = append(ips, v.IP)
		}
	}
	return ips, nil
}

var ansiEscapeRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

var gatewayOverrideOnce = &sync.Once{}

// resolveOverrideFn is resolveContainerGatewayOverride by default; tests
// swap it to exercise ensureContainerGatewayOverride's os.Setenv path
// without a real container.
var resolveOverrideFn = resolveContainerGatewayOverride

// ensureContainerGatewayOverride detects whether fullsend itself is running
// inside a container whose loopback does not reach the openshell gateway's
// configured endpoint (typically 127.0.0.1) — the situation when the
// fullsend-runner container image runs with --network=host on a macOS
// Podman machine, where the container's 127.0.0.1 is the Podman VM's own
// loopback, not the Mac's (fullsend-ai/fullsend#5261). When that's the
// case, it sets OPENSHELL_GATEWAY_ENDPOINT to whichever host-gateway DNS
// name (host.containers.internal for Podman, host.docker.internal for
// Docker) actually reaches the gateway, so every openshell subprocess
// invocation for the rest of this process picks it up via inherited
// environment — this is OpenShell's own documented override
// (--gateway-endpoint / OPENSHELL_GATEWAY_ENDPOINT), which connects directly
// rather than looking up stored gateway metadata.
//
// On Linux, --network=host gives the container the real host network
// namespace, so the configured endpoint already works and this is a no-op.
// It's also a no-op when not running in a container at all (the common
// case: native fullsend on the host).
//
// Call this before any operation that talks to the gateway. It runs at
// most once per process; later calls are free.
func ensureContainerGatewayOverride(ctx context.Context) {
	gatewayOverrideOnce.Do(func() {
		if _, explicit := os.LookupEnv("OPENSHELL_GATEWAY_ENDPOINT"); explicit {
			return // respect a user-supplied override; don't second-guess it
		}
		if override := resolveOverrideFn(ctx); override != "" {
			os.Setenv("OPENSHELL_GATEWAY_ENDPOINT", override) //nolint:errcheck // best-effort override; os.Setenv only errors on a NUL byte, which resolveOverrideFn's URL-derived values never contain
		}
	})
}

// resolveContainerGatewayOverride returns the OPENSHELL_GATEWAY_ENDPOINT
// value to use, or "" if the configured endpoint should be left alone.
func resolveContainerGatewayOverride(ctx context.Context) string {
	return resolveOverride(ctx, runningInContainer, fetchGatewayInfo, dialReachable)
}

// fetchGatewayInfo runs `openshell gateway info` and returns its raw output.
func fetchGatewayInfo(ctx context.Context) (string, error) {
	infoCtx, cancel := context.WithTimeout(ctx, gatewayInfoTimeout)
	defer cancel()
	out, err := exec.CommandContext(infoCtx, "openshell", "gateway", "info").Output()
	return string(out), err
}

// resolveOverride holds the decision logic apart from the real
// container-detection, subprocess, and dialing calls, so it can be
// exercised with fakes for each branch: not in a container, the `openshell
// gateway info` call failing, an unparseable or non-loopback endpoint, an
// already-reachable loopback endpoint, and each host-gateway candidate being
// reachable or not.
//
// The reachability probe below is a bare TCP connect, not a TLS handshake —
// it doesn't authenticate whatever answers. A hijacked DNS entry or a
// same-host-gateway sibling container could win the probe and get adopted
// as OPENSHELL_GATEWAY_ENDPOINT. This is intentionally low-risk: OpenShell's
// real gateway call still does its own mTLS handshake (client + server
// certificates, hostname verification) against that endpoint, so an
// attacker without the pinned CA's key material can't complete it as the
// gateway — the residual exposure is presenting the client certificate to
// the wrong peer during a handshake that then fails.
func resolveOverride(
	ctx context.Context,
	inContainer func() bool,
	fetchInfo func(context.Context) (string, error),
	dial func(context.Context, string, time.Duration) bool,
) string {
	if !inContainer() {
		return ""
	}

	out, err := fetchInfo(ctx)
	if err != nil {
		return "" // best-effort; let the normal call path surface the real error
	}

	endpoint := parseGatewayEndpointInfo(out)
	u, ok := parseLoopbackEndpoint(endpoint)
	if !ok {
		return ""
	}

	if dial(ctx, u.Host, gatewayProbeTimeout) {
		return "" // already reachable as configured (e.g. Linux --network=host)
	}

	for _, host := range containerGatewayHosts {
		candidateHostPort := host + ":" + u.Port()
		if dial(ctx, candidateHostPort, gatewayProbeTimeout) {
			return rewriteHost(u, host)
		}
	}
	// Neither host-gateway name is reachable either — leave the configured
	// endpoint alone so the real openshell call fails with its own native
	// connection error, rather than swapping in an unresolvable hostname.
	return ""
}

// parseLoopbackEndpoint parses endpoint and reports ok=true only if it points
// at a loopback address (127.0.0.1, ::1, or localhost) with an explicit
// port — anything else (unparseable, missing a port, or already a real
// hostname) means "nothing to rewrite here."
func parseLoopbackEndpoint(endpoint string) (u *url.URL, ok bool) {
	if endpoint == "" {
		return nil, false
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Port() == "" {
		return nil, false
	}
	switch parsed.Hostname() {
	case "127.0.0.1", "::1", "localhost":
		return parsed, true
	default:
		return nil, false
	}
}

// rewriteHost returns u with its host swapped for host, keeping u's original
// port, scheme, and path.
func rewriteHost(u *url.URL, host string) string {
	rewritten := *u
	rewritten.Host = host + ":" + u.Port()
	return rewritten.String()
}

// runningInContainer reports whether the current process is running inside
// a container. Both markers are checked since Docker and Podman use
// different conventions.
func runningInContainer() bool {
	return containerMarkersExist([]string{"/.dockerenv", "/run/.containerenv"})
}

func containerMarkersExist(markers []string) bool {
	for _, m := range markers {
		if _, err := os.Stat(m); err == nil {
			return true
		}
	}
	return false
}

// parseGatewayEndpointInfo extracts the endpoint URL from `openshell gateway
// info` output, e.g. "  Gateway endpoint: https://127.0.0.1:17670". The CLI
// emits ANSI color codes unconditionally, even when stdout isn't a
// terminal, so those are stripped before matching.
func parseGatewayEndpointInfo(output string) string {
	clean := stripANSI(output)
	for _, line := range strings.Split(clean, "\n") {
		if _, value, found := strings.Cut(line, "Gateway endpoint:"); found {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func stripANSI(s string) string {
	return ansiEscapeRe.ReplaceAllString(s, "")
}

func dialReachable(ctx context.Context, hostport string, timeout time.Duration) bool {
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", hostport)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
