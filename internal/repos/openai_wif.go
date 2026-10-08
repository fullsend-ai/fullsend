package repos

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/preset"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
	"gopkg.in/yaml.v3"
)

// openAIWIFComponent is the probe component reporting whether an
// openai-wif repository has a complete set of OpenAI WIF identifiers on
// its default branch.
const openAIWIFComponent = "openai-wif:identifiers"

// openAIWIFResidualComponent is the probe component reporting OpenAI WIF
// identifiers in configuration that override an openai-api-key selection.
const openAIWIFResidualComponent = "openai-wif:residual-identifiers"

// openAIWIFWorkflowsComponent reports whether installed workflows serve WIF.
const openAIWIFWorkflowsComponent = "openai-wif:workflows"

// openAIWIFVariables lists the identifier variables in a fixed order.
var openAIWIFVariables = []string{forge.VarOpenAIAudience, forge.VarOpenAIIdentityProviderID, forge.VarOpenAIServiceAccountID}

// openAIWIFIdentifiers is the outcome of resolving the OpenAI WIF
// identifiers the way the runtime does: when any FULLSEND_OPENAI_*
// repository variable is set the three variables are the only source;
// otherwise the layered .fullsend/config.yaml (overlay → base) supplies
// them. The two sources are never mixed. Values are not kept: only the
// source and the names of missing identifiers are needed for readiness,
// and diagnostics never print identifier values.
type openAIWIFIdentifiers struct {
	// source is "Actions variables", "config.yaml", or "" when no
	// identifier is set anywhere.
	source string
	// missing names the unset identifiers within source.
	missing []string
	// unverified names the inherited-variable scopes that could not be
	// inspected. Their variables are unknown, not absent: they override
	// configuration at runtime, so a result that rests on the absence of
	// inherited variables (an empty or configuration-backed set) is not
	// verified.
	unverified []string
}

// complete reports whether a whole identifier set was verified. A complete
// set of Actions variables stands regardless of unread scopes, since every
// identifier is already set; a configuration-backed set (or none) can still
// be overridden by variables in an unread scope.
func (i openAIWIFIdentifiers) complete() bool {
	if i.source == "" || len(i.missing) > 0 {
		return false
	}
	return i.source == openAIWIFVariableSource || len(i.unverified) == 0
}

// problem returns an actionable description of why the identifiers are
// not usable, or "" when they are.
func (i openAIWIFIdentifiers) problem() string {
	switch {
	case len(i.missing) == 0 && i.source != "" && !i.complete():
		return fmt.Sprintf("cannot verify the effective OpenAI WIF identifiers: inherited variables could not be read (%s) and would override the %s identifiers; grant access to them or set the %s repository variables",
			strings.Join(i.unverified, ", "), i.source, strings.Join(openAIWIFVariables, ", "))
	case i.source == "" && len(i.unverified) > 0:
		return fmt.Sprintf("no OpenAI WIF identifiers found, and inherited variables could not be read (%s): set the %s repository variables",
			strings.Join(i.unverified, ", "), strings.Join(openAIWIFVariables, ", "))
	case i.source == "":
		return fmt.Sprintf("no OpenAI WIF identifiers: set inference.openai (audience, identity_provider_id, service_account_id) in .fullsend/config.yaml, config.base.yaml or managed config, or the %s repository or organization variables",
			strings.Join(openAIWIFVariables, ", "))
	case len(i.missing) > 0:
		return fmt.Sprintf("OpenAI WIF identifiers in %s are partially configured: missing %s",
			i.source, strings.Join(i.missing, ", "))
	}
	return ""
}

// openAIWIFVariableSource names the identifier source backed by Actions
// variables: the effective `vars` context, repository variables over the
// organization (GitHub) or group (GitLab) variables they inherit.
const openAIWIFVariableSource = "Actions variables"

// openAIWIFConfigSource names the identifier source backed by the layered
// .fullsend/config.yaml (overlay → base).
const openAIWIFConfigSource = "config.yaml"

// openAIWIFVariableIdentifiers resolves the FULLSEND_OPENAI_* variables the
// way the workflow `vars` context does: a repository variable wins over an
// inherited organization or group variable of the same name. It reports
// source "" when none is set anywhere. Inherited variables are looked up
// only when a repository variable is missing; a forge without that lookup
// contributes none. Inherited scopes the caller cannot read are reported in
// the result's unverified field rather than treated as empty.
func openAIWIFVariableIdentifiers(ctx context.Context, client forge.Client, owner, repo string) (openAIWIFIdentifiers, error) {
	var set, missing, unverified []string
	var inherited map[string]forge.OrgVariable
	inheritedLoaded := false
	for _, name := range openAIWIFVariables {
		variable, exists, err := client.GetRepoVariableInfo(ctx, owner, repo, name)
		if err != nil {
			return openAIWIFIdentifiers{}, fmt.Errorf("checking variable %s: %w", name, err)
		}
		if exists && variable.FileType {
			return openAIWIFIdentifiers{}, fmt.Errorf("OpenAI WIF variable %s is file-type; its runtime file path overrides the static API key", name)
		}
		if !exists {
			if !inheritedLoaded {
				inheritedLoaded = true
				vars, listErr := client.ListInheritedRepoVariables(ctx, owner, repo)
				// A forge without inheritance has nothing to list. Scopes the
				// caller cannot read (a GitLab project Maintainer cannot read
				// group or instance variables; a GitHub organization listing
				// can be forbidden or not found) are unverified, not empty
				// and not fatal: the variables read so far are kept and the
				// unread scopes are recorded.
				switch {
				case listErr == nil || errors.Is(listErr, forge.ErrNotSupported):
				case len(forge.UnverifiedScopes(listErr)) > 0:
					unverified = forge.UnverifiedScopes(listErr)
				case forge.IsForbidden(listErr) || forge.IsNotFound(listErr):
					unverified = []string{"inherited variables"}
				default:
					return openAIWIFIdentifiers{}, fmt.Errorf("listing inherited variables: %w", listErr)
				}
				inherited = make(map[string]forge.OrgVariable, len(vars))
				for _, v := range vars {
					inherited[v.Name] = v
				}
			}
			// GitLab exposes only whether a readable value is nonblank;
			// unknown name-only results remain conservative. GitHub returns
			// non-secret values, which are trimmed like the runtime's input.
			if v, ok := inherited[name]; ok {
				nonblank := v.Value == "" || strings.TrimSpace(v.Value) != ""
				if v.NonBlank != nil {
					nonblank = *v.NonBlank
				}
				if nonblank {
					set = append(set, name)
					continue
				}
			}
		} else if strings.TrimSpace(variable.Value) != "" {
			set = append(set, name)
			continue
		}
		missing = append(missing, name)
	}
	if len(set) == 0 {
		return openAIWIFIdentifiers{unverified: unverified}, nil
	}
	return openAIWIFIdentifiers{source: openAIWIFVariableSource, missing: missing, unverified: unverified}, nil
}

// openAIWIFConfigIdentifiers resolves inference.openai from the overlay
// and base configuration documents, each identifier independently through
// the layers like the runtime. Either document may be empty.
func openAIWIFConfigIdentifiers(overlayYAML, baseYAML []byte) (openAIWIFIdentifiers, error) {
	cfg, err := config.ParsePerRepoConfigWriterLayered(overlayYAML, baseYAML)
	if err != nil {
		// Parser type errors can quote scalar contents. Keep diagnostics useful
		// without forwarding identifier values or retaining the raw error.
		layer := preset.OverlayPath
		if strings.HasPrefix(err.Error(), "parsing base config:") {
			layer = preset.BasePath
		}
		return openAIWIFIdentifiers{}, fmt.Errorf("invalid YAML in %s while resolving inference.openai; check YAML syntax and field types", layer)
	}
	ids := cfg.ConfigInferenceOpenAI().Trimmed()
	if ids.IsZero() {
		return openAIWIFIdentifiers{}, nil
	}
	return openAIWIFIdentifiers{source: openAIWIFConfigSource, missing: ids.Missing()}, nil
}

// withUnverified returns ids marked as resting on unread inherited scopes.
func (i openAIWIFIdentifiers) withUnverified(scopes []string) openAIWIFIdentifiers {
	i.unverified = scopes
	return i
}

// readOptionalFile returns the file content on the default branch, or nil
// when it does not exist.
func readOptionalFile(ctx context.Context, client forge.Client, owner, repo, path string) ([]byte, error) {
	data, err := client.GetFileContent(ctx, owner, repo, path)
	if err != nil {
		if forge.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return data, nil
}

// liveOpenAIWIFIdentifiers resolves the identifiers active on the default
// branch: repository variables, else the committed overlay and base.
// Configuration delivered by an unmerged pull request is not live.
func liveOpenAIWIFIdentifiers(ctx context.Context, client forge.Client, owner, repo string) (openAIWIFIdentifiers, error) {
	return resolveOpenAIWIFIdentifiers(ctx, client, owner, repo, nil, nil)
}

// resolveOpenAIWIFIdentifiers resolves the identifiers a run would use.
// Repository variables win when any is set. Otherwise overlayYAML and
// baseYAML, when non-nil, stand in for the documents this run delivers
// (managed configuration, a configuration preset); a nil document is
// read from the default branch.
func resolveOpenAIWIFIdentifiers(ctx context.Context, client forge.Client, owner, repo string, overlayYAML, baseYAML []byte) (openAIWIFIdentifiers, error) {
	ids, err := openAIWIFVariableIdentifiers(ctx, client, owner, repo)
	if err != nil || ids.source != "" {
		return ids, err
	}
	if overlayYAML == nil {
		if overlayYAML, err = readOptionalFile(ctx, client, owner, repo, preset.OverlayPath); err != nil {
			return openAIWIFIdentifiers{}, err
		}
	}
	if baseYAML == nil {
		if baseYAML, err = readOptionalFile(ctx, client, owner, repo, preset.BasePath); err != nil {
			return openAIWIFIdentifiers{}, err
		}
	}
	cfgIDs, err := openAIWIFConfigIdentifiers(overlayYAML, baseYAML)
	if err != nil {
		return openAIWIFIdentifiers{}, err
	}
	// The configuration applies only when no variable is set, and that is
	// unverified while inherited scopes are unreadable.
	return cfgIDs.withUnverified(ids.unverified), nil
}

// effectiveConfigOverlay returns the .fullsend/config.yaml content that
// will be in effect after this run, for resolving the OpenAI identifiers
// the runtime would read. For a config-managed repository it applies the
// same ADR-0122 adoption and safety outcomes the delivery path enforces:
// the rendered managed overlay counts only when it will actually be
// written; when adoption is required or the safety gate rejects it, the
// existing file stays and is returned instead. A nil result means the
// default branch content is used unchanged (no overlay is delivered).
func effectiveConfigOverlay(ctx context.Context, d convergeDiscovery) ([]byte, error) {
	if !d.resolved.ConfigManaged {
		return nil, nil
	}
	existing, err := readOptionalFile(ctx, d.resolved.ForgeConfig.Client, d.repo.Owner, d.repo.Repo, preset.OverlayPath)
	if err != nil {
		return nil, err
	}
	if len(existing) > 0 && !hasManagedConfigMarker(existing) {
		return existing, nil
	}
	if bytes.Equal(existing, d.managedConfig) {
		return d.managedConfig, nil
	}
	if rejected := managedSafetyRejectedAction(ctx, d.resolved, existing); rejected != nil {
		if rejected.Action == "error" {
			return nil, fmt.Errorf("%s/%s: %s", d.repo.Owner, d.repo.Repo, rejected.Detail)
		}
		return existing, nil
	}
	return d.managedConfig, nil
}

// effectiveOpenAIWIFIdentifiers resolves the OpenAI WIF identifiers the
// runtime would read once this run's delivery outcomes are applied.
func effectiveOpenAIWIFIdentifiers(ctx context.Context, d convergeDiscovery) (openAIWIFIdentifiers, error) {
	overlay, err := effectiveConfigOverlay(ctx, d)
	if err != nil {
		return openAIWIFIdentifiers{}, err
	}
	var base []byte
	if d.resolved.Config != "" {
		base = d.preset
	}
	return resolveOpenAIWIFIdentifiers(ctx, d.resolved.ForgeConfig.Client, d.repo.Owner, d.repo.Repo, overlay, base)
}

// checkNoResidualOpenAIWIF rejects an openai-api-key selection while OpenAI
// WIF identifiers would still be active. The runtime prefers WIF over the
// static key wherever an OIDC exchange is possible, so retained identifiers
// make the key route unreachable (revoked WIF still fails; partial
// identifiers block the key fallback). The identifiers may be user-owned
// compatibility variables or configuration, so they are never deleted
// automatically; the operator must remove them. A configuration block is
// ignored by the runtime on GitLab, where there is no GitHub OIDC endpoint
// and a static key wins.
func checkNoResidualOpenAIWIF(ctx context.Context, d convergeDiscovery) error {
	ids, err := effectiveOpenAIWIFIdentifiers(ctx, d)
	if err != nil {
		return err
	}
	if ids.source == "" || (ids.source == openAIWIFConfigSource && d.resolved.Forge == ForgeGitLab) {
		return nil
	}
	remedy := "remove the " + strings.Join(openAIWIFVariables, ", ") + " repository and organization variables"
	if ids.source == openAIWIFConfigSource {
		remedy = "remove the inference.openai block from .fullsend/config.yaml and config.base.yaml"
	}
	return fmt.Errorf("%s/%s uses inference.auth %s but OpenAI WIF identifiers are still configured in %s, which the runtime prefers over the API key; %s (user-owned, not deleted automatically) and re-run",
		d.repo.Owner, d.repo.Repo, InferenceAuthOpenAIAPIKey, ids.source, remedy)
}

// unverifiedOpenAIWIFScopes returns the inherited-variable scopes that could
// not be inspected when identifiers are absent or configuration-backed.
// Visible variable identifiers are handled by the residual-identifier check.
// An openai-api-key selection is only usable once inherited identifiers are
// known absent, so its obsolete
// credentials are kept while this is non-empty.
func unverifiedOpenAIWIFScopes(ctx context.Context, client forge.Client, owner, repo string) ([]string, error) {
	ids, err := liveOpenAIWIFIdentifiers(ctx, client, owner, repo)
	if err != nil || ids.source == openAIWIFVariableSource {
		return nil, err
	}
	return ids.unverified, nil
}

// probeOpenAIWIFIdentifiers returns the readiness component for an
// openai-wif repository. Present means some identifier is configured;
// Match means a complete set is live on the default branch.
func probeOpenAIWIFIdentifiers(ctx context.Context, client forge.Client, owner, repo string) (ComponentStatus, error) {
	ids, err := liveOpenAIWIFIdentifiers(ctx, client, owner, repo)
	if err != nil {
		return ComponentStatus{}, err
	}
	cs := ComponentStatus{
		Name:     openAIWIFComponent,
		Present:  ids.source != "",
		Match:    ids.complete(),
		Expected: "complete OpenAI WIF identifiers",
		Actual:   "complete (" + ids.source + ")",
	}
	if !cs.Match {
		cs.Actual = ids.problem()
	}
	return cs, nil
}

// probeResidualOpenAIWIF reports, for an openai-api-key repository on
// GitHub, OpenAI WIF identifiers that stay active on the default branch: in
// the layered .fullsend/config.yaml, or in organization Actions variables
// the repository inherits. The runtime prefers WIF over the static key, so
// complete identifiers select WIF and partial ones block the key fallback.
// Repository-scoped variables are reported as orphans by CheckOrphanVars,
// which reads only those, so a variable source yields a component here only
// when none is repository-scoped. Values are never reported. GitLab is
// exempt: the runtime ignores the configuration block there.
func probeResidualOpenAIWIF(ctx context.Context, client forge.Client, owner, repo, forgeName string) ([]ComponentStatus, error) {
	ids, err := liveOpenAIWIFIdentifiers(ctx, client, owner, repo)
	if err != nil {
		return nil, err
	}
	if forgeName == ForgeGitLab && ids.source == openAIWIFConfigSource {
		// GitLab ignores the config block, but unread runner variables may
		// still override the static key.
		ids.source = ""
	}
	switch ids.source {
	case "":
		if len(ids.unverified) > 0 {
			return []ComponentStatus{{Name: openAIWIFResidualComponent, Present: true, Match: false,
				Expected: "verified absence of inherited OpenAI WIF identifiers",
				Actual:   "cannot verify inherited variables: " + strings.Join(ids.unverified, ", ")}}, nil
		}
		return nil, nil
	case openAIWIFVariableSource:
		for _, name := range openAIWIFVariables {
			_, exists, err := client.GetRepoVariable(ctx, owner, repo, name)
			if err != nil {
				return nil, fmt.Errorf("checking variable %s: %w", name, err)
			}
			if exists {
				return nil, nil
			}
		}
	}
	expected := "no inference.openai block in config.yaml or config.base.yaml"
	if ids.source == openAIWIFVariableSource {
		expected = "no " + strings.Join(openAIWIFVariables, ", ") + " inherited variables"
	}
	actual := "complete identifiers in " + ids.source + " select WIF over the API key"
	if !ids.complete() {
		actual = "partial identifiers in " + ids.source + " block the API key (missing " + strings.Join(ids.missing, ", ") + ")"
	}
	return []ComponentStatus{{
		Name:     openAIWIFResidualComponent,
		Present:  true,
		Match:    false,
		Expected: expected,
		Actual:   actual,
	}}, nil
}

// openAIWIFReusableWorkflows are the reusable workflows that must deliver
// the FULLSEND_OPENAI_* variables to the agent runner. A workflow that
// predates the forwarding silently drops the identifiers, so the openai-wif
// route would have no credentials once the API key is gone.
var openAIWIFReusableWorkflows = []string{
	".github/workflows/reusable-dispatch.yml",
	".github/workflows/reusable-prioritize.yml",
}

// openAIWIFGCPSecrets are the GCP secrets an openai-wif repository may not
// have (they are optional, used by Vertex sub-agents). A reusable workflow
// that declares one required cannot be called without it, so it is
// incompatible only when that secret will actually be absent.
var openAIWIFGCPSecrets = []string{forge.SecretGCPWIFProvider, forge.SecretGCPProjectID}

// gcpSecretSet names the GCP secrets that are available to workflows once
// the installation completes.
type gcpSecretSet map[string]bool

// plannedGCPSecrets returns the GCP secrets available after this run: those
// already present in the probed components, plus the pair this run writes
// when --vertex-project is supplied.
func plannedGCPSecrets(components []ComponentStatus, cfg ConvergeConfig) gcpSecretSet {
	set := gcpSecretSet{}
	for _, name := range openAIWIFGCPSecrets {
		if cfg.InferenceProject != "" || hasComponent(components, "secret:"+name) {
			set[name] = true
		}
	}
	return set
}

// liveGCPSecrets returns the GCP secrets that exist on the repository now.
func liveGCPSecrets(ctx context.Context, client forge.Client, owner, repo string) (gcpSecretSet, error) {
	set := gcpSecretSet{}
	for _, name := range openAIWIFGCPSecrets {
		exists, err := client.RepoSecretExists(ctx, owner, repo, name)
		if err != nil {
			return nil, fmt.Errorf("checking secret %s: %w", name, err)
		}
		if exists {
			set[name] = true
		}
	}
	return set, nil
}

// agentActionUses is the local action every reusable workflow uses to
// invoke an agent.
const agentActionUses = "./.defaults"

// workflowYAML models the parts of a GitHub Actions workflow the OpenAI WIF
// compatibility check inspects. Parsing the structure, rather than searching
// the text, keeps comments and unrelated references from counting.
type workflowYAML struct {
	On          yaml.Node      `yaml:"on"`
	Permissions any            `yaml:"permissions"`
	Env         map[string]any `yaml:"env"`
	Jobs        map[string]struct {
		Uses        string         `yaml:"uses"`
		Permissions any            `yaml:"permissions"`
		Env         map[string]any `yaml:"env"`
		Steps       []struct {
			If   any            `yaml:"if"`
			Uses string         `yaml:"uses"`
			Env  map[string]any `yaml:"env"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

// hasWorkflowCall accepts mapping, string, and sequence trigger declarations.
func (w workflowYAML) hasWorkflowCall() bool {
	switch w.On.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(w.On.Content); i += 2 {
			if w.On.Content[i].Value == "workflow_call" {
				return true
			}
		}
	case yaml.ScalarNode:
		return w.On.Value == "workflow_call"
	case yaml.SequenceNode:
		for _, n := range w.On.Content {
			if n.Value == "workflow_call" {
				return true
			}
		}
	}
	return false
}

// workflowCallSecretRequired reports whether the workflow's workflow_call
// trigger declares the named secret required.
func (w workflowYAML) workflowCallSecretRequired(name string) bool {
	if w.On.Kind != yaml.MappingNode {
		return false
	}
	var on struct {
		WorkflowCall struct {
			Secrets map[string]struct {
				Required any `yaml:"required"`
			} `yaml:"secrets"`
		} `yaml:"workflow_call"`
	}
	if err := w.On.Decode(&on); err != nil {
		return false
	}
	return truthy(on.WorkflowCall.Secrets[name].Required)
}

// truthy reports whether a YAML scalar is the boolean true.
func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return strings.EqualFold(strings.TrimSpace(t), "true")
	}
	return false
}

// stepDisabled reports whether a step's condition is a literal false.
func stepDisabled(cond any) bool {
	switch t := cond.(type) {
	case bool:
		return !t
	case string:
		t = strings.TrimSpace(t)
		t = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(t, "${{"), "}}"))
		return strings.EqualFold(t, "false")
	}
	return false
}

// forwardsVar reports whether an env value is exactly an expression reading
// the named Actions variable. Anything else (another variable sharing the
// prefix, a computed expression) does not deliver the value unchanged.
func forwardsVar(env map[string]any, name string) bool {
	val, ok := env[name].(string)
	if !ok {
		return false
	}
	m := varExpressionRE.FindStringSubmatch(strings.TrimSpace(val))
	return m != nil && (m[1] == name || m[2] == name)
}

// varExpressionRE matches an expression that reads exactly one Actions
// variable using property or literal index access, capturing its name.
var varExpressionRE = regexp.MustCompile(`^\$\{\{\s*vars(?:\.([A-Za-z0-9_]+)|\s*\[\s*'([A-Za-z0-9_]+)'\s*\])\s*\}\}$`)

// grantsIDTokenWrite reports whether a `permissions` value grants
// id-token: write, which a job needs to obtain an OIDC token. The write-all
// shorthand grants it; read-all, an empty mapping, and a mapping without
// id-token: write do not.
func grantsIDTokenWrite(perms any) bool {
	switch t := perms.(type) {
	case string:
		return strings.EqualFold(strings.TrimSpace(t), "write-all")
	case map[string]any:
		v, ok := t["id-token"].(string)
		return ok && strings.EqualFold(strings.TrimSpace(v), "write")
	}
	return false
}

// openAIWIFWorkflowProblem reports why a reusable workflow cannot serve an
// openai-wif repository, or "" when it can. It must not require the GCP
// secrets such a repository lacks, and every active agent invocation must
// receive all three identifiers from Actions variables, in a job that can
// obtain an OIDC token (id-token: write). A GCP secret the
// workflow declares required is acceptable when gcp says it is available.
func openAIWIFWorkflowProblem(content []byte, gcp gcpSecretSet) string {
	var wf workflowYAML
	if err := yaml.Unmarshal(content, &wf); err != nil {
		return "it is not a parseable workflow"
	}
	if !wf.hasWorkflowCall() {
		return "it does not declare a workflow_call trigger"
	}
	for _, name := range openAIWIFGCPSecrets {
		if !gcp[name] && wf.workflowCallSecretRequired(name) {
			return "it declares the " + name + " secret required and the repository will not have it"
		}
	}
	agents := 0
	for jobName, job := range wf.Jobs {
		// Job-level permissions replace the workflow-level ones entirely.
		perms := wf.Permissions
		if job.Permissions != nil {
			perms = job.Permissions
		}
		for _, step := range job.Steps {
			if strings.TrimSuffix(strings.TrimSpace(step.Uses), "/") != agentActionUses || stepDisabled(step.If) {
				continue
			}
			agents++
			if !grantsIDTokenWrite(perms) {
				return "agent job " + jobName + " does not grant id-token: write"
			}
			env := map[string]any{}
			for _, scope := range []map[string]any{wf.Env, job.Env, step.Env} {
				for name, value := range scope {
					env[name] = value
				}
			}
			for _, name := range openAIWIFVariables {
				if !forwardsVar(env, name) {
					return "an agent step does not forward " + name
				}
			}
		}
	}
	if agents == 0 {
		return "it has no agent step"
	}
	return ""
}

// openAIWIFWorkflowsForward reports whether every reusable workflow read
// through read can serve an openai-wif repository (see
// openAIWIFWorkflowProblem). A workflow that does not exist cannot.
func openAIWIFWorkflowsForward(read func(path string) ([]byte, error), gcp gcpSecretSet) (bool, error) {
	for _, path := range openAIWIFReusableWorkflows {
		ok, err := openAIWIFWorkflowServes(path, read, gcp)
		if err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

// openAIWIFWorkflowServes reports whether the reusable workflow at path, read
// through read, can serve an openai-wif repository.
func openAIWIFWorkflowServes(path string, read func(path string) ([]byte, error), gcp gcpSecretSet) (bool, error) {
	content, err := read(path)
	if err != nil {
		if forge.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("reading %s: %w", path, err)
	}
	return openAIWIFWorkflowProblem(content, gcp) == "", nil
}

// checkPinnedOpenAIWIFContract verifies that the reusable workflows a new or
// refreshed install will run forward the OpenAI WIF identifiers and do not
// require a GCP secret the repository will lack (gcp). They are read from the
// explicit manifest pin or, with none, from the release-default upstream ref
// the rendered shim calls. An established installation with neither ref has
// no scaffold refresh, so the installed workflows are all there is and
// checkEstablishedOpenAIWIFContract covers it; only a fresh install falls
// back to the default branch. The fetch is read-only and fails closed:
// without proof the workflows support the route, credentials could be
// replaced for a workflow that never sees the identifiers or cannot run
// without GCP credentials.
func checkPinnedOpenAIWIFContract(ctx context.Context, resolved ResolvedConfig, cfg ConvergeConfig, refResolver *RefResolver, gcp gcpSecretSet, established bool) error {
	if refResolver == nil {
		return nil
	}
	rref := resolveTargetRef(ctx, resolved.FullsendRef, cfg.UpstreamRef, cfg.UpstreamTag, refResolver)
	ref, what := rref.manifestRef, "pinned fullsend_ref"
	if ref == "" {
		ref, what = rref.ref, "release-default ref"
		if ref == "" {
			if established {
				return nil
			}
			ref = config.DefaultUpstreamRef
		}
	}
	ok, err := openAIWIFWorkflowsForward(func(path string) ([]byte, error) {
		return refResolver.client.GetFileContentAtRef(ctx, shimOwner, shimRepo, path, ref)
	}, gcp)
	if err != nil {
		return fmt.Errorf("fetching reusable workflows at the %s to verify %s support (existing credentials were left unchanged): %w",
			what, InferenceAuthOpenAIWIF, err)
	}
	if !ok {
		return fmt.Errorf("the %s %s does not support inference.auth %s: its reusable workflows must forward %s to the agent and not require GCP secrets the repository will not have; "+
			"supply the GCP inference credentials (--vertex-project) or upgrade fullsend_ref to a release that supports it (existing credentials were left unchanged)",
			what, ref, InferenceAuthOpenAIWIF, strings.Join(openAIWIFVariables, ", "))
	}
	// Validate the same rendered callers installation will deliver, before
	// variables or credentials can change. Missing historical thin callers
	// remain absent, matching FetchRemoteScaffold's installation behavior.
	callers, err := scaffold.CollectPerRepoInstallFiles(false, rref.ref, rref.tag)
	if rref.manifestRef != "" {
		callers, err = FetchRemoteScaffold(ctx, refResolver.client, rref.manifestRef, rref.ref, resolved.Forge, nil, nil, false)
	}
	if err != nil {
		return fmt.Errorf("fetching callers at the %s to verify %s support (existing credentials were left unchanged): %w", what, InferenceAuthOpenAIWIF, err)
	}
	for _, caller := range callers {
		if !callerGrantsIDToken(caller.Content) {
			return fmt.Errorf("the %s %s does not support inference.auth %s: caller %s must grant id-token: write (existing credentials were left unchanged)", what, ref, InferenceAuthOpenAIWIF, caller.Path)
		}
	}
	return nil
}

// checkVendoredOpenAIWIFContract verifies the reusable workflows a vendored
// install copies. They come from the resolved vendor source (read), not from
// any remote ref, so that source is what must forward the OpenAI WIF
// identifiers and not require a GCP secret the repository will lack (gcp).
// A workflow the source lacks, or one that cannot be read, fails closed.
func checkVendoredOpenAIWIFContract(read func(path string) ([]byte, error), gcp gcpSecretSet) error {
	ok, err := openAIWIFWorkflowsForward(read, gcp)
	if err != nil {
		return fmt.Errorf("reading the vendored reusable workflows to verify %s support (existing credentials were left unchanged): %w",
			InferenceAuthOpenAIWIF, err)
	}
	if !ok {
		return fmt.Errorf("the vendored reusable workflows from the resolved fullsend source do not support inference.auth %s: they must forward %s to the agent and not require GCP secrets the repository will not have; "+
			"supply the GCP inference credentials (--vertex-project) or use a --fullsend-source that supports it (existing credentials were left unchanged)",
			InferenceAuthOpenAIWIF, strings.Join(openAIWIFVariables, ", "))
	}
	return nil
}

// callerTarget is a reusable workflow an installed caller job runs, with
// whether that job is allowed an OIDC token.
type callerTarget struct {
	uses string
	// idToken is true when the calling job's effective permissions grant
	// id-token: write. A reusable workflow cannot raise the permissions its
	// caller denies, so this bounds what the callee can obtain.
	idToken bool
}

// callerUses returns the reusable workflow targets (job-level `uses`) of an
// installed caller workflow, sorted by target. ok is false when the caller
// does not parse.
func callerUses(content []byte) (targets []callerTarget, ok bool) {
	var wf workflowYAML
	if err := yaml.Unmarshal(content, &wf); err != nil {
		return nil, false
	}
	for _, job := range wf.Jobs {
		u := strings.TrimSpace(job.Uses)
		if u == "" {
			continue
		}
		// Job-level permissions replace the workflow-level ones entirely.
		perms := wf.Permissions
		if job.Permissions != nil {
			perms = job.Permissions
		}
		targets = append(targets, callerTarget{uses: u, idToken: grantsIDTokenWrite(perms)})
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].uses < targets[j].uses })
	return targets, true
}

// callerGrantsIDToken reports whether an installed caller workflow parses,
// runs at least one reusable workflow, and lets every such calling job obtain
// an OIDC token.
func callerGrantsIDToken(content []byte) bool {
	targets, ok := callerUses(content)
	if !ok || len(targets) == 0 {
		return false
	}
	for _, target := range targets {
		if !target.idToken {
			return false
		}
	}
	return true
}

// openAIWIFCallerServes reports whether every reusable workflow an installed
// caller runs can serve an openai-wif repository. Each target is resolved
// as the caller does: a local workflow from the default branch, an upstream
// workflow at the ref the caller pins. A caller with no resolvable target, or
// whose calling job does not grant id-token: write, does not serve.
func openAIWIFCallerServes(ctx context.Context, client forge.Client, owner, repo string, caller []byte, gcp gcpSecretSet) (bool, error) {
	targets, ok := callerUses(caller)
	if !ok || len(targets) == 0 {
		return false, nil
	}
	for _, target := range targets {
		// The callee cannot obtain an OIDC token its calling job denies.
		if !target.idToken {
			return false, nil
		}
		uses := target.uses
		var read func(string) ([]byte, error)
		path := ""
		switch {
		case strings.HasPrefix(uses, "./"):
			path = strings.TrimPrefix(uses, "./")
			read = func(p string) ([]byte, error) { return client.GetFileContent(ctx, owner, repo, p) }
		default:
			spec, ref, found := strings.Cut(uses, "@")
			prefix := shimOwner + "/" + shimRepo + "/"
			if !found || ref == "" || !strings.HasPrefix(spec, prefix) {
				return false, nil
			}
			path = strings.TrimPrefix(spec, prefix)
			read = func(p string) ([]byte, error) {
				return client.GetFileContentAtRef(ctx, shimOwner, shimRepo, p, ref)
			}
		}
		if !slices.Contains(openAIWIFReusableWorkflows, path) {
			return false, nil
		}
		served, err := openAIWIFWorkflowServes(path, read, gcp)
		if err != nil || !served {
			return false, err
		}
	}
	return true, nil
}

// installedOpenAIWIFWorkflowsForward reports whether the reusable workflows
// every installed credential consumer on the default branch runs can serve an
// openai-wif repository: the shim workflow and each installed per-repo thin
// caller (for example prioritize.yml), each resolved at its own `uses`
// target. A missing shim does not serve; a thin caller that is not
// installed is not a consumer. gcp names the GCP secrets available to the
// workflows.
func installedOpenAIWIFWorkflowsForward(ctx context.Context, resolved ResolvedConfig, client forge.Client, gcp gcpSecretSet) (bool, error) {
	shim, _, err := readWorkflowContent(ctx, client, resolved.Owner, resolved.Repo, resolved.ForgeConfig)
	if err != nil {
		return false, fmt.Errorf("reading %s: %w", githubOpenAIConsumerPath, err)
	}
	if shim == nil {
		return false, nil
	}
	callers := [][]byte{shim}
	for _, path := range scaffold.PerRepoThinCallerPaths() {
		content, err := client.GetFileContent(ctx, resolved.Owner, resolved.Repo, path)
		if err != nil {
			if forge.IsNotFound(err) {
				continue
			}
			return false, fmt.Errorf("reading %s: %w", path, err)
		}
		callers = append(callers, content)
	}
	for _, caller := range callers {
		ok, err := openAIWIFCallerServes(ctx, client, resolved.Owner, resolved.Repo, caller, gcp)
		if err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

// checkEstablishedOpenAIWIFContract covers an established installation for
// which no convergence refreshes scaffold content (neither a manifest ref
// nor a build-time upstream ref): the installed workflows are all there is,
// so a legacy one is rejected before credentials change.
func checkEstablishedOpenAIWIFContract(ctx context.Context, resolved ResolvedConfig, cfg ConvergeConfig, gcp gcpSecretSet) error {
	if resolved.FullsendRef != "" || cfg.UpstreamRef != "" {
		return nil
	}
	ok, err := installedOpenAIWIFWorkflowsForward(ctx, resolved, resolved.ForgeConfig.Client, gcp)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("the installed reusable workflows do not support inference.auth %s: they must forward %s to the agent without requiring GCP secrets the repository will not have, and no scaffold ref is configured to refresh them; "+
			"set fullsend_ref (or run a release build) so the scaffold can be upgraded (existing credentials were left unchanged)",
			InferenceAuthOpenAIWIF, strings.Join(openAIWIFVariables, ", "))
	}
	return nil
}

// probeOpenAIWIFWorkflows checks the live callers and their actual targets,
// rather than a manifest pin that may not yet be on the default branch.
func probeOpenAIWIFWorkflows(ctx context.Context, client forge.Client, owner, repo string, fc ForgeConfig) (ComponentStatus, error) {
	gcp, err := liveGCPSecrets(ctx, client, owner, repo)
	if err != nil {
		return ComponentStatus{}, err
	}
	resolved := ResolvedConfig{Owner: owner, Repo: repo, Forge: ForgeGitHub, ForgeConfig: fc}
	ready, err := installedOpenAIWIFWorkflowsForward(ctx, resolved, client, gcp)
	if err != nil {
		return ComponentStatus{}, err
	}
	cs := ComponentStatus{Name: openAIWIFWorkflowsComponent, Present: true, Match: ready, Expected: "installed callers and reusable workflows support OpenAI WIF", Actual: "compatible"}
	if !ready {
		cs.Actual = "installed callers or reusable workflows cannot deliver OpenAI WIF identifiers with id-token: write and the available GCP secrets"
	}
	return cs, nil
}

// checkOpenAIWIFContract chooses the actual vendor or pinned workflow source
// and verifies it before writes. The returned flag controls delivery preflight.
func checkOpenAIWIFContract(ctx context.Context, d convergeDiscovery, cfg ConvergeConfig, refResolver *RefResolver) (bool, error) {
	resolved := d.resolved
	if resolved.InferenceAuth != InferenceAuthOpenAIWIF || resolved.Forge == ForgeGitLab {
		return false, nil
	}
	gcp := plannedGCPSecrets(d.components, cfg)
	vendor := resolved.Vendor
	if cfg.VendorOverride != nil {
		vendor = *cfg.VendorOverride
	}
	vendoredSource := vendor && cfg.ReadVendoredWorkflow != nil
	if vendoredSource {
		if err := checkVendoredOpenAIWIFContract(cfg.ReadVendoredWorkflow, gcp); err != nil {
			return true, err
		}
	} else if err := checkPinnedOpenAIWIFContract(ctx, resolved, cfg, refResolver, gcp, workflowPresent(d.components)); err != nil {
		return false, err
	}
	if workflowPresent(d.components) && !vendoredSource {
		if err := checkEstablishedOpenAIWIFContract(ctx, resolved, cfg, gcp); err != nil {
			return false, err
		}
	}
	return vendoredSource, nil
}
