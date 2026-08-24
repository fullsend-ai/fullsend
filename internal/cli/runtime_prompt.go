package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"golang.org/x/term"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// defaultRuntimeChoice is what setup selects when the user does not choose.
const defaultRuntimeChoice = "claude"

// promptRuntime asks which agent runtime the per-repo config should select
// when --runtime was not given. It only prompts on an interactive terminal;
// otherwise (CI, pipes, --dry-run callers that pass interactive=false) it
// returns the default so setup never blocks. Enter or EOF keep the default.
// The choice is written to `runtime:` in .fullsend/config.yaml — the same
// key `fullsend github setup --runtime` and a per-run `--runtime` override.
func promptRuntime(printer *ui.Printer, in io.Reader, interactive bool) (string, error) {
	if !interactive {
		return "", nil
	}
	printer.Header("Agent Runtime")
	printer.Blank()
	printer.StepInfo("Choose the agent runtime for this repository:")
	printer.StepInfo("  [claude] Claude Code — stable default, recommended; all fleet agents, concurrent sub-agents")
	printer.StepInfo("  [pi]     pi — experimental (enablement phase); any provider pi supports, no sub-agent tool yet")
	printer.StepInfo("  Keep the default unless you are taking part in the pi pilot. Change later with `runtime:`")
	printer.StepInfo("  in .fullsend/config.yaml or `fullsend github setup --runtime`; see docs/runtimes.md.")
	printer.Blank()

	reader := bufio.NewReader(in)
	for {
		printer.StepInfo(fmt.Sprintf("Enter runtime [%s]: ", defaultRuntimeChoice))
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", fmt.Errorf("reading runtime choice: %w", err)
		}
		choice := strings.ToLower(strings.TrimSpace(line))
		if choice == "" {
			return "", nil // keep the default (not written to the overlay)
		}
		if slices.Contains(userRuntimeChoices(), choice) {
			return choice, nil
		}
		printer.StepWarn(fmt.Sprintf("Invalid runtime: %q (expected one of %s)", choice, strings.Join(userRuntimeChoices(), ", ")))
		if err == io.EOF {
			return "", nil
		}
	}
}

// userRuntimeChoices lists the runtimes offered to a person: every valid
// runtime except dummy, which exists for behaviour-test installs and is only
// ever selected with an explicit --runtime dummy.
func userRuntimeChoices() []string {
	var out []string
	for _, r := range config.ValidRuntimes() {
		if r != "dummy" {
			out = append(out, r)
		}
	}
	return out
}

// stdinIsInteractive reports whether stdin is a terminal.
func stdinIsInteractive() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}
