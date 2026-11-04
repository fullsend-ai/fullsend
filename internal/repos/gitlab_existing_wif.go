package repos

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

// GCPServiceAccountEmailPattern matches a full Google service account
// email: a 6-30 character account ID and a project-scoped
// .iam.gserviceaccount.com domain. The character set also keeps the value
// safe inside the credential JSON the GitLab agent job writes.
var GCPServiceAccountEmailPattern = regexp.MustCompile(
	`^[a-z][a-z0-9-]{4,28}[a-z0-9]@[a-z0-9][a-z0-9.-]*[a-z0-9]\.iam\.gserviceaccount\.com$`,
)

// validateGCPServiceAccountEmail checks inference.service_account.
func validateGCPServiceAccountEmail(email string) error {
	if !GCPServiceAccountEmailPattern.MatchString(email) {
		return fmt.Errorf("inference.service_account %q is not a full service account email (expected <name>@<project>.iam.gserviceaccount.com)", email)
	}
	return nil
}

// validateWIFAudience checks inference.wif_audience: a non-empty single
// line of printable ASCII with no surrounding spaces. "$" is rejected
// because GitLab expands CI/CD variables in id_tokens audiences, so the
// token would not carry the configured value.
func validateWIFAudience(audience string) error {
	if audience == "" || strings.TrimSpace(audience) != audience {
		return fmt.Errorf("inference.wif_audience %q must be a non-empty value without surrounding spaces", audience)
	}
	for _, r := range audience {
		if r < 0x20 || r > 0x7e {
			return fmt.Errorf("inference.wif_audience %q must be a single line of printable ASCII characters", audience)
		}
		if r == '$' {
			return fmt.Errorf("inference.wif_audience %q must not contain \"$\": GitLab would expand it as a CI/CD variable", audience)
		}
	}
	return nil
}

// validateExistingWIFOptionFormats checks the format of the GitLab
// existing-provider options; empty values are not declared and pass.
func validateExistingWIFOptionFormats(audience, serviceAccount string) error {
	if audience != "" {
		if err := validateWIFAudience(audience); err != nil {
			return err
		}
	}
	if serviceAccount != "" {
		if err := validateGCPServiceAccountEmail(serviceAccount); err != nil {
			return err
		}
	}
	return nil
}

// applyExistingWIFOptions checks the format of the GitLab existing-provider
// options (inference.wif_audience, inference.service_account) wherever they
// are declared, then clears them where they do not apply. They only change
// the GitLab agent job's Vertex credentials, so on GitHub repositories (which
// can inherit them from defaults.config) and on GitLab repositories whose
// inference.auth is not vertex-wif they are inert: no variable is written and
// no audience is rendered.
func (c *ResolvedConfig) applyExistingWIFOptions() error {
	if err := validateExistingWIFOptionFormats(c.DeclaredWIFAudience, c.DeclaredServiceAccount); err != nil {
		return fmt.Errorf("%s/%s: effective configuration: %w", c.Owner, c.Repo, err)
	}
	if c.Forge != ForgeGitLab || c.InferenceAuth != InferenceAuthVertexWIF {
		c.DeclaredWIFAudience = ""
		c.DeclaredServiceAccount = ""
	}
	return nil
}

// rejectGitLabOnlyInferenceKeys refuses the GitLab existing-provider options
// in a GitHub layer (github.config or a GitHub repository's config), where
// they could never apply.
func rejectGitLabOnlyInferenceKeys(field string, o config.ManagedConfig) error {
	if !o.IsSet() {
		return nil
	}
	r := o.Writer()
	for _, key := range []struct{ name, value string }{
		{"inference.wif_audience", r.ConfigInferenceWIFAudience()},
		{"inference.service_account", r.ConfigInferenceServiceAccount()},
	} {
		if key.value != "" {
			return fmt.Errorf("%s.%s applies only to GitLab repositories; set it under gitlab.config or on a gitlab.repos entry", field, key.name)
		}
	}
	return nil
}

// renderGitLabAgentAudience sets the configured FULLSEND_ID_TOKEN audience
// in the GitLab agent template among files. An empty audience leaves the
// files unchanged, so the shipped aud "fullsend" stays byte for byte.
// files is a full template set, so a declared audience without the agent
// template is an error rather than a silently unrendered job.
func renderGitLabAgentAudience(files []forge.TreeFile, audience string) ([]forge.TreeFile, error) {
	if audience == "" {
		return files, nil
	}
	out := make([]forge.TreeFile, len(files))
	copy(out, files)
	found := false
	for i, f := range out {
		if f.Path != fullsendAgentTemplatePath {
			continue
		}
		rendered, err := scaffold.RenderGitLabAgentAudience(f.Content, audience)
		if err != nil {
			return nil, err
		}
		out[i].Content = rendered
		found = true
	}
	if !found {
		return nil, fmt.Errorf("inference.wif_audience is declared but the GitLab template set has no %s to render it into", fullsendAgentTemplatePath)
	}
	return out, nil
}

// gitlabAgentJobScriptUpstreamPath is run-agent-job.sh in
// fullsend-ai/fullsend, read at a pinned gitlab.fullsend_ref.
const gitlabAgentJobScriptUpstreamPath = "internal/scaffold/fullsend-repo-gitlab/" + gitlabAgentJobScriptPath

// requireServiceAccountScriptSupport fails when serviceAccount is declared
// but the agent job script among files never reads
// FULLSEND_GCP_SERVICE_ACCOUNT: install would write the variable and the
// job would still use direct federation. Files without the script pass.
func requireServiceAccountScriptSupport(files []forge.TreeFile, serviceAccount string) error {
	if serviceAccount == "" {
		return nil
	}
	for _, f := range files {
		if f.Path != gitlabAgentJobScriptPath {
			continue
		}
		if bytes.Contains(f.Content, []byte(forge.VarGCPServiceAccount)) {
			return nil
		}
		return fmt.Errorf("inference.service_account needs a gitlab.fullsend_ref that includes service-account impersonation support (the pinned %s does not read %s)",
			path.Base(gitlabAgentJobScriptPath), forge.VarGCPServiceAccount)
	}
	return nil
}

// checkPinnedServiceAccountSupport reads the agent job script at an explicit
// gitlab.fullsend_ref pin and applies requireServiceAccountScriptSupport, so
// install fails before any write and status reports the configuration error.
// An unpinned or release-matching ref renders the running binary's embedded
// script, which reads the variable; an established install with no ref at
// all is covered by checkEstablishedServiceAccountSupport. The fetch is
// read-only.
func checkPinnedServiceAccountSupport(ctx context.Context, resolved ResolvedConfig, upstreamRef, upstreamTag string, refResolver *RefResolver) error {
	if resolved.Forge != ForgeGitLab || resolved.DeclaredServiceAccount == "" || resolved.Vendor {
		return nil
	}
	rref := resolveTargetRef(ctx, resolved.FullsendRef, upstreamRef, upstreamTag, refResolver)
	if rref.manifestRef == "" || refResolver == nil {
		return nil
	}
	repo := resolved.Owner + "/" + resolved.Repo
	content, err := refResolver.client.GetFileContentAtRef(ctx, shimOwner, shimRepo, gitlabAgentJobScriptUpstreamPath, rref.manifestRef)
	if err != nil {
		return fmt.Errorf("%s: fetching %s at %s to verify inference.service_account support: %w", repo, gitlabAgentJobScriptPath, rref.manifestRef, err)
	}
	files := []forge.TreeFile{{Path: gitlabAgentJobScriptPath, Content: content}}
	if err := requireServiceAccountScriptSupport(files, resolved.DeclaredServiceAccount); err != nil {
		return fmt.Errorf("%s: %w", repo, err)
	}
	return nil
}

// checkEstablishedServiceAccountSupport covers an established GitLab
// installation with neither a manifest ref nor a build-time upstream ref.
// Convergence still refreshes the agent job script from the embedded
// templates, which read FULLSEND_GCP_SERVICE_ACCOUNT, in two cases, so
// neither is rejected: an unstamped install (unstampedWorkflow, the
// predicate convergeContentDriftFiles is given) and a script the probe
// reports missing (the scaffold: component convergeScaffoldFiles repairs).
// Otherwise the live script on the default branch is all there is, so a
// legacy one that never reads the variable must be rejected before the
// variable is written rather than left on direct federation. components
// is the probed component list. The read is read-only.
func checkEstablishedServiceAccountSupport(ctx context.Context, resolved ResolvedConfig, upstreamRef string, components []ComponentStatus) error {
	if resolved.Forge != ForgeGitLab || resolved.DeclaredServiceAccount == "" ||
		resolved.FullsendRef != "" || upstreamRef != "" {
		return nil
	}
	if unstampedWorkflow(resolved.Forge, components) ||
		componentMissing(components, "scaffold:"+gitlabAgentJobScriptPath) {
		return nil
	}
	repo := resolved.Owner + "/" + resolved.Repo
	content, err := resolved.ForgeConfig.Client.GetFileContent(ctx, resolved.Owner, resolved.Repo, gitlabAgentJobScriptPath)
	if err != nil && !forge.IsNotFound(err) {
		return fmt.Errorf("%s: reading %s to verify inference.service_account support: %w", repo, gitlabAgentJobScriptPath, err)
	}
	if err == nil && bytes.Contains(content, []byte(forge.VarGCPServiceAccount)) {
		return nil
	}
	return fmt.Errorf("%s: inference.service_account is not supported by the installed GitLab scaffold: its %s does not read %s and no scaffold ref is configured to refresh it; "+
		"set gitlab.fullsend_ref (or run a release build) so the scaffold can be upgraded (existing variables were left unchanged)",
		repo, gitlabAgentJobScriptPath, forge.VarGCPServiceAccount)
}

// componentMissing reports whether the probe listed the named component and
// found it absent.
func componentMissing(components []ComponentStatus, name string) bool {
	for _, c := range components {
		if c.Name == name {
			return !c.Present
		}
	}
	return false
}

// applyGitLabExistingWIFTemplates applies the existing-provider options to a
// full GitLab template set: it renders the agent job's token audience and
// checks that the set's agent job script reads a declared service account.
func applyGitLabExistingWIFTemplates(files []forge.TreeFile, audience, serviceAccount string) ([]forge.TreeFile, error) {
	files, err := renderGitLabAgentAudience(files, audience)
	if err != nil {
		return nil, err
	}
	if err := requireServiceAccountScriptSupport(files, serviceAccount); err != nil {
		return nil, err
	}
	return files, nil
}
