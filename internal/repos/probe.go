package repos

import (
	"context"
	"fmt"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

// ComponentStatus describes the state of a single installation component
// as probed from the forge. Both install and status use this to answer
// "what's wrong with this repo?" — install acts on the answer, status
// reports it.
type ComponentStatus struct {
	// Name identifies the component, prefixed by category:
	//   "workflow", "thin-caller:<path>", "var:<name>", "secret:<name>"
	Name string

	// Present is true when the component exists on the forge.
	Present bool

	// Expected is the manifest's desired value. Empty when value
	// checking does not apply (e.g., secrets whose values cannot be
	// read back, or when no expected value was provided).
	Expected string

	// Actual is the component's current value on the forge. Empty
	// when the component is not present or its value is opaque.
	Actual string

	// Match is true when the component is present and either no value
	// check applies or the actual value equals the expected value.
	Match bool
}

// AllMatch returns true when every ComponentStatus has Match == true.
func AllMatch(components []ComponentStatus) bool {
	for _, c := range components {
		if !c.Match {
			return false
		}
	}
	return true
}

// DriftFieldName returns the component name without its category prefix,
// suitable for use as a Drift.Field value. "var:FULLSEND_MINT_URL"
// becomes "FULLSEND_MINT_URL"; "workflow" stays "workflow".
func DriftFieldName(componentName string) string {
	if _, after, ok := strings.Cut(componentName, ":"); ok {
		return after
	}
	return componentName
}

// ProbeComponents checks all per-repo installation components and
// returns their status.
//
// Components checked:
//   - Shim workflow file (presence)
//   - Per-repo thin callers (presence, GitHub only)
//   - Required variables (presence; values compared when expectedVarValues
//     contains a non-empty entry for the variable name)
//   - Required secrets (presence only — values cannot be read back)
//
// expectedVarValues maps variable names to their expected values for
// value-level drift detection. Pass nil for presence-only checking.
func ProbeComponents(ctx context.Context, client forge.Client, owner, repo, forgeName string, fc ForgeConfig, expectedVarValues map[string]string) ([]ComponentStatus, error) {
	var results []ComponentStatus

	// Workflow file (try forge-appropriate extensions).
	workflowPresent := false
	var workflowRef string
	for _, path := range fc.WorkflowPaths {
		content, err := client.GetFileContent(ctx, owner, repo, path)
		if err != nil {
			if forge.IsNotFound(err) {
				continue
			}
			return nil, fmt.Errorf("checking workflow file: %w", err)
		}
		workflowPresent = true
		workflowRef = extractWorkflowRef(content, fc)
		break
	}
	results = append(results, ComponentStatus{
		Name:    "workflow",
		Present: workflowPresent,
		Actual:  workflowRef,
		Match:   workflowPresent,
	})

	// Per-repo thin callers (GitHub only).
	if forgeName == ForgeGitHub || forgeName == "" {
		for _, tcPath := range scaffold.PerRepoThinCallerPaths() {
			_, tcErr := client.GetFileContent(ctx, owner, repo, tcPath)
			if tcErr != nil {
				if forge.IsNotFound(tcErr) {
					results = append(results, ComponentStatus{
						Name:    "thin-caller:" + tcPath,
						Present: false,
						Match:   false,
					})
					continue
				}
				return nil, fmt.Errorf("checking thin caller %s: %w", tcPath, tcErr)
			}
			results = append(results, ComponentStatus{
				Name:    "thin-caller:" + tcPath,
				Present: true,
				Match:   true,
			})
		}
	}

	// Required variables (forge-specific list).
	for _, varName := range requiredVarsForForge(forgeName) {
		val, exists, err := client.GetRepoVariable(ctx, owner, repo, varName)
		if err != nil {
			return nil, fmt.Errorf("checking variable %s: %w", varName, err)
		}
		cs := ComponentStatus{
			Name:    "var:" + varName,
			Present: exists,
			Actual:  val,
			Match:   exists,
		}
		if expectedVarValues != nil {
			if expected, hasExpected := expectedVarValues[varName]; hasExpected && expected != "" {
				cs.Expected = expected
				cs.Match = exists && val == expected
			}
		}
		results = append(results, cs)
	}

	// Required secrets (existence check only — values cannot be read back).
	for _, secretName := range requiredSecretsForForge() {
		exists, err := client.RepoSecretExists(ctx, owner, repo, secretName)
		if err != nil {
			return nil, fmt.Errorf("checking secret %s: %w", secretName, err)
		}
		results = append(results, ComponentStatus{
			Name:    "secret:" + secretName,
			Present: exists,
			Match:   exists,
		})
	}

	return results, nil
}
