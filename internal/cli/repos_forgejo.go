package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/fullsend-ai/fullsend/internal/forge/forgejo"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// forgejoTestHooks replaces the live Forgejo client and environment
// lookup in tests.
type forgejoTestHooks struct {
	newClient repos.ForgejoClientFunc
	lookupEnv func(string) (string, bool)
}

// newForgejoLifecycle returns the Forgejo repository lifecycle for m.
func newForgejoLifecycle(m *repos.Manifest, dryRun bool, hooks *forgejoTestHooks) *repos.ForgejoLifecycle {
	lc := &repos.ForgejoLifecycle{
		Manifest: m,
		DryRun:   dryRun,
		NewClient: func(baseURL, token string) (repos.ForgejoFileClient, error) {
			return forgejo.New(baseURL, token, nil)
		},
		Lookup: os.LookupEnv,
	}
	if hooks != nil {
		if hooks.newClient != nil {
			lc.NewClient = hooks.newClient
		}
		if hooks.lookupEnv != nil {
			lc.Lookup = hooks.lookupEnv
		}
	}
	return lc
}

// othersFilter returns the selection for the github/gitlab lifecycle once
// the Forgejo part of filter has been split off. An empty filter keeps
// meaning "every declared repository".
func othersFilter(filter []string, sel repos.ForgejoSelection) []string {
	if len(filter) == 0 {
		return filter
	}
	return sel.Rest
}

// printForgejoResults writes one line per Forgejo repository.
func printForgejoResults(out io.Writer, results []repos.ForgejoRepoResult) {
	if len(results) == 0 {
		return
	}
	width := len("FORGEJO REPO")
	for _, r := range results {
		width = max(width, len(r.Selector))
	}
	fmt.Fprintf(out, "%-*s  %-14s  %s\n", width, "FORGEJO REPO", "STATUS", "DETAIL")
	for _, r := range results {
		fmt.Fprintf(out, "%-*s  %-14s  %s\n", width, r.Selector, r.State, r.Detail)
	}
}

// forgejoStatusError returns the status command's failure for Forgejo
// repositories that are not up to date.
func forgejoStatusError(results []repos.ForgejoRepoResult) error {
	counts := make(map[repos.ForgejoState]int)
	for _, r := range results {
		counts[r.State]++
	}
	if len(results) == counts[repos.ForgejoStateUpToDate] {
		return nil
	}
	return fmt.Errorf("forgejo: %d up to date, %d drifted, %d not installed, %d unmanaged, %d errored",
		counts[repos.ForgejoStateUpToDate], counts[repos.ForgejoStateDrift], counts[repos.ForgejoStateNotInstalled],
		counts[repos.ForgejoStateUnmanaged], counts[repos.ForgejoStateError])
}

// confirmForgejoTargets asks before acting on more than one Forgejo
// repository.
func confirmForgejoTargets(printer *ui.Printer, action string, targets []repos.ForgejoTarget, stdin *os.File) error {
	if len(targets) <= 1 {
		return nil
	}
	if !term.IsTerminal(int(stdin.Fd())) {
		return fmt.Errorf("stdin is not a terminal; use --yes to skip confirmation")
	}
	printer.StepWarn(fmt.Sprintf("This will %s %d Forgejo repos:", action, len(targets)))
	for _, t := range targets {
		printer.StepInfo("  " + t.Selector())
	}
	printer.StepInfo("Continue? [y/N]")
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil {
		return fmt.Errorf("reading confirmation: %w", err)
	}
	if strings.TrimSpace(strings.ToLower(line)) != "y" {
		return fmt.Errorf("aborted")
	}
	return nil
}

// runForgejoUninstall tears down the selected Forgejo repositories and,
// unless uninstallOnly, removes the torn-down entries from the manifest.
func runForgejoUninstall(opts *reposUninstallConfig, printer *ui.Printer, manifest *repos.Manifest, targets []repos.ForgejoTarget, run func() ([]repos.ForgejoRepoResult, error)) error {
	var removable []repos.ForgejoTarget
	if opts.manifestOnly {
		removable = targets
	} else {
		results, err := run()
		printForgejoResults(os.Stdout, results)
		for i, r := range results {
			if r.Err == nil {
				removable = append(removable, targets[i])
			}
		}
		if err != nil {
			if !opts.uninstallOnly && len(removable) > 0 && !opts.dryRun {
				if werr := removeForgejoFromManifest(opts, printer, manifest, removable); werr != nil {
					return fmt.Errorf("forgejo uninstall: %w; %w", err, werr)
				}
			}
			return fmt.Errorf("forgejo uninstall: %w", err)
		}
	}
	if opts.uninstallOnly || len(removable) == 0 {
		return nil
	}
	if opts.dryRun {
		for _, t := range removable {
			printer.StepInfo(fmt.Sprintf("[%s] Would remove from manifest", t.Selector()))
		}
		return nil
	}
	return removeForgejoFromManifest(opts, printer, manifest, removable)
}

func removeForgejoFromManifest(opts *reposUninstallConfig, printer *ui.Printer, manifest *repos.Manifest, targets []repos.ForgejoTarget) error {
	removed := manifest.RemoveForgejoRepos(targets)
	if err := repos.WriteManifest(opts.manifest, manifest); err != nil {
		return fmt.Errorf("writing manifest: %w", err)
	}
	for _, s := range removed {
		printer.StepDone(fmt.Sprintf("[%s] Removed from manifest", s))
	}
	return nil
}
