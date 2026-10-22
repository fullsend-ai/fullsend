package scaffold

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// triggerLedgerGeneration is a ready trigger-account ledger. The dispatch
// activation (triggerActivationRecord) is a separate protected variable bound
// to exactly this generation, not a ledger field: released CLI decoders reject
// unknown ledger fields before they read the version (#8438).
const triggerLedgerGeneration = `{"version":1,"phase":"ready","branch":"main","nonce":"0123456789abcdef","account_id":77,"deploy_key_id":5,"deploy_key_fingerprint":"SHA256:abc","grant_entry_id":6,"trigger_token_id":9,"trigger_published":true,"publish_started":true}`

// triggerActivationRecord is the activation record bound to
// triggerLedgerGeneration.
const triggerActivationRecord = `{"version":1,"nonce":"0123456789abcdef","account_id":77,"deploy_key_id":5,"grant_entry_id":6,"trigger_token_id":9}`

// fakeFullsendCLI writes a `fullsend` stub and returns its directory. It
// answers the `poll --print-dispatch-routes` capability probe like a current
// CLI when supportsRoute is true, and like a CLI that predates the route
// selector (unknown flag, nonzero exit) when it is false. Every other
// invocation prints the dispatch route the script exported.
func fakeFullsendCLI(t *testing.T, supportsRoute bool) string {
	t.Helper()
	bin := t.TempDir()
	probe := `echo "unknown flag: --print-dispatch-routes" >&2; exit 1`
	if supportsRoute {
		probe = `printf 'api\nchild-pipeline\n'; exit 0`
	}
	body := `#!/bin/sh
if [ "$1" = poll ]; then
  for arg in "$@"; do
    if [ "$arg" = --print-dispatch-routes ]; then
      ` + probe + `
    fi
  done
fi
echo "ROUTE=${FULLSEND_GITLAB_DISPATCH_ROUTE:-<unset>}"
`
	require.NoError(t, os.WriteFile(filepath.Join(bin, "fullsend"), []byte(body), 0o755))
	return bin
}

// withCLI returns a PATH entry that puts the fake CLI first.
func withCLI(t *testing.T, supportsRoute bool) string {
	t.Helper()
	return "PATH=" + fakeFullsendCLI(t, supportsRoute) + ":" + os.Getenv("PATH")
}
