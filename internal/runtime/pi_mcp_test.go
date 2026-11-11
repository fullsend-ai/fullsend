package runtime

import (
	"encoding/json"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildPiRunCommand_MCPExclusions(t *testing.T) {
	t.Setenv("FULLSEND_PI_MODEL", "")
	for _, tc := range []struct {
		name  string
		tools []string
		want  string
	}{
		{"default", nil, ""},
		{"extensions-only", []string{}, ""},
		{"non-MCP", []string{"codemode"}, "mcp__*,list_mcp_resources,list_mcp_resource_templates,read_mcp_resource"},
		{"selected resource", []string{"codemode", "read_mcp_resource"}, "mcp__*,list_mcp_resources,list_mcp_resource_templates"},
		{"resource pattern", []string{"list_mcp_*"}, "mcp__*,read_mcp_resource"},
		{"all resources", []string{"list_mcp_resources", "list_mcp_resource_templates", "read_mcp_resource"}, "mcp__*"},
		{"MCP exact", []string{"codemode", "mcp__docs__search"}, ""},
		{"MCP pattern", []string{"mcp__docs__*"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, piMCPExclusions(tc.tools))
			cmd := buildPiRunCommand(piTestParams(), &piManifest{Tools: tc.tools}, nil, "")
			if tc.want == "" {
				assert.NotContains(t, cmd, "--exclude-tools")
			} else {
				assert.Contains(t, cmd, "--exclude-tools "+shellQuote(tc.want))
			}
			// Parent flags must match the child flags exercised by the
			// offline real-pi nested-execution regression.
			if _, err := exec.LookPath("node"); err != nil {
				t.Skip("node not installed")
			}
			encoded, err := json.Marshal(tc.tools)
			require.NoError(t, err)
			script := `import {childArgs} from "./pi_extension/fullsend-agent.js";
const tools = JSON.parse(process.argv[1]) ?? [];
const args = childArgs({mcpResourceTools: JSON.parse(process.argv[2])}, {seq: 1, modelSpec: "m", tools});
const i = args.indexOf("--exclude-tools");
process.stdout.write(i < 0 ? "" : args[i+1]);`
			out, err := exec.Command("node", "--input-type=module", "-e", script, string(encoded), string(piMCPResourceJSON)).CombinedOutput()
			require.NoError(t, err, string(out))
			assert.Equal(t, tc.want, string(out))
		})
	}
}
