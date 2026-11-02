package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// EffectiveYAML renders the effective values of a layered per-repo
// configuration (overlay → base → code defaults) as a YAML mapping, one
// top-level key per setting, resolved through the whole parent chain. It
// is a read-only view for operator guidance (for example the effective
// difference shown when adopting an existing configuration file); it is
// not a loadable configuration file.
func EffectiveYAML(r PerRepoConfigReader) ([]byte, error) {
	if r == nil {
		return nil, fmt.Errorf("configuration is required")
	}
	out := map[string]any{
		"kill_switch":  r.IsKillSwitchActive(),
		"keep_history": r.ConfigKeepHistory(),
		"roles":        r.ConfigRoles(),
	}
	for key, val := range map[string]string{
		"version":  r.ConfigVersion(),
		"forge":    r.ConfigForge(),
		"tracker":  r.ConfigTracker(),
		"runtime":  r.ConfigRuntime(),
		"mint_url": r.ConfigMintURL(),
	} {
		if val != "" {
			out[key] = val
		}
	}
	if resources := r.AllowedResources(); resources != nil {
		out["allowed_remote_resources"] = resources
	}
	if ci := r.IssueCreationConfig(); ci != nil {
		out["create_issues"] = ci
	}
	if agents := r.AgentEntries(); len(agents) > 0 {
		out["agents"] = agents
	}
	if n := r.StatusNotifications(); n != nil {
		out["status_notifications"] = n
	}
	if aliases := r.ConfigModelAliases(); len(aliases) > 0 {
		out["models"] = map[string]any{"aliases": aliases}
	}
	var authProviders []any
	if r.IsOwnersFileAuthEnabled() {
		authProviders = append(authProviders, AuthProviderOwnersFile)
	}
	if jira := r.JiraAuthorization(); jira != nil {
		// Render the lists (a set but empty list stays distinct from an
		// unset one) and the effective labels rule, so a policy change
		// shows up in the effective configuration.
		entry := *jira
		entry.Labels = jira.EffectiveJiraLabels()
		authProviders = append(authProviders, entry)
	}
	if len(authProviders) > 0 {
		out["authorization"] = authProviders
	}
	inference := map[string]any{}
	for key, val := range map[string]string{
		"provider":        r.ConfigInferenceProvider(),
		"project":         r.ConfigInferenceProject(),
		"region":          r.ConfigInferenceRegion(),
		"wif_provider":    r.ConfigInferenceWIFProvider(),
		"wif_audience":    r.ConfigInferenceWIFAudience(),
		"service_account": r.ConfigInferenceServiceAccount(),
	} {
		if val != "" {
			inference[key] = val
		}
	}
	if openai := r.ConfigInferenceOpenAI().Trimmed(); openai != (OpenAIWIFConfig{}) {
		inference["openai"] = openai
	}
	if gateway := r.ConfigInferenceGateway().Trimmed(); !gateway.IsZero() {
		inference["gateway"] = gateway
	}
	if len(inference) > 0 {
		out["inference"] = inference
	}
	return yaml.Marshal(out)
}
