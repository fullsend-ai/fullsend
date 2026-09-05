package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/evalmeasure"
	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/forge"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func newEvalMeasureCmd() *cobra.Command {
	var (
		telemetryPath string
		registryPath  string
		outDir        string
		outputDir     string
		agent         string
		fullsendDir   string
		offline       bool
	)

	cmd := &cobra.Command{
		Use:   "eval-measure",
		Short: "Score agent run traces with eval measurements",
		Long: `Parse run-telemetry.jsonl, score with an agents measurement manifest,
and write eval-measurements.jsonl beside the telemetry artifact.

When OTEL_EXPORTER_OTLP_ENDPOINT or OTEL_EXPORTER_OTLP_TRACES_ENDPOINT is
set (same env as agent traces, ADR 0050), newly written scores are also
exported as OTLP span events (gen_ai.evaluation.result) on the scored
trace. Export is fail-open: local JSONL always wins.

The binary resolves the manifest (local FULLSEND_DIR override, else a
SHA-pinned fetch from fullsend-ai/agents — same pin, allowlist, hash, and
audit as harness fallback). Platform telemetry is the file at the top of
each run directory; nested iteration-N/output/ copies are ignored.

Remote backends are not selected by fullsend: scores are a portable local
JSONL artifact plus optional OTLP on the shared OTEL_* path (ADR 0087).
No vendor score adapters (MLflow Assessments, Phoenix SDK, …) in core.

Exit 0 when scores fail — measurements are data, not gates. Non-zero only
on hard IO/parse errors. Missing telemetry or manifest is a skip (exit 0).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			printer := ui.New(cmd.OutOrStdout())
			printer.Header("Eval Measure")

			results, skipped, err := runEvalMeasure(cmd.Context(), printer, evalMeasureOpts{
				telemetryPath: telemetryPath,
				registryPath:  registryPath,
				outDir:        outDir,
				outputDir:     outputDir,
				agent:         agent,
				fullsendDir:   fullsendDir,
				offline:       offline,
			})
			if err != nil {
				printMeasurementResults(printer, results, false)
				return err
			}
			if skipped {
				return nil
			}
			printMeasurementResults(printer, results, true)
			return nil
		},
	}

	cmd.Flags().StringVar(&telemetryPath, "telemetry", "", "path to run-telemetry.jsonl (or use --output-dir)")
	cmd.Flags().StringVar(&registryPath, "registry", "", "path to agents measurement manifest YAML (or use --agent)")
	cmd.Flags().StringVar(&outDir, "out-dir", "", "directory for eval-measurements.jsonl (default: telemetry directory)")
	cmd.Flags().StringVar(&outputDir, "output-dir", "", "CI output base or runDir; scores only top-of-runDir telemetry")
	cmd.Flags().StringVar(&agent, "agent", "", "agent name for manifest resolution")
	cmd.Flags().StringVar(&fullsendDir, "fullsend-dir", "", "path to the .fullsend directory (local manifest override + fetch cache)")
	cmd.Flags().BoolVar(&offline, "offline", false, "reject network fetches; only use a local FULLSEND_DIR measurement manifest")
	return cmd
}

type evalMeasureOpts struct {
	telemetryPath string
	registryPath  string
	outDir        string
	outputDir     string
	agent         string
	fullsendDir   string
	offline       bool
}

func runEvalMeasure(ctx context.Context, printer *ui.Printer, opts evalMeasureOpts) ([]evalmeasure.EvaluationResult, bool, error) {
	telemPaths, err := resolveEvalMeasureTelemetry(opts)
	if err != nil {
		return nil, false, err
	}
	if len(telemPaths) == 0 {
		printer.StepInfo("No platform run-telemetry.jsonl at the top of a run directory; skipping eval measurements")
		return nil, true, nil
	}

	registry, err := resolveEvalMeasureRegistry(ctx, printer, opts)
	if err != nil {
		return nil, false, err
	}
	if registry == "" {
		printer.StepInfo("No eval measurement manifest; skipping")
		return nil, true, nil
	}

	var all []evalmeasure.EvaluationResult
	for _, p := range telemPaths {
		results, stats, err := evalmeasure.MeasureAndExport(ctx, p, registry, opts.outDir, Version())
		if stats.Incomplete != "" {
			printer.StepWarn(fmt.Sprintf("%s: telemetry parse incomplete (%s); scored available traces", p, stats.Incomplete))
		}
		if stats.SkippedLines > 0 {
			printer.StepWarn(fmt.Sprintf("%s: skipped %d unreadable of %d telemetry line(s)", p, stats.SkippedLines, stats.NonEmptyLines))
		}
		if stats.SkippedSpans > 0 {
			printer.StepWarn(fmt.Sprintf("%s: skipped %d unreadable span(s) inside otherwise-valid telemetry line(s)", p, stats.SkippedSpans))
		}
		if stats.RemoteExportWarning != "" {
			printer.StepWarn(fmt.Sprintf("%s: OTLP score export failed (local JSONL kept): %s", p, stats.RemoteExportWarning))
		}
		if err != nil {
			return append(all, results...), false, err
		}
		all = append(all, results...)
	}
	return all, false, nil
}

func resolveEvalMeasureTelemetry(opts evalMeasureOpts) ([]string, error) {
	if opts.telemetryPath != "" {
		return []string{opts.telemetryPath}, nil
	}
	if opts.outputDir != "" {
		agent := ""
		if opts.agent != "" {
			a, err := sanitizeMeasurementAgentName(opts.agent)
			if err != nil {
				return nil, err
			}
			agent = a
		}
		return evalmeasure.FindPlatformTelemetry(opts.outputDir, agent)
	}
	return nil, fmt.Errorf("either --telemetry or --output-dir is required")
}

func resolveEvalMeasureRegistry(ctx context.Context, printer *ui.Printer, opts evalMeasureOpts) (string, error) {
	if opts.registryPath != "" {
		return opts.registryPath, nil
	}
	if opts.agent == "" {
		return "", fmt.Errorf("either --registry or --agent is required")
	}
	agent, err := sanitizeMeasurementAgentName(opts.agent)
	if err != nil {
		return "", err
	}
	// --fullsend-dir prefers a working-tree override. Managed CI scaffolds
	// must not pass the MR/PR checkout here (trend-poisoning); they pass
	// --registry from the default/base tip or omit both and fetch agents@v0.
	if opts.fullsendDir != "" {
		local, err := localMeasurementManifest(opts.fullsendDir, agent)
		if err != nil {
			return "", err
		}
		if st, err := os.Stat(local); err == nil && !st.IsDir() {
			printer.StepInfo("Using local measurement manifest " + local)
			return local, nil
		}
	}
	composeOpts, client := evalMeasureFetchContext(opts.fullsendDir, opts.offline, printer)
	path, ok := tryAgentsRepoMeasurementManifest(ctx, agent, client, composeOpts, printer)
	if !ok {
		return "", nil
	}
	return path, nil
}

func sanitizeMeasurementAgentName(agent string) (string, error) {
	a := strings.ToLower(strings.TrimSpace(agent))
	if a == "" || strings.ContainsAny(a, `/\`) || strings.Contains(a, "..") {
		return "", fmt.Errorf("invalid --agent name %q", agent)
	}
	return a, nil
}

func localMeasurementManifest(fullsendDir, agent string) (string, error) {
	rel := filepath.Join("eval", "measurements", agent+".yaml")
	resolved := filepath.Clean(filepath.Join(fullsendDir, rel))
	if r, err := filepath.Rel(fullsendDir, resolved); err != nil || !filepath.IsLocal(r) {
		return "", fmt.Errorf("agent name %q escapes fullsend directory", agent)
	}
	return resolved, nil
}

func evalMeasureFetchContext(fullsendDir string, offline bool, printer *ui.Printer) (harness.ComposeOpts, forge.Client) {
	workspace := fullsendDir
	if workspace == "" {
		// Prefer a per-user cache dir over shared os.TempDir() (sticky,
		// multi-user /tmp races and predictable paths). Fall back to a
		// process-private temp dir if UserCacheDir is unavailable.
		if cache, err := os.UserCacheDir(); err == nil && cache != "" {
			workspace = filepath.Join(cache, "fullsend", "eval-measure")
		} else {
			workspace = filepath.Join(os.TempDir(), fmt.Sprintf("fullsend-evalmeasure-%d", os.Getpid()))
		}
		if err := os.MkdirAll(workspace, 0o700); err != nil && printer != nil {
			printer.StepWarn("Could not create eval-measure cache dir: " + err.Error())
		}
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		abs = workspace
	}
	orgAllowlist := config.DefaultAllowedRemoteResources()
	if fullsendDir != "" && printer != nil {
		if orgCfg := tryLoadOrgConfig(filepath.Join(abs, "config.yaml"), printer); orgCfg != nil {
			orgAllowlist = orgCfg.AllowedResources()
		}
	}
	token, err := resolveToken()
	if (err != nil || token == "") && printer != nil && !offline {
		printer.StepWarn("No GH_TOKEN/GITHUB_TOKEN; agents@v0 GetRef runs unauthenticated (public repo, ~60 req/hr per IP). Prefer a token on shared runners; local FULLSEND_DIR override skips the fetch.")
	}
	policy := fetch.DefaultPolicy
	if offline {
		policy.Offline = true
	}
	return harness.ComposeOpts{
		WorkspaceRoot: abs,
		FetchPolicy:   policy,
		AuditLogPath:  filepath.Join(abs, ".fullsend-cache", "fetch-audit.jsonl"),
		OrgAllowlist:  orgAllowlist,
		GitToken:      token,
	}, gh.New(token)
}

func printMeasurementResults(printer *ui.Printer, results []evalmeasure.EvaluationResult, wroteOK bool) {
	if len(results) == 0 {
		if wroteOK {
			printer.StepDone("No new measurements (already scored or no matching traces)")
		}
		return
	}
	for _, r := range results {
		line := fmt.Sprintf("%s %s=%.2f (%s) %s", r.Version, r.Name, r.Value, r.Label, r.Explanation)
		switch r.Label {
		case evalmeasure.LabelPass:
			printer.StepDone(line)
		case evalmeasure.LabelSkip:
			printer.StepInfo(line)
		default:
			printer.StepWarn(line)
		}
	}
	if wroteOK {
		printer.StepDone(fmt.Sprintf("Wrote %d measurement(s)", len(results)))
	}
}
