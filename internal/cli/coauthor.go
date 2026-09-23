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

// contextInstructionFiles are the root instruction files where a repo
// states contributor conventions. Names match hasAgentsMD / hasClaudeMD.
var contextInstructionFiles = []string{
	"AGENTS.md", "agents.md", "Agents.md",
	"CLAUDE.md", "claude.md", "Claude.md", ".claude.md",
}

// coAuthoredByProhibitRe matches an explicit ban on Co-authored-by trailers.
// It requires a negation ("do not", "don't", "never", "no", "without") next
// to the trailer name so a repo that requires or merely mentions
// Co-authored-by is not treated as a ban.
var coAuthoredByProhibitRe = regexp.MustCompile(`(?i)` +
	`(?:do\s+not|don['’]t|never)\s+(?:add|include|use)\s+(?:a\s+|any\s+|the\s+)?` + "`*" + `co-authored-by` +
	`|\b(?:no|without)\s+` + "`*" + `co-authored-by` +
	`|co-authored-by.{0,80}(?:is\s+)?(?:forbidden|prohibited|disallowed|banned)`)

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
	return coAuthoredByProhibitRe.MatchString(content)
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
// does not leak out of the sandbox. A failed install is a warning: the
// Claude Code setting still covers that runtime.
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
