package scaffold

import (
	"bytes"
	"fmt"
)

// GitLabAgentTemplatePath is the GitLab agent job template whose id_tokens
// block requests the token used for Vertex workload identity federation.
const GitLabAgentTemplatePath = ".gitlab/ci/fullsend-agent.yml"

// DefaultGitLabIDTokenAudience is the audience the shipped GitLab agent job
// requests for FULLSEND_ID_TOKEN. Installs whose WIF provider is the guide's
// own gitlab-oidc provider keep it.
const DefaultGitLabIDTokenAudience = "fullsend"

// gitlabDefaultAudienceLine is the exact id_tokens audience line of the
// shipped agent template. It is a literal rather than a placeholder so that
// any path that writes the template without rendering it keeps the default
// audience byte for byte.
var gitlabDefaultAudienceLine = []byte("\n      aud: \"" + DefaultGitLabIDTokenAudience + "\"\n")

// RenderGitLabAgentAudience returns the agent template content with its
// FULLSEND_ID_TOKEN audience set to audience. An empty audience, or the
// default one, returns content unchanged. The caller validates audience
// (a single printable line); it is written as a double-quoted YAML scalar.
// The template must contain the shipped audience line exactly once, so a
// template that has drifted from it fails instead of silently keeping the
// default audience.
func RenderGitLabAgentAudience(content []byte, audience string) ([]byte, error) {
	if audience == "" || audience == DefaultGitLabIDTokenAudience {
		return content, nil
	}
	if n := bytes.Count(content, gitlabDefaultAudienceLine); n != 1 {
		return nil, fmt.Errorf("%s: expected exactly one id_tokens line aud: %q to render the configured audience, found %d", GitLabAgentTemplatePath, DefaultGitLabIDTokenAudience, n)
	}
	rendered := fmt.Appendf(nil, "\n      aud: %q\n", audience)
	return bytes.Replace(content, gitlabDefaultAudienceLine, rendered, 1), nil
}
