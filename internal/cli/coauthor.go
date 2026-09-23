package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// agentsMDFilenames are the accepted root-directory casings for AGENTS.md.
// Shared with hasAgentsMD (run.go) so the two lists cannot drift apart.
var agentsMDFilenames = []string{"AGENTS.md", "agents.md", "Agents.md"}

// claudeMDFilenames are the accepted root-directory casings for CLAUDE.md.
// Shared with hasClaudeMD (run.go) so the two lists cannot drift apart.
var claudeMDFilenames = []string{"CLAUDE.md", "claude.md", "Claude.md", ".claude.md"}

// contextInstructionFiles are the root instruction files where a repo
// states contributor conventions.
var contextInstructionFiles = append(append([]string{}, agentsMDFilenames...), claudeMDFilenames...)

// coAuthoredByProhibitRe matches an explicit ban on Co-authored-by trailers.
// It requires a negation ("do not", "don't", "never", "no") next to the
// trailer name, or "co-authored-by" followed by a forbidden/prohibited/
// disallowed/banned clause across a short, whitespace-only gap, so a repo
// that requires or merely mentions Co-authored-by is not treated as a ban.
// The gap is bounded to a couple of words and cannot itself contain "not" —
// unlike a free-form `.{0,80}` gap, it cannot bridge across an intervening
// negation such as "Co-authored-by is not forbidden". A bare "without" is
// handled separately by coAuthoredByWithoutRe below, since "without" alone
// is ambiguous without also checking what precedes the verb it attaches to.
var coAuthoredByProhibitRe = regexp.MustCompile(`(?i)` +
	`(?:do\s+not|don['’]t|never)\s+(?:add|include|use)\s+(?:a\s+|any\s+|the\s+)?` + "`*" + `co-authored-by` +
	`|\bno\s+` + "`*" + `co-authored-by` +
	`|co-authored-by\s*(?:trailers?|lines?|tags?|entries?)?\s{0,20}(?:is\s+|are\s+)?(?:forbidden|prohibited|disallowed|banned)`)

// coAuthoredByWithoutRe matches "commit/merge without Co-authored-by",
// which reads as a ban on the trailer only when the verb itself is not
// negated. It requires a verb before "without" so unrelated uses of the
// word elsewhere in the file are ignored.
var coAuthoredByWithoutRe = regexp.MustCompile(`(?i)\b(?:commit(?:s|ting)?|merg(?:e|ing))\s+without\s+(?:a\s+|any\s+|the\s+)?` + "`*" + `co-authored-by`)

// negatedVerbBeforeRe matches a negation word ("never", "not", "cannot",
// "don't", ...) shortly before a position. Used to check the text preceding
// a coAuthoredByWithoutRe match: "never merge without Co-authored-by" is a
// requirement (the trailer is mandatory), not a ban, because the negation
// applies to the verb rather than to the trailer. Go's RE2 engine has no
// lookbehind, so this is applied against a slice of the preceding text
// instead of embedded in coAuthoredByWithoutRe directly.
//
// "cannot"/"can't"/"couldn't" are listed explicitly rather than relying on
// \bnot matching inside them: RE2's \b is a \w/\W boundary, and there is no
// such boundary between "can" and "not" in "cannot" (both are word
// characters), so a bare "not" alternative never fires there. The trailing
// `(?:\s+\S+){0,3}\s*$` allows up to three intervening words (e.g. "never
// ever merge") between the negation and the verb, rather than only a single
// token immediately before the end of the window.
var negatedVerbBeforeRe = regexp.MustCompile(`(?i)\b(?:never|not|cannot|can['’]t|couldn['’]t|don['’]t|doesn['’]t|won['’]t|wouldn['’]t|shouldn['’]t|didn['’]t|isn['’]t)(?:\s+\S+){0,3}\s*$`)

// coAuthorStripHookScript is a commit-msg hook that drops Co-authored-by
// trailers. Other trailers (Assisted-by, Signed-off-by, Closes) are left
// alone. The hook never fails the commit: a rewrite error is ignored so a
// sandbox awk/mv problem cannot block an otherwise valid agent commit.
const coAuthorStripHookScript = `#!/bin/sh
# fullsend: strip Co-authored-by trailers when the target repo prohibits them.
msg_file=$1
if [ -z "$msg_file" ] || [ ! -f "$msg_file" ]; then
  exit 0
fi
tmp="${msg_file}.fullsend-coauthor-strip"
awk '{
  lower=tolower($0)
  if (lower ~ /^co-authored-by:/) next
  print
}' "$msg_file" > "$tmp" || exit 0
mv "$tmp" "$msg_file" || exit 0
`

// repoProhibitsCoAuthoredBy reports whether a checkout's root AGENTS.md or
// CLAUDE.md explicitly forbids Co-authored-by trailers. orgAgentsMD is the
// org-default AGENTS.md that run.go injects when the repo has none; it is
// consulted only in that case so a repo that wrote its own AGENTS.md is not
// bound by the org file's attribution rule.
func repoProhibitsCoAuthoredBy(repoDir, orgAgentsMD string) bool {
	for _, name := range contextInstructionFiles {
		if fileProhibitsCoAuthoredBy(filepath.Join(repoDir, name)) {
			return true
		}
	}
	if hasAgentsMD(repoDir) {
		return false
	}
	return fileProhibitsCoAuthoredBy(orgAgentsMD)
}

// fileProhibitsCoAuthoredBy reports whether the file at path contains an
// explicit Co-authored-by prohibition. Missing or unreadable files are not
// a prohibition.
func fileProhibitsCoAuthoredBy(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return prohibitsCoAuthoredBy(string(data))
}

func prohibitsCoAuthoredBy(content string) bool {
	if coAuthoredByProhibitRe.MatchString(content) {
		return true
	}
	// Check every "commit/merge without Co-authored-by" occurrence, not just
	// the first: an earlier negated occurrence (a requirement) does not rule
	// out a later, un-negated occurrence that is an actual ban.
	const window = 40
	for _, loc := range coAuthoredByWithoutRe.FindAllStringIndex(content, -1) {
		prefix := content[:loc[0]]
		if len(prefix) > window {
			prefix = prefix[len(prefix)-window:]
		}
		if !negatedVerbBeforeRe.MatchString(prefix) {
			return true
		}
	}
	return false
}

// applyCoAuthorSuppress flags Claude Code's --settings file to disable the
// runtime's automatic Co-authored-by trailer. No-op when security hooks are
// not installed (the git commit-msg hook still covers that path).
func applyCoAuthorSuppress(boot runtime.BootstrapInput) runtime.BootstrapInput {
	hw, ok := boot.(*harnessBootstrapWithHooks)
	if !ok {
		return boot
	}
	next := *hw
	next.hooks = hw.hooks.WithSuppressCoAuthoredBy()
	return &next
}

// prepareCoAuthorSuppress flags Claude Code --settings when the repo (or the
// org default AGENTS.md that will be injected) forbids Co-authored-by. The
// bool is the same signal maybeInstallCoAuthorStripHook uses after the clone
// is copied into the sandbox.
func prepareCoAuthorSuppress(hostRepoDir, orgAgentsMD string, boot runtime.BootstrapInput) (runtime.BootstrapInput, bool) {
	if !repoProhibitsCoAuthoredBy(hostRepoDir, orgAgentsMD) {
		return boot, false
	}
	return applyCoAuthorSuppress(boot), true
}

// maybeInstallCoAuthorStripHook writes a commit-msg hook into the sandbox
// clone so every runtime strips Co-authored-by trailers at git commit time.
// SafeDownload removes .git/hooks on the way back to the host, so the hook
// does not leak out of the sandbox. A failed install is a warning, not a
// fatal error, matching the hook script's own fail-open behavior: the
// Claude Code setting still covers that runtime, but a non-Claude-Code
// runtime whose install fails is not guaranteed to have the trailer
// stripped from that commit.
func maybeInstallCoAuthorStripHook(sandboxName, remoteRepositoryDir string, suppress bool, printer *ui.Printer, execFn sandboxExecFunc) {
	if !suppress {
		return
	}
	if err := doInstallCoAuthorStripHook(sandboxName, remoteRepositoryDir, printer, execFn); err != nil {
		printer.StepWarn("Could not install Co-authored-by strip hook: " + err.Error())
	}
}

func doInstallCoAuthorStripHook(sandboxName, remoteRepositoryDir string, printer *ui.Printer, execFn sandboxExecFunc) error {
	// Quoted heredoc keeps $1 and newlines literal. Go %q would expand $
	// inside the outer sh -c and turn real newlines into \n.
	hooksDir := remoteRepositoryDir + "/.git/hooks"
	hookPath := hooksDir + "/commit-msg"
	writeCmd := fmt.Sprintf(
		"mkdir -p %s && cat > %s << 'FULLSEND_COAUTHOR_HOOK'\n%sFULLSEND_COAUTHOR_HOOK\nchmod 755 %s",
		shellQuote(hooksDir), shellQuote(hookPath), coAuthorStripHookScript, shellQuote(hookPath),
	)
	if _, stderr, exitCode, err := execFn(sandboxName, writeCmd, 10*time.Second); err != nil {
		return fmt.Errorf("installing Co-authored-by strip hook: %w", err)
	} else if exitCode != 0 {
		return fmt.Errorf("installing Co-authored-by strip hook: exit %d: %s", exitCode, stderr)
	}
	printer.StepDone("Honoring repo no-attribution rule: Co-authored-by trailers will be stripped")
	return nil
}
