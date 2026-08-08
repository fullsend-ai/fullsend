// Package prescript implements the pre-script output protocol (issue #4718).
//
// fullsend run hands the harness pre-script an output file path via the
// FULLSEND_PRESCRIPT_OUTPUT environment variable. The script may append
// key=value lines to that file — most notably "skipped=true" plus an
// optional "reason=..." — which fullsend run reads back after the script
// exits. When skipped=true, fullsend run reports a "skipped" status and
// exits successfully before creating the sandbox.
//
// The protocol is specified normatively in docs/normative/prescript-output/v1;
// that document, not this comment, is what pre-script authors write
// against. The summary below is a convenience for readers of this package.
//
// Version skew is the same class of concern as ADR 0062, and is handled
// asymmetrically — deliberately, because the two directions have different
// costs:
//
//   - Old script, new CLI: the script never writes to the file, which
//     parses to a Result with Skipped false and no outputs — exactly
//     today's behavior.
//   - New script, old CLI: FULLSEND_PRESCRIPT_OUTPUT is unset, so the
//     script cannot signal a skip and the agent runs anyway (a duplicate
//     run — this direction fails open). Scripts must guard on the variable
//     being unset, the same pattern as the existing GITHUB_OUTPUT guard in
//     the scaffold scripts.
//
// Malformed content is a hard error, not a silent proceed: a typo like
// "skipped true" silently ignored would let a duplicate agent run start —
// the exact failure mode the pre-checks exist to prevent. Values that
// case-insensitively match a reserved key but differ in case (SKIPPED=true)
// are rejected for the same reason.
//
// Supported syntax (line-based):
//
//	skipped=true
//	reason=an open PR already addresses this issue
//	# comments and blank lines are ignored
//
// The format resembles GITHUB_OUTPUT but is not compatible with it: there
// is no quoting, and the "key<<DELIMITER" heredoc form for multiline
// values is not supported. Values are single-line and may not contain
// control characters. Surrounding whitespace around both key and value is
// not significant. Later assignments to the same key win.
package prescript

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// EnvVar is the environment variable through which fullsend run passes the
// output file path to the pre-script. Scripts must guard on it being unset
// (older CLI versions do not export it).
const EnvVar = "FULLSEND_PRESCRIPT_OUTPUT"

// ExitCodeNeutral is the exit code a pre-script uses to signal "nothing to
// do, skip cleanly" (issue #582). When fullsend run sees this exit code it
// treats the run as skipped/neutral — no sandbox is created, no LLM is
// invoked, and the run reports a ⏭️ skipped status. The code follows the
// CI convention for "neutral" (used by GitHub Actions and others).
//
// Exit 78 is complementary to the file-based skip protocol (skipped=true).
// Either mechanism alone is sufficient to request a skip. When both are
// present, exit 78 wins — even if the output file says skipped=false.
const ExitCodeNeutral = 78

const (
	skippedKey = "skipped"
	reasonKey  = "reason"

	// maxOutputSize caps the output file read, mirroring internal/envfile.
	maxOutputSize = 1 << 20 // 1 MB
)

// validKeyRe allows hyphens as well as underscores: GitHub Actions output
// names conventionally use hyphens, and rejecting them would turn a
// perfectly ordinary key into a hard run failure.
var validKeyRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_-]*$`)

// heredocRe matches GITHUB_OUTPUT's "key<<DELIMITER" multiline form, which
// this protocol does not support. Anchored at the key so it cannot fire on
// a value that merely contains "<<".
var heredocRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_-]*<<`)

// reservedKeys are the keys the protocol itself interprets. A key that
// differs from one of these only by case is rejected rather than stored as
// an unrelated output — see the package doc on silent-proceed failures.
var reservedKeys = []string{skippedKey, reasonKey}

// Result is the parsed pre-script output.
type Result struct {
	// Skipped is true when the pre-script wrote skipped=true, requesting
	// that fullsend run stop before sandbox creation.
	Skipped bool
	// Reason is the optional human-readable reason accompanying a skip.
	Reason string
	// Outputs holds every key=value pair the script wrote, including
	// skipped and reason, with later assignments overriding earlier ones.
	Outputs map[string]string
}

// Prepare creates the file the pre-script writes its outputs to, inside
// dir. Using the run directory rather than the system temp directory keeps
// the file out of reach of tmp reapers during a long pre-script. The
// cleanup func removes the file and never fails loudly.
func Prepare(dir string) (path string, cleanup func(), err error) {
	f, err := os.CreateTemp(dir, "prescript-*.out")
	if err != nil {
		return "", nil, fmt.Errorf("creating pre-script output file: %w", err)
	}
	name := f.Name()
	if err := f.Close(); err != nil {
		_ = os.Remove(name)
		return "", nil, fmt.Errorf("closing pre-script output file: %w", err)
	}
	return name, func() { _ = os.Remove(name) }, nil
}

// ParseFile reads a pre-script output file. An empty file yields a Result
// with Skipped false and no outputs (proceed). Malformed lines, invalid
// keys, values containing control characters, and skipped values other
// than "true"/"false" are hard errors.
//
// A missing file is also a hard error: Prepare creates the file before the
// script runs, so its absence afterwards means something removed it, and
// treating that as "proceed" is the silent-proceed failure this protocol
// exists to prevent.
func ParseFile(path string) (Result, error) {
	res := Result{Outputs: map[string]string{}}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return res, fmt.Errorf("pre-script output file %s is missing — it was created before the "+
				"pre-script ran, so the script must have removed it", filepath.Base(path))
		}
		return res, fmt.Errorf("opening pre-script output: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return res, fmt.Errorf("stat pre-script output: %w", err)
	}
	if info.Size() > maxOutputSize {
		return res, fmt.Errorf("pre-script output %s exceeds maximum size (%d bytes)", path, maxOutputSize)
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(strings.TrimRight(scanner.Text(), "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Checked before the key=value split so a delimiter containing "="
		// (reason<<E=OF) still gets the message naming the limitation.
		// Anchored to the key position so an ordinary value containing
		// "<<" (reason=shift left << 2) is unaffected.
		if heredocRe.MatchString(line) {
			return res, fmt.Errorf("pre-script output line %d: %q uses GITHUB_OUTPUT heredoc syntax, "+
				"which this protocol does not support — values must be single-line key=value", lineNum, line)
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			return res, fmt.Errorf("pre-script output line %d: %q is not key=value", lineNum, line)
		}
		key = strings.TrimSpace(key)
		if !validKeyRe.MatchString(key) {
			return res, fmt.Errorf("pre-script output line %d: invalid key %q", lineNum, key)
		}
		if canonical, ok := miscasedReservedKey(key); ok {
			return res, fmt.Errorf("pre-script output line %d: key %q differs only in case from the reserved "+
				"key %q — reserved keys are lowercase", lineNum, key, canonical)
		}
		value = strings.TrimSpace(value)
		if i := strings.IndexFunc(value, isControlChar); i >= 0 {
			return res, fmt.Errorf("pre-script output line %d: value for %q contains a control character (%#U) "+
				"at offset %d — values must be plain single-line text", lineNum, key, rune(value[i]), i)
		}
		res.Outputs[key] = value
	}
	if err := scanner.Err(); err != nil {
		return res, fmt.Errorf("reading pre-script output: %w", err)
	}

	// An absent skipped key means proceed. A key that is *present but
	// empty* is a different, far more suspicious state — `skipped=${SKIP}`
	// with SKIP unset — and treating it as "proceed" would be the silent
	// duplicate run this protocol exists to prevent.
	v, present := res.Outputs[skippedKey]
	switch {
	case !present:
		res.Skipped = false
	case v == "false":
		res.Skipped = false
	case v == "true":
		res.Skipped = true
	default:
		return res, fmt.Errorf("pre-script output: skipped must be \"true\" or \"false\", got %q", v)
	}
	res.Reason = res.Outputs[reasonKey]
	return res, nil
}

// miscasedReservedKey reports whether key matches a reserved key
// case-insensitively without matching it exactly, returning the reserved
// spelling the author probably meant.
func miscasedReservedKey(key string) (string, bool) {
	for _, reserved := range reservedKeys {
		if key != reserved && strings.EqualFold(key, reserved) {
			return reserved, true
		}
	}
	return "", false
}

// isControlChar reports whether r is a C0 control character or DEL. These
// are rejected in values because a bare \r or \n written into
// GITHUB_OUTPUT would be read as a line terminator by the Actions runner,
// letting a value smuggle in additional output entries — including an
// override of skipped itself.
func isControlChar(r rune) bool {
	return r < 0x20 || r == 0x7f
}

// validateOutputs re-checks what ParseFile already enforces. Relay and
// LogLine are exported and a hand-built Result must not be able to inject
// extra GITHUB_OUTPUT entries — a newline in a *key* smuggles a line just
// as effectively as one in a value, including an override of skipped.
func validateOutputs(outputs map[string]string) error {
	for k, v := range outputs {
		if !validKeyRe.MatchString(k) {
			return fmt.Errorf("refusing to relay invalid key %q", k)
		}
		if strings.IndexFunc(v, isControlChar) >= 0 {
			return fmt.Errorf("refusing to relay value for %q: contains a control character", k)
		}
	}
	return nil
}

// Relay surfaces the pre-script outputs to the surrounding CI system so
// remaining workflow-level gating (if any) can consume them. Today only
// GitHub Actions is detected; other CIs get the outputs in the run log
// only (fullsend run logs them on every path). Returns whether a relay
// target was found.
//
// Callers must invoke Relay on both the skip and proceed paths: the
// skipped key is always written (normalized to "true"/"false") so
// consumers can gate on its value, and an absent key means the CLI
// predates this protocol rather than "not skipped".
func Relay(res Result) (relayed bool, err error) {
	target := os.Getenv("GITHUB_OUTPUT")
	// Gate on GITHUB_ACTIONS as well as the file path, matching
	// detectForgePlatform: a stray exported GITHUB_OUTPUT in a local shell
	// must not cause fullsend to append to an unrelated file.
	if target == "" || os.Getenv("GITHUB_ACTIONS") != "true" {
		return false, nil
	}

	if err := validateOutputs(res.Outputs); err != nil {
		return false, err
	}

	f, err := os.OpenFile(target, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return false, fmt.Errorf("opening GITHUB_OUTPUT: %w", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("closing GITHUB_OUTPUT: %w", cerr)
			relayed = false
		}
	}()

	keys := make([]string, 0, len(res.Outputs))
	for k := range res.Outputs {
		if k != skippedKey {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	var b strings.Builder
	fmt.Fprintf(&b, "%s=%t\n", skippedKey, res.Skipped)
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, res.Outputs[k])
	}
	if _, err := f.WriteString(b.String()); err != nil {
		return false, fmt.Errorf("writing GITHUB_OUTPUT: %w", err)
	}
	return true, nil
}

// LogLine renders the outputs as a single stable, sorted line for the run
// log, so non-GitHub CIs (and local runs) can still see what the
// pre-script reported. Returns "" when there is nothing to show, or when
// an output would break the single-line rendering.
func LogLine(res Result) string {
	if err := validateOutputs(res.Outputs); err != nil {
		return ""
	}

	keys := make([]string, 0, len(res.Outputs))
	for k := range res.Outputs {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", k, res.Outputs[k]))
	}
	return strings.Join(parts, " ")
}
