package steps

// vault.go holds the behaviour steps for Vault-backed runner secrets
// (ADR 0155). A Vault feature shares one OpenBao server
// (install.StartVaultServer) across its scenarios: the first scenario
// that needs it starts it, later scenarios of the same feature file reuse
// it, and StopVaultServers stops every server at job cleanup. Features
// that never use these steps never start a server. Each scenario writes
// synthetic secrets under its own random prefix and selects them through
// the leased repository's trusted base configuration (config.base.yaml).
//
// Secret values never reach the logs: a host-side pre-script prints a
// fingerprint of each value it received (the first 16 hex digits of
// sha256("fullsend-bt-vault:" + value)), and the steps compare those.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/cucumber/godog"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
	scmgh "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm/github"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

const (
	// vaultConnectionName is the connection alias the scenarios declare
	// in trusted base configuration for the feature's OpenBao server.
	vaultConnectionName = "bt-openbao"
	// vaultFingerprintPrefix salts the fingerprint the pre-script prints.
	vaultFingerprintPrefix = "fullsend-bt-vault:"
	// vaultBundleEntry is the runner secret name inside the synthetic
	// FULLSEND_GITHUB_RUNNER_SECRETS bundle a scenario stores in Vault.
	vaultBundleEntry = "BEHAVIOUR_VAULT_RUNNER_SECRET"
	// vaultRunnerSecretsName is the runner-secrets bundle secret.
	vaultRunnerSecretsName = forge.SecretGitHubRunnerSecrets
	// vaultTracesHeadersName is the trace-specific OTLP headers secret.
	vaultTracesHeadersName = "OTEL_EXPORTER_OTLP_TRACES_HEADERS"
)

// vaultTraceWait bounds how long the trace step waits for the run's
// trace file after the run completed: the exporter flushes at the end of
// the run, and the tunnel adds latency. Variables so unit tests can
// shrink them.
var (
	vaultTraceWait     = 3 * time.Minute
	vaultTracePollTick = 5 * time.Second
)

// vaultFingerprintScript is the host-side pre-script the Vault scenarios
// commit. It prints a fingerprint of each scenario secret it received
// through env.runner, never the value, and reports whether the
// runner-secrets bundle itself reached it (it must not: Fullsend parses
// the bundle and never places it in the environment).
const vaultFingerprintScript = `#!/usr/bin/env bash
# Behaviour test pre-script (ADR 0155 Vault scenarios). Prints a
# fingerprint of each Vault scenario secret this host-side script
# received, never the value.
set -euo pipefail
for name in JIRA_TOKEN OTEL_EXPORTER_OTLP_HEADERS OTEL_EXPORTER_OTLP_TRACES_HEADERS ` + vaultBundleEntry + `; do
  value="${!name:-}"
  if [ -n "$value" ]; then
    fp=$(printf '%s%s' '` + vaultFingerprintPrefix + `' "$value" | sha256sum | cut -c1-16)
    echo "vault-fingerprint ${name}=${fp}"
  else
    echo "vault-fingerprint ${name} unset"
  fi
done
if [ -n "${FULLSEND_GITHUB_RUNNER_SECRETS:-}" ]; then
  echo "vault-bundle-visible-to-script"
fi
`

// vaultFingerprint returns the fingerprint vaultFingerprintScript prints
// for value.
func vaultFingerprint(value string) string {
	sum := sha256.Sum256([]byte(vaultFingerprintPrefix + value))
	return hex.EncodeToString(sum[:])[:16]
}

// vaultFingerprintLine is the log line the pre-script prints for name
// holding value.
func vaultFingerprintLine(name, value string) string {
	return "vault-fingerprint " + name + "=" + vaultFingerprint(value)
}

// vaultServerStarter starts a feature's server; install.StartVaultServer
// in production.
type vaultServerStarter func(logf func(string, ...any)) (install.VaultServer, string, error)

// vaultServerRegistry holds one OpenBao server per feature file.
type vaultServerRegistry struct {
	start vaultServerStarter

	mu      sync.Mutex
	entries map[string]*vaultFeatureEntry
}

// vaultFeatureEntry is one feature's server. The first scenario of the
// feature starts it; a skip reason or start error is remembered, so the
// feature's other scenarios neither retry a slow failing start nor run
// without the server.
type vaultFeatureEntry struct {
	mu      sync.Mutex
	started bool
	server  install.VaultServer
	skip    string
	err     error
}

func newVaultServerRegistry(start vaultServerStarter) *vaultServerRegistry {
	return &vaultServerRegistry{start: start, entries: map[string]*vaultFeatureEntry{}}
}

// vaultServers is the process-wide registry the steps use.
var vaultServers = newVaultServerRegistry(install.StartVaultServer)

// forFeature returns the feature's server, starting it on first use. A
// nil server with a non-empty skip reason means the Vault scenarios are
// disabled in this environment.
func (r *vaultServerRegistry) forFeature(feature string, logf func(string, ...any)) (install.VaultServer, string, error) {
	r.mu.Lock()
	e := r.entries[feature]
	if e == nil {
		e = &vaultFeatureEntry{}
		r.entries[feature] = e
	}
	r.mu.Unlock()

	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.started {
		e.started = true
		if logf != nil {
			logf("[vault] starting the OpenBao server for feature %s", feature)
		}
		e.server, e.skip, e.err = r.start(logf)
		if e.err != nil && logf != nil {
			logf("[vault] OpenBao server for feature %s failed to start: %v", feature, e.err)
		}
	}
	if e.err != nil {
		return nil, "", fmt.Errorf("the feature's OpenBao server failed to start: %w", e.err)
	}
	return e.server, e.skip, nil
}

// stopAll stops every started server, including one whose failed start
// left containers behind. A server whose Stop failed stays registered so
// a later call retries it.
func (r *vaultServerRegistry) stopAll(ctx context.Context) error {
	r.mu.Lock()
	entries := make(map[string]*vaultFeatureEntry, len(r.entries))
	for k, v := range r.entries {
		entries[k] = v
	}
	r.mu.Unlock()
	var errs []error
	for feature, e := range entries {
		e.mu.Lock()
		if e.server != nil {
			if err := e.server.Stop(ctx); err != nil {
				errs = append(errs, fmt.Errorf("stopping the OpenBao server for feature %s: %w", feature, err))
			} else {
				e.server = nil
			}
		}
		e.mu.Unlock()
	}
	return errors.Join(errs...)
}

// StopVaultServers stops every feature's OpenBao server and saves its
// log and measurements. The suite runner calls it at job cleanup. Safe
// to call more than once.
func StopVaultServers(ctx context.Context) error {
	return vaultServers.stopAll(ctx)
}

func registerVaultSteps(sc *godog.ScenarioContext) {
	sc.Step(`^the Vault test server is running$`, func(ctx context.Context) (context.Context, error) {
		return ctx, givenVaultServer(world.FromContext(ctx))
	})
	sc.Step(`^the Vault test server allows the repository$`, func(ctx context.Context) (context.Context, error) {
		return ctx, givenVaultAllowsRepository(ctx, world.FromContext(ctx))
	})
	sc.Step(`^the Vault test server holds a synthetic "([A-Z][A-Z0-9_]*)"$`, func(ctx context.Context, name string) (context.Context, error) {
		return ctx, givenVaultSyntheticSecret(ctx, world.FromContext(ctx), name)
	})
	sc.Step(`^the Vault test server holds the trace collector headers as "OTEL_EXPORTER_OTLP_TRACES_HEADERS"$`, func(ctx context.Context) (context.Context, error) {
		return ctx, givenVaultTraceCollectorHeaders(ctx, world.FromContext(ctx))
	})
	sc.Step(`^the repository's native "OTEL_EXPORTER_OTLP_TRACES_HEADERS" secret holds a wrong token$`, func(ctx context.Context) (context.Context, error) {
		return ctx, givenNativeTracesHeadersWrong(world.FromContext(ctx))
	})
	sc.Step(`^the trusted base configuration selects the Vault test server for "([A-Z][A-Z0-9_]*)"$`, func(ctx context.Context, name string) (context.Context, error) {
		return ctx, givenTrustedVaultSelection(world.FromContext(ctx), name, false)
	})
	sc.Step(`^the trusted base configuration selects a missing Vault secret for "([A-Z][A-Z0-9_]*)"$`, func(ctx context.Context, name string) (context.Context, error) {
		return ctx, givenTrustedVaultSelection(world.FromContext(ctx), name, true)
	})
	sc.Step(`^a Vault fingerprint script "([^"]+)"$`, func(ctx context.Context, rel string) (context.Context, error) {
		return ctx, givenFullsendFile(world.FromContext(ctx), rel, vaultFingerprintScript)
	})
	sc.Step(`^a pull request that points "([A-Z][A-Z0-9_]*)" at another Vault secret$`, func(ctx context.Context, name string) (context.Context, error) {
		return ctx, givenUntrustedVaultOverridePR(ctx, world.FromContext(ctx), name)
	})
	sc.Step(`^the harness "([^"]+)" workflow logs contain the fingerprint of the Vault "([A-Z][A-Z0-9_]*)"$`, func(ctx context.Context, agent, name string) (context.Context, error) {
		return ctx, thenVaultFingerprintInLogs(ctx, world.FromContext(ctx), agent, name)
	})
	sc.Step(`^the harness "([^"]+)" workflow logs do not contain the fingerprint of the Vault "([A-Z][A-Z0-9_]*)" as "([A-Z][A-Z0-9_]*)"$`, func(ctx context.Context, agent, name, as string) (context.Context, error) {
		return ctx, thenVaultFingerprintNotInLogsAs(ctx, world.FromContext(ctx), agent, name, as)
	})
	sc.Step(`^the harness "([^"]+)" workflow logs do not contain the fingerprint of the overriding "([A-Z][A-Z0-9_]*)"$`, func(ctx context.Context, agent, name string) (context.Context, error) {
		return ctx, thenVaultOverrideFingerprintNotInLogs(ctx, world.FromContext(ctx), agent, name)
	})
	sc.Step(`^the trace collector received a trace from the harness "([^"]+)" run$`, func(ctx context.Context, agent string) (context.Context, error) {
		return ctx, thenTraceCollectorReceived(ctx, world.FromContext(ctx), agent)
	})
}

// givenVaultServer binds the feature's OpenBao server to the scenario,
// starting it if this is the feature's first Vault scenario. When the
// environment disables the Vault scenarios the scenario is skipped.
func givenVaultServer(w *world.World) error {
	feature := w.ScenarioURI
	if feature == "" {
		feature = "(unknown feature)"
	}
	server, skip, err := vaultServers.forFeature(feature, w.Logf)
	if err != nil {
		return err
	}
	if server == nil {
		worldLogf(w, "[vault] skipping %q: %s", w.ScenarioName, skip)
		return godog.ErrSkip
	}
	id, err := randomVaultHex(8)
	if err != nil {
		return err
	}
	w.VaultServer = server
	w.VaultScenarioID = "s-" + id
	worldLogf(w, "[vault] scenario %q uses OpenBao at %s with secret prefix %s", w.ScenarioName, server.Address(), w.VaultScenarioID)
	return nil
}

func requireVault(w *world.World) error {
	if w.VaultServer == nil {
		return fmt.Errorf("no Vault test server; call 'Given the Vault test server is running' first")
	}
	if w.Org == "" || w.RepoName == "" {
		return fmt.Errorf("no repo configured; call 'Given the enrolled test repository' before Vault steps")
	}
	return nil
}

// givenVaultAllowsRepository binds the leased repository to the server's
// JWT role, so its workflows can log in with their job OIDC token.
func givenVaultAllowsRepository(ctx context.Context, w *world.World) error {
	if err := requireVault(w); err != nil {
		return err
	}
	return w.VaultServer.AllowRepo(ctx, w.Org, w.RepoName)
}

// randomVaultHex returns n random bytes as hex.
func randomVaultHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generating random value: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// syntheticVaultValue returns a fresh synthetic value for name in the
// shape its consumer expects, and, for the runner-secrets bundle, the
// value of the bundle's single entry.
func syntheticVaultValue(name string) (value, bundleEntry string, err error) {
	r, err := randomVaultHex(16)
	if err != nil {
		return "", "", err
	}
	switch name {
	case "OTEL_EXPORTER_OTLP_HEADERS", vaultTracesHeadersName:
		return "x-behaviour-vault=" + r, "", nil
	case vaultRunnerSecretsName:
		entry := "behaviour-vault-" + r
		data, err := json.Marshal(map[string]string{vaultBundleEntry: entry})
		if err != nil {
			return "", "", err
		}
		return string(data), entry, nil
	default:
		return "behaviour-vault-" + r, "", nil
	}
}

// vaultSecretName is the path, under the server's scenario prefix, of
// the scenario's secret for name.
func vaultSecretName(w *world.World, kind, name string) string {
	return w.VaultScenarioID + "/" + kind + "-" + strings.ToLower(name)
}

// putVaultSecret stores value as the field name at the scenario's secret
// path and returns the KV v2 read path.
func putVaultSecret(ctx context.Context, w *world.World, kind, name, value string) (string, error) {
	registerSecretForms(value)
	return w.VaultServer.PutSecret(ctx, vaultSecretName(w, kind, name), map[string]string{name: value})
}

func recordVaultSecret(w *world.World, name, value, path string) {
	if w.VaultSecrets == nil {
		w.VaultSecrets = map[string]world.VaultSecret{}
	}
	w.VaultSecrets[name] = world.VaultSecret{Value: value, Path: path}
}

// givenVaultSyntheticSecret stores a fresh synthetic value for name on
// the server. For the runner-secrets bundle the bundle's entry is
// recorded too, so its fingerprint can be checked in a script that
// declares it.
func givenVaultSyntheticSecret(ctx context.Context, w *world.World, name string) error {
	if err := requireVault(w); err != nil {
		return err
	}
	value, entry, err := syntheticVaultValue(name)
	if err != nil {
		return err
	}
	if entry != "" {
		registerSecretForms(entry)
	}
	p, err := putVaultSecret(ctx, w, "trusted", name, value)
	if err != nil {
		return err
	}
	recordVaultSecret(w, name, value, p)
	if entry != "" {
		recordVaultSecret(w, vaultBundleEntry, entry, p)
	}
	return nil
}

// traceInspector returns the driver's trace collector view, or nil when
// no collector receives the leased repositories' traces.
func traceInspector(w *world.World) install.TraceInspector {
	ti, ok := w.Driver.(install.TraceInspector)
	if !ok || !ti.TraceCollectorRunning() {
		return nil
	}
	return ti
}

// givenVaultTraceCollectorHeaders stores the trace collector's bearer
// headers on the server as OTEL_EXPORTER_OTLP_TRACES_HEADERS. The
// scenario is skipped when no collector runs.
func givenVaultTraceCollectorHeaders(ctx context.Context, w *world.World) error {
	if err := requireVault(w); err != nil {
		return err
	}
	ti := traceInspector(w)
	if ti == nil {
		worldLogf(w, "[vault] skipping %q: no trace collector is running (BEHAVIOUR_TRACE_COLLECTOR)", w.ScenarioName)
		return godog.ErrSkip
	}
	headers := ti.TraceCollectorHeaders()
	p, err := putVaultSecret(ctx, w, "trusted", vaultTracesHeadersName, headers)
	if err != nil {
		return err
	}
	recordVaultSecret(w, vaultTracesHeadersName, headers, p)
	return nil
}

// givenNativeTracesHeadersWrong replaces the leased repository's native
// OTEL_EXPORTER_OTLP_TRACES_HEADERS secret, which the driver set to the
// collector's headers, with a wrong token, so a trace reaching the
// collector proves the Vault value was used. The repository is deleted
// at deallocation, which removes the secret.
func givenNativeTracesHeadersWrong(w *world.World) error {
	if w.Org == "" || w.RepoName == "" {
		return fmt.Errorf("no repo configured; call 'Given the enrolled test repository' first")
	}
	ghDriver, ok := w.SCM.(*scmgh.Driver)
	if !ok {
		return fmt.Errorf("the native traces headers secret requires the GitHub SCM driver")
	}
	r, err := randomVaultHex(16)
	if err != nil {
		return err
	}
	wrong := "Authorization=Bearer%20wrong-" + r
	registerSecretForms(wrong)
	if err := ghDriver.Client.CreateRepoSecret(mutationCtx(w), w.Org, w.RepoName, vaultTracesHeadersName, wrong); err != nil {
		return fmt.Errorf("setting repository secret %s: %w", vaultTracesHeadersName, err)
	}
	return nil
}

// givenTrustedVaultSelection selects the server for name in the leased
// repository's config.base.yaml, the trusted base layer. With missing it
// selects a path under the scenario prefix that holds no secret, so the
// lookup fails; otherwise it selects the path the scenario stored.
func givenTrustedVaultSelection(w *world.World, name string, missing bool) error {
	if err := requireVault(w); err != nil {
		return err
	}
	var p string
	if missing {
		p = path.Join("secret/data/fullsend-bt", vaultSecretName(w, "missing", name))
	} else {
		s, ok := w.VaultSecrets[name]
		if !ok {
			return fmt.Errorf("the scenario stored no Vault secret %s; add 'the Vault test server holds a synthetic %q' first", name, name)
		}
		p = s.Path
	}
	cfgPath := path.Join(".fullsend", "config.base.yaml")
	data, err := w.SCM.GetFileContent(context.Background(), w.Org, w.RepoName, cfgPath)
	if err != nil {
		if !forge.IsNotFound(err) {
			return fmt.Errorf("reading base config: %w", err)
		}
		data = nil
	}
	merged, err := withVaultSelection(data, w.VaultServer.Address(), name, p)
	if err != nil {
		return fmt.Errorf("updating base config: %w", err)
	}
	if err := w.SCM.CommitFile(context.Background(), w.Org, w.RepoName, cfgPath,
		fmt.Sprintf("behaviour: select Vault for %s in config.base.yaml", name), merged); err != nil {
		return fmt.Errorf("committing base config: %w", err)
	}
	return nil
}

// withVaultSelection returns the config document data with the
// scenario's Vault connection declared and name selecting it with the
// single read path p. Other keys are kept.
func withVaultSelection(data []byte, address, name, p string) ([]byte, error) {
	doc := map[string]any{}
	if len(strings.TrimSpace(string(data))) > 0 {
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return nil, fmt.Errorf("parsing config: %w", err)
		}
		if doc == nil {
			doc = map[string]any{}
		}
	}
	conns, err := yamlMapping(doc, "connections")
	if err != nil {
		return nil, err
	}
	conns[vaultConnectionName] = map[string]any{"kind": "vault", "vault_address": address}
	secrets, err := yamlMapping(doc, "secrets")
	if err != nil {
		return nil, err
	}
	secrets[name] = map[string]any{"connection": vaultConnectionName, "vault_secrets": []string{p}}
	return yaml.Marshal(doc)
}

// yamlMapping returns doc[key] as a mapping, creating it when absent.
func yamlMapping(doc map[string]any, key string) (map[string]any, error) {
	v, ok := doc[key]
	if !ok || v == nil {
		m := map[string]any{}
		doc[key] = m
		return m, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s is not a mapping", key)
	}
	return m, nil
}

// givenUntrustedVaultOverridePR stores a different value for name on
// the server and opens a pull request whose .fullsend/config.yaml
// selects that value instead of the trusted one. A run triggered from
// the pull request must keep using the trusted base configuration.
func givenUntrustedVaultOverridePR(ctx context.Context, w *world.World, name string) error {
	if err := requireVault(w); err != nil {
		return err
	}
	value, _, err := syntheticVaultValue(name)
	if err != nil {
		return err
	}
	p, err := putVaultSecret(ctx, w, "override", name, value)
	if err != nil {
		return err
	}
	if w.VaultOverrideValues == nil {
		w.VaultOverrideValues = map[string]string{}
	}
	w.VaultOverrideValues[name] = value

	cfgPath := path.Join(".fullsend", "config.yaml")
	data, err := w.SCM.GetFileContent(context.Background(), w.Org, w.RepoName, cfgPath)
	if err != nil {
		return fmt.Errorf("reading config: %w", err)
	}
	override, err := withVaultSelection(data, w.VaultServer.Address(), name, p)
	if err != nil {
		return fmt.Errorf("building override config: %w", err)
	}
	branch, err := openPullRequestWithContent(w, w.SCM, cfgPath, defaultPRBody, override)
	if branch != "" {
		w.CreatedBranches = append(w.CreatedBranches, branch)
	}
	if err != nil {
		return err
	}
	w.CreatedPRNumbers = append(w.CreatedPRNumbers, w.PRNumber)
	return nil
}

// thenVaultFingerprintInLogs checks that the harness run's pre-script
// printed the fingerprint of the scenario's Vault value for name.
func thenVaultFingerprintInLogs(ctx context.Context, w *world.World, agent, name string) error {
	s, ok := w.VaultSecrets[name]
	if !ok {
		return fmt.Errorf("the scenario stored no Vault secret %s", name)
	}
	return thenHarnessWorkflowLogsContain(ctx, w, agent, vaultFingerprintLine(name, s.Value), true)
}

// thenVaultFingerprintNotInLogsAs checks that the scenario's Vault value
// for name did not reach the run under the variable as, so two secrets
// with similar consumers (the generic and trace-specific OTLP headers)
// stay distinct.
func thenVaultFingerprintNotInLogsAs(ctx context.Context, w *world.World, agent, name, as string) error {
	s, ok := w.VaultSecrets[name]
	if !ok {
		return fmt.Errorf("the scenario stored no Vault secret %s", name)
	}
	return thenHarnessWorkflowLogsContain(ctx, w, agent, vaultFingerprintLine(as, s.Value), false)
}

// thenVaultOverrideFingerprintNotInLogs checks that the value an
// untrusted pull request pointed name at did not reach the run.
func thenVaultOverrideFingerprintNotInLogs(ctx context.Context, w *world.World, agent, name string) error {
	v, ok := w.VaultOverrideValues[name]
	if !ok {
		return fmt.Errorf("the scenario opened no pull request overriding %s", name)
	}
	return thenHarnessWorkflowLogsContain(ctx, w, agent, vaultFingerprintLine(name, v), false)
}

// thenTraceCollectorReceived waits until the trace collector holds a
// trace file, written since the scenario's trigger, carrying the
// harness's agent attribute and the leased repository.
func thenTraceCollectorReceived(ctx context.Context, w *world.World, agent string) error {
	ti := traceInspector(w)
	if ti == nil {
		return fmt.Errorf("no trace collector is running")
	}
	if w.ScenarioStart.IsZero() {
		return fmt.Errorf("no workflow trigger time recorded")
	}
	needles := []string{`"fullsend.agent"`, `"` + agent + `"`, w.Org + "/" + w.RepoName}
	deadline := time.Now().Add(vaultTraceWait)
	for {
		n, err := ti.TracesContaining(w.ScenarioStart, needles...)
		if err != nil {
			return fmt.Errorf("reading the trace collector's traces: %w", err)
		}
		if n > 0 {
			worldLogf(w, "[vault] trace collector holds %d trace file(s) from harness %q in %s/%s", n, agent, w.Org, w.RepoName)
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the trace collector received no trace from harness %q in %s/%s within %s of the run completing; "+
				"check the run log for the Vault resolution line and the OpenBao audit log (BEHAVIOUR_ARTIFACT_DIR/openbao) for its traceparent",
				agent, w.Org, w.RepoName, vaultTraceWait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(vaultTracePollTick):
		}
	}
}
