package scaffold

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestGitLabChunkSchemasAndLiteralVariableBridges(t *testing.T) {
	for _, path := range []string{".gitlab-ci.yml", ".gitlab/ci/fullsend-pipeline.yml", ".gitlab/ci/fullsend-agent.yml"} {
		content, err := GitLabPerRepoFile(path)
		require.NoError(t, err)
		decoder := yaml.NewDecoder(bytes.NewReader(content))
		var header struct {
			Spec struct {
				Inputs map[string]struct {
					Regex   string
					Default string
				}
			}
		}
		require.NoError(t, decoder.Decode(&header))
		for i := 0; i < 9; i++ {
			name := fmt.Sprintf("event_payload_chunk_%02d", i)
			definition, exists := header.Spec.Inputs[name]
			require.True(t, exists)
			pattern, err := regexp.Compile(definition.Regex)
			require.NoError(t, err)
			require.NotEmpty(t, definition.Regex)
			for _, valid := range []string{"", "YWJj+/==", strings.Repeat("A", 1000)} {
				assert.True(t, pattern.MatchString(valid), path+" "+name)
			}
			for _, hostile := range []string{"${PROTECTED_VARIABLE}", "$(id)", "'", "a\nb", strings.Repeat("A", 1001)} {
				assert.False(t, pattern.MatchString(hostile), path+" "+name)
			}
		}
		if strings.HasSuffix(path, "fullsend-agent.yml") {
			var body map[string]any
			require.NoError(t, decoder.Decode(&body))
			job := body["fullsend $[[ inputs.stage | expand_vars ]] agent"].(map[string]any)
			variables := job["variables"].(map[string]any)
			for key, value := range variables {
				if key == "FULLSEND_FORGE" || key == "CODE_ALLOWED_TARGET_BRANCHES" {
					continue
				}
				bridge, ok := value.(map[string]any)
				require.True(t, ok, key)
				assert.Equal(t, false, bridge["expand"], key)
				assert.Contains(t, bridge["value"], "$[[ inputs.", key)
			}
		}
	}
}

func TestGitLabPollModesUseScheduleIdentity(t *testing.T) {
	content, err := GitLabPerRepoFile(".gitlab/ci/fullsend-poll.yml")
	require.NoError(t, err)
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	var header, body map[string]any
	require.NoError(t, decoder.Decode(&header))
	require.NoError(t, decoder.Decode(&body))
	job := body["$[[ inputs.schedule_name | expand_vars ]]"].(map[string]any)
	rules := job["rules"].([]any)
	for i, tc := range []struct{ description, mode string }{{"fullsend slash poll", "slash"}, {"fullsend event poll", "events"}} {
		rule := rules[i+1].(map[string]any)
		assert.Contains(t, rule["if"], "$CI_PIPELINE_SCHEDULE_DESCRIPTION == \""+tc.description+"\"")
		assert.Equal(t, tc.mode, rule["variables"].(map[string]any)["FULLSEND_POLL_MODE"])
	}
}
