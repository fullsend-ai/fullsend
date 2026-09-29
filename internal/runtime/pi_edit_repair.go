package runtime

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
)

// The edit-repair extension is a stopgap for a pi defect: some models send
// the edit tool's `edits` as a JSON string holding raw control characters,
// or as an array of JSON strings, and pi's own argument preparation rejects
// both with `edits.0: must be object` (earendil-works/pi#8521, #8962). It
// re-registers pi's edit tool with the argument repair in front; see
// pi_extension/fullsend-edit-repair.js.
//
// Remove this file, the extension and their call sites once the pinned
// PI_VERSION (images/sandbox/Containerfile) repairs both shapes itself.

// piEditRepairExtensionFile is the embedded extension Bootstrap writes under
// ConfigDir and Run loads with -e when the agent has the edit tool.
const piEditRepairExtensionFile = "fullsend-edit-repair.js"

// piEditToolName is pi's edit tool, the only tool the extension registers.
const piEditToolName = "edit"

// piEditRepairTamperedExit is the exit code of the extension's integrity
// guard. Distinct from the other guards' codes so Run can name the file.
const piEditRepairTamperedExit = 93

//go:embed pi_extension/fullsend-edit-repair.js
var piEditRepairExtensionJS []byte

// piEditRepairEnabled reports whether the extension loads for a pi tool
// list: nil is the default set, which includes edit; a declared list must
// name edit. The gate is a security boundary, not an optimisation: under
// --no-builtin-tools with no --tools list pi does not filter extension
// tools, so loading the extension there would grant edit to an agent whose
// definition withholds it.
func piEditRepairEnabled(tools []string) bool {
	if tools == nil {
		return hasTool(piDefaultTools, piEditToolName)
	}
	return hasTool(tools, piEditToolName)
}

// piEditRepairGuard is the extension's counterpart of piAgentGuard: the file
// must exist and match the embedded copy, else piEditRepairTamperedExit. It
// sits in the config dir the agent can write between iterations. A deleted
// copy already fails closed in pi itself, which exits 1 on a missing -e
// path; the guard exists for a rewritten copy, which pi would otherwise
// load and run, and to give tampering its own, distinguishable exit code.
func piEditRepairGuard(ext string) string {
	sum := sha256.Sum256(piEditRepairExtensionJS)
	return fmt.Sprintf(`{ test -f %s && [ "$(command -p sha256sum %s | command -p cut -d' ' -f1)" = %s ] || { echo 'fullsend: pi edit-repair extension missing or modified; refusing to run' >&2; exit %d; }; }`,
		shellQuote(ext), shellQuote(ext), shellQuote(hex.EncodeToString(sum[:])), piEditRepairTamperedExit)
}
