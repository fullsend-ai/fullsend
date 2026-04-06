package layers

import (
	"context"
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const (
	agentWorkflowPath   = ".github/workflows/agent.yaml"
	onboardWorkflowPath = ".github/workflows/repo-onboard.yaml"
	codeownersPath      = "CODEOWNERS"
)

// managedFiles lists every file this layer manages, in write order.
// CODEOWNERS must be last — its failure is non-fatal.
var managedFiles = []string{agentWorkflowPath, onboardWorkflowPath, codeownersPath}

// WorkflowsLayer manages workflow files and CODEOWNERS in the .fullsend
// config repo. It writes the reusable agent dispatch workflow, the repo
// onboarding workflow, and a CODEOWNERS file that grants the installing
// user ownership of all config-repo contents.
type WorkflowsLayer struct {
	org               string
	client            forge.Client
	ui                *ui.Printer
	authenticatedUser string
}

// Compile-time check that WorkflowsLayer implements Layer.
var _ Layer = (*WorkflowsLayer)(nil)

// NewWorkflowsLayer creates a new WorkflowsLayer.
// user is the authenticated user who will own CODEOWNERS entries.
func NewWorkflowsLayer(org string, client forge.Client, printer *ui.Printer, user string) *WorkflowsLayer {
	return &WorkflowsLayer{
		org:               org,
		client:            client,
		ui:                printer,
		authenticatedUser: user,
	}
}

func (l *WorkflowsLayer) Name() string {
	return "workflows"
}

// Install writes the workflow files and CODEOWNERS to the .fullsend repo.
// CODEOWNERS failure is treated as a warning, not a fatal error.
func (l *WorkflowsLayer) Install(ctx context.Context) error {
	files := map[string][]byte{
		agentWorkflowPath:   []byte(agentWorkflowContent),
		onboardWorkflowPath: []byte(onboardWorkflowContent),
		codeownersPath:      []byte(l.codeownersContent()),
	}

	for _, path := range managedFiles {
		content := files[path]
		l.ui.StepStart("Writing " + path)

		err := l.client.CreateOrUpdateFile(ctx, l.org, forge.ConfigRepoName, path, "chore: update "+path, content)
		if err != nil {
			if path == codeownersPath {
				l.ui.StepWarn("Could not write " + path + ": " + err.Error())
				continue
			}
			l.ui.StepFail("Failed to write " + path)
			return fmt.Errorf("writing %s: %w", path, err)
		}
		l.ui.StepDone("Wrote " + path)
	}

	return nil
}

// Uninstall is a no-op. Workflow files are removed when the config repo
// is deleted by the ConfigRepoLayer.
func (l *WorkflowsLayer) Uninstall(_ context.Context) error {
	return nil
}

// Analyze checks which managed files exist in the config repo.
func (l *WorkflowsLayer) Analyze(ctx context.Context) (*LayerReport, error) {
	report := &LayerReport{Name: l.Name()}

	var present, missing []string
	for _, path := range managedFiles {
		_, err := l.client.GetFileContent(ctx, l.org, forge.ConfigRepoName, path)
		if err != nil {
			if forge.IsNotFound(err) {
				missing = append(missing, path)
				continue
			}
			return nil, fmt.Errorf("checking %s: %w", path, err)
		}
		present = append(present, path)
	}

	switch {
	case len(missing) == 0:
		report.Status = StatusInstalled
		for _, p := range present {
			report.Details = append(report.Details, p+" exists")
		}
	case len(present) == 0:
		report.Status = StatusNotInstalled
		for _, m := range missing {
			report.WouldInstall = append(report.WouldInstall, "write "+m)
		}
	default:
		report.Status = StatusDegraded
		for _, p := range present {
			report.Details = append(report.Details, p+" exists")
		}
		for _, m := range missing {
			report.WouldFix = append(report.WouldFix, "write "+m)
		}
	}

	return report, nil
}

func (l *WorkflowsLayer) codeownersContent() string {
	return fmt.Sprintf("# fullsend configuration is governed by org admins.\n* @%s\n", l.authenticatedUser)
}

const agentWorkflowContent = `# Reusable agent dispatch workflow
# Called by per-repo shim workflows to run fullsend agents.
name: Agent Dispatch

on:
  workflow_call:
    inputs:
      event_type:
        required: true
        type: string
      event_payload:
        required: true
        type: string
    secrets:
      APP_PRIVATE_KEY:
        required: true

jobs:
  dispatch:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Run fullsend entrypoint
        run: echo "fullsend entrypoint - event=${{ inputs.event_type }}"
        env:
          EVENT_TYPE: ${{ inputs.event_type }}
          EVENT_PAYLOAD: ${{ inputs.event_payload }}
`

const onboardWorkflowContent = `# Repo onboarding workflow
# Creates enrollment PRs for repos listed in config.yaml.
name: Repo Onboard

on:
  push:
    branches: [main]
    paths: [config.yaml]

permissions:
  contents: write
  pull-requests: write

jobs:
  onboard:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Read enabled repos
        id: repos
        run: |
          repos=$(yq '.repos | to_entries | map(select(.value.enabled == true)) | .[].key' config.yaml)
          echo "repos<<EOF" >> "$GITHUB_OUTPUT"
          echo "$repos" >> "$GITHUB_OUTPUT"
          echo "EOF" >> "$GITHUB_OUTPUT"
      - name: Create enrollment PRs
        run: echo "Would create enrollment PRs for enabled repos"
`
