package repos

import (
	"context"
	"fmt"
	"sort"
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
	//   "workflow", "thin-caller:<path>", "scaffold:<path>", "var:<name>",
	//   "secret:<name>", "schedule:<name>", "openai-wif:<name>"
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

	// Match is true when the component meets its requirement. When no
	// inference.auth is selected, both GCP secrets absent is also a match
	// because Vertex is optional (see ProbeComponentsForAuth).
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
//   - GitLab trust script (presence)
//   - Per-repo thin callers (presence, GitHub only)
//   - Required variables (presence; values compared when expectedVarValues
//     contains a non-empty entry for the variable name)
//   - Required secrets (presence only — values cannot be read back)
//
// expectedVarValues maps variable names to their expected values for
// value-level drift detection. Pass nil for presence-only checking.
func ProbeComponents(ctx context.Context, client forge.Client, owner, repo, forgeName string, fc ForgeConfig, expectedVarValues map[string]string) ([]ComponentStatus, error) {
	return ProbeComponentsForAuth(ctx, client, owner, repo, forgeName, "", fc, expectedVarValues)
}

// ProbeComponentsForAuth is ProbeComponents for a repository whose
// effective inference.auth is known. The secrets probed are the ones
// that carry the selected method's credentials (inferenceSecretsForAuth),
// and each must be present to match. An empty auth keeps the legacy
// behavior: the GCP pair is probed and both absent counts as a match.
// openai-wif probes the GCP pair the same optional way and adds an
// openai-wif:identifiers component that matches only when a complete set
// of OpenAI WIF identifiers is live on the default branch. Installed repos
// also report openai-wif:workflows for their live workflow contract.
func ProbeComponentsForAuth(ctx context.Context, client forge.Client, owner, repo, forgeName, auth string, fc ForgeConfig, expectedVarValues map[string]string) ([]ComponentStatus, error) {
	var results []ComponentStatus

	// Workflow presence is the current carrier (WorkflowPaths). GitLab
	// still reads a leftover dispatch stub for the version ref so repos
	// enrolled before #7707 keep reporting the installed version until
	// converge writes the marker onto the pipeline wrapper and deletes
	// the stub. Presence stays false when only the stub exists, so the
	// missing wrapper is repaired.
	content, _, carrierPresent, err := readWorkflowMarker(ctx, client, owner, repo, fc)
	if err != nil {
		return nil, fmt.Errorf("checking workflow file: %w", err)
	}
	var workflowRef string
	if content != nil {
		workflowRef = extractWorkflowRef(content, fc)
	}
	workflowPresent := carrierPresent
	results = append(results, ComponentStatus{
		Name:    "workflow",
		Present: workflowPresent,
		Actual:  workflowRef,
		Match:   workflowPresent,
	})

	// Auxiliary GitLab scripts are sourced by the poll and agent templates
	// but are not themselves the workflow component. Probe them separately
	// so status and converge can detect and repair installs missing only
	// these files.
	if forgeName == ForgeGitLab {
		scriptPaths := gitlabAuxiliaryScriptPaths()
		// The webhook dispatcher template and script exist only in
		// scaffold versions whose pipeline wrapper includes them, so a
		// pre-dispatcher install must not report them missing. An
		// unchanged-ref rollout that has not yet rewritten the wrapper is
		// covered by CheckFileContentDrift, which compares against the
		// selected version's expected files.
		if gitlabWrapperReferencesDispatcher(content) {
			scriptPaths = append(scriptPaths, gitlabDispatcherPaths()...)
		}
		for _, path := range scriptPaths {
			_, err := client.GetFileContent(ctx, owner, repo, path)
			if err != nil && !forge.IsNotFound(err) {
				return nil, fmt.Errorf("checking GitLab scaffold file %s: %w", path, err)
			}
			present := err == nil
			results = append(results, ComponentStatus{
				Name:    "scaffold:" + path,
				Present: present,
				Match:   present,
			})
		}
	}

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
	probedVars := make(map[string]bool)
	for _, varName := range requiredVarsForForge(forgeName) {
		probedVars[varName] = true
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

	// Additional static variables from expectedVarValues that are not
	// in the required list. These are optional install-time variables
	// (e.g., FULLSEND_GCP_REGION, FULLSEND_REVIEW_CLIENT_ID) that
	// should be value-checked when expected values are provided.
	// Sort keys for deterministic ordering of ComponentStatus entries.
	var extraVarNames []string
	for varName := range expectedVarValues {
		if !probedVars[varName] && expectedVarValues[varName] != "" {
			extraVarNames = append(extraVarNames, varName)
		}
	}
	sort.Strings(extraVarNames)
	for _, varName := range extraVarNames {
		expected := expectedVarValues[varName]
		val, exists, err := client.GetRepoVariable(ctx, owner, repo, varName)
		if err != nil {
			return nil, fmt.Errorf("checking variable %s: %w", varName, err)
		}
		// Only report drift when the variable is present — these
		// are optional, so absence is not a problem (the repo may
		// have been installed without the corresponding flag).
		if exists {
			results = append(results, ComponentStatus{
				Name:     "var:" + varName,
				Present:  true,
				Expected: expected,
				Actual:   val,
				Match:    val == expected,
			})
		}
	}

	// Pipeline schedules (GitLab only — GitHub uses webhook dispatch).
	if forgeName == ForgeGitLab {
		schedules, schedErr := client.ListPipelineSchedules(ctx, owner, repo)
		if schedErr != nil {
			return nil, fmt.Errorf("checking pipeline schedules: %w", schedErr)
		}
		for _, spec := range pipelineScheduleSpecs {
			found := false
			active := false
			for _, s := range schedules {
				if s.Description == spec.Description {
					found = true
					if s.Active {
						active = true
						break
					}
				}
			}
			actual := ""
			if found {
				if active {
					actual = "active"
				} else {
					actual = "inactive"
				}
			}
			results = append(results, ComponentStatus{
				Name:     spec.ComponentName,
				Present:  found,
				Expected: "active",
				Actual:   actual,
				Match:    found && active,
			})
		}
	}

	// Required secrets (existence check only — values cannot be read back).
	probedSecrets := requiredSecretsForForge(forgeName)
	if auth != "" {
		probedSecrets = inferenceSecretsForAuth(auth)
	}
	for _, secretName := range probedSecrets {
		if auth == InferenceAuthOpenAIAPIKey && secretName == forge.SecretOpenAIAPIKey {
			// Existence alone is not readiness: jobs only get a masked,
			// protected, wildcard-scoped environment variable. Present
			// keeps meaning "exists"; Match reports usability.
			prot, err := client.GetRepoSecretProtection(ctx, owner, repo, secretName)
			if err != nil {
				return nil, fmt.Errorf("checking secret %s: %w", secretName, err)
			}
			cs := ComponentStatus{
				Name:    "secret:" + secretName,
				Present: prot.Exists,
				Match:   prot.Exists,
			}
			if defect := openAIKeyDefect(prot); defect != "" {
				cs.Expected = "masked, protected environment variable"
				cs.Actual = defect
				cs.Match = false
			}
			results = append(results, cs)
			continue
		}
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
	if auth == InferenceAuthOpenAIWIF {
		// Readiness is a complete identifier set on the default branch,
		// not a secret. The probed GCP pair stays optional below so
		// Vertex sub-agents of an OpenAI parent keep working.
		ids, err := probeOpenAIWIFIdentifiers(ctx, client, owner, repo)
		if err != nil {
			return nil, err
		}
		results = append(results, ids)
		if workflowPresent {
			contract, err := probeOpenAIWIFWorkflows(ctx, client, owner, repo, fc)
			if err != nil {
				return nil, err
			}
			results = append(results, contract)
		}
	} else if auth == InferenceAuthOpenAIAPIKey && anyComponentPresent(results) {
		// Check inherited identifiers and unread scopes on both forges.
		// Layered configuration overrides the key only on GitHub; the
		// residual probe ignores that source on GitLab.
		conflict, err := probeResidualOpenAIWIF(ctx, client, owner, repo, forgeName)
		if err != nil {
			return nil, err
		}
		results = append(results, conflict...)
		return results, nil
	} else if auth != "" {
		// The selected method's credentials are required.
		return results, nil
	}
	// Without a selected inference.auth (or with openai-wif), Vertex is
	// optional. Keep both GCP components visible so a partial pair remains
	// detectable and convergence can repair it when values are supplied.
	var projectIndex, wifIndex = -1, -1
	for i := range results {
		switch results[i].Name {
		case "secret:" + forge.SecretGCPProjectID:
			projectIndex = i
		case "secret:" + forge.SecretGCPWIFProvider:
			wifIndex = i
		}
	}
	if projectIndex >= 0 && wifIndex >= 0 && !results[projectIndex].Present && !results[wifIndex].Present {
		results[projectIndex].Match = true
		results[wifIndex].Match = true
	}

	return results, nil
}
