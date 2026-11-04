package steps

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"

	"github.com/fullsend-ai/fullsend/internal/evalmeasure"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/artifacts"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// preScriptSkippedAttr is the root "run" span attribute fullsend run sets
// for harnesses with a pre-script; true means the pre-script requested a
// skip and the run ended before sandbox creation.
const preScriptSkippedAttr = "fullsend.prescript.skipped"

// agentOutputMarkers are files that only exist in a harness artifact when
// the agent itself ran: the structured result and the dummy runtime's
// behaviour report.
var agentOutputMarkers = []string{"agent-result.json", "behaviour-results.json"}

func registerPreScriptSkipSteps(sc *godog.ScenarioContext) {
	sc.Step(`^the harness "([^"]+)" run was skipped by its pre-script$`, func(ctx context.Context, agent string) (context.Context, error) {
		return ctx, thenHarnessRunSkippedByPreScript(world.FromContext(ctx), agent)
	})
}

// thenHarnessRunSkippedByPreScript downloads the fullsend-<agent> artifact
// of the workflow run the scenario is tracking and requires that the host
// telemetry recorded a pre-script skip and that no agent output exists.
// A skipped run still uploads its artifact and its workflow still
// succeeds, so neither the artifact's presence nor the run's conclusion
// can tell a skip from a run where the agent ran.
func thenHarnessRunSkippedByPreScript(w *world.World, agent string) error {
	agent = strings.TrimSpace(agent)
	if w.WorkflowRun == nil {
		return fmt.Errorf("no workflow run recorded; assert the harness %q workflow completed first", agent)
	}
	runID := w.WorkflowRun.ID
	dest, err := prepareArtifactDir()
	if err != nil {
		return err
	}
	defer os.RemoveAll(dest)
	artifactName := "fullsend-" + agent
	if err := w.CI.DownloadNamedArtifactFromRun(context.Background(), w.Org, w.RepoName, runID, artifactName, dest); err != nil {
		return fmt.Errorf("harness %q run %d: downloading artifact %s: %w", agent, runID, artifactName, err)
	}
	root, err := extractedArtifactRoot(dest)
	if err != nil {
		return fmt.Errorf("harness %q run %d: artifact %s: %w", agent, runID, artifactName, err)
	}
	if err := checkPreScriptSkipped(root); err != nil {
		return fmt.Errorf("harness %q run %d was not skipped by its pre-script: %w", agent, runID, err)
	}
	return nil
}

// extractedArtifactRoot returns the directory a CI driver extracted one
// named artifact into. dest is fresh, and the drivers extract into
// dest/<driver artifact name>/: fullsend-<agent> on GitHub, the job name
// ("fullsend <agent> agent") on GitLab.
func extractedArtifactRoot(dest string) (string, error) {
	entries, err := os.ReadDir(dest)
	if err != nil {
		return "", err
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	if len(dirs) != 1 {
		return "", fmt.Errorf("expected one extracted artifact directory, found %d", len(dirs))
	}
	return filepath.Join(dest, dirs[0]), nil
}

// hostRunDirBases are where the run directories sit inside an extracted
// harness artifact: at its root on GitHub (upload-artifact strips the
// output/ prefix), under output/ on GitLab (artifacts:paths keeps it).
var hostRunDirBases = []string{".", "output"}

// checkPreScriptSkipped inspects a downloaded harness artifact. It reads
// the host-written run-telemetry.jsonl (never an agent-writable nested
// copy) and fails, naming everything it found, unless the root run span
// has fullsend.prescript.skipped=true and no agent output file exists.
func checkPreScriptSkipped(artifactDir string) error {
	var problems []string

	skipped, skipErr := preScriptSkippedFromTelemetry(artifactDir)
	switch {
	case skipErr != nil:
		problems = append(problems, skipErr.Error())
	case !skipped:
		problems = append(problems, preScriptSkippedAttr+"=false")
	}

	for _, name := range agentOutputMarkers {
		if _, err := artifacts.FindOutputFile(artifactDir, name); err == nil {
			problems = append(problems, name+" present (the agent ran)")
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return nil
}

// preScriptSkippedFromTelemetry returns the root run span's
// fullsend.prescript.skipped value from the artifact's platform telemetry.
func preScriptSkippedFromTelemetry(artifactDir string) (bool, error) {
	var paths []string
	for _, base := range hostRunDirBases {
		found, err := evalmeasure.FindPlatformTelemetry(filepath.Join(artifactDir, base), "")
		if err != nil {
			return false, fmt.Errorf("locating %s: %w", evalmeasure.PlatformTelemetryFile, err)
		}
		if len(found) > 0 {
			paths = found
			break
		}
	}
	if len(paths) == 0 {
		return false, fmt.Errorf("no host %s in artifact", evalmeasure.PlatformTelemetryFile)
	}
	traces, _, err := evalmeasure.ParseTelemetryFile(paths[0])
	if err != nil && len(traces) == 0 {
		return false, fmt.Errorf("reading %s: %w", evalmeasure.PlatformTelemetryFile, err)
	}
	for _, tr := range traces {
		run, ok := tr.SpanByName("run")
		if !ok {
			continue
		}
		skipped, ok := run.AttrBool(preScriptSkippedAttr)
		if !ok {
			return false, fmt.Errorf("run span has no %s attribute (the harness has no pre-script)", preScriptSkippedAttr)
		}
		return skipped, nil
	}
	return false, fmt.Errorf("no run span in %s", evalmeasure.PlatformTelemetryFile)
}
