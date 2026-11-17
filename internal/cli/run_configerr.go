package cli

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/normevent"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// reportConfigLoadFailure posts a failure status comment carrying loadErr
// when `fullsend run` cannot load its config.yaml (#9098). The run still
// fails closed: an unparseable config is never replaced by defaults, since
// that could bypass an explicit allowed_remote_resources (including a
// deny-all). Without this comment the run would exit before the regular
// status notifier is set up, so the PR or issue only ever saw a generic
// "Interrupted" comment and the config error stayed in the job log.
//
// The harness (and so its role) is not loaded yet, so the agent name is
// used as the minting role, matching the fallback reconcile-status uses
// when the harness role was never relayed. Jira-sourced runs are skipped:
// their status routing comes from the normalized event, which is resolved
// after config loading. All failures here are warnings only.
func reportConfigLoadFailure(ctx context.Context, agentName, fullsendDir, forgeFlag, eventFile string, sOpts statusOpts, runnerSecrets map[string]string, loadErr error, printer *ui.Printer) {
	if loadErr == nil || sOpts.statusRepo == "" || sOpts.statusNum <= 0 {
		return
	}
	if configErrEventClaimsJira(eventFile, fullsendDir) {
		printer.StepWarn("Config load failure not posted: Jira status routing is resolved after config loading")
		return
	}
	forgePlatform, err := detectForgePlatform(forgeFlag, nil)
	if err != nil {
		printer.StepWarn("Config load failure not posted: " + err.Error())
		return
	}
	notifier, err := setupStatusNotifier(fullsendDir, agentName, forgePlatform, sOpts, printer)
	if err != nil {
		printer.StepWarn("Config load failure not posted: " + err.Error())
		return
	}

	description := titleCase(strings.ReplaceAll(agentName, "-", " "))
	// The detail lands on the issue or PR, where Actions log masking does
	// not apply, so it gets the same redaction as the regular completion.
	detail := redactFeedback("fullsend config could not be loaded, agent did not run: "+loadErr.Error(), runnerSecrets)

	pCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	if err := notifier.PostStart(pCtx, description); err != nil {
		printer.StepWarn("Failed to post start status: " + err.Error())
	}
	if err := notifier.PostCompletionWithDetail(pCtx, description, "failure", detail); err != nil {
		printer.StepWarn("Failed to post config load failure status: " + err.Error())
		return
	}
	printer.StepDone("Posted config load failure status comment")
}

// configErrEventClaimsJira reports whether the run's event (from
// --event-file or the dispatch payload) names Jira as its source, or
// whether that cannot be determined from an unreadable --event-file.
func configErrEventClaimsJira(eventFile, fullsendDir string) bool {
	if eventFile != "" {
		data, err := os.ReadFile(eventFile)
		if err != nil {
			return true
		}
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			return true
		}
		return extractMapString(raw, "source", "system") == string(normevent.SystemJira)
	}
	return dispatchPayloadClaimsJira(fullsendDir)
}
