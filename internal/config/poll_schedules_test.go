package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestValidatePollSchedules(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *PollSchedulesConfig
		wantErr string
	}{
		{name: "nil", cfg: nil},
		{name: "empty", cfg: &PollSchedulesConfig{}},
		{name: "both set", cfg: &PollSchedulesConfig{SlashPoll: "*/30 * * * *", EventPoll: "7 */2 * * MON-FRI"}},
		{name: "extra whitespace", cfg: &PollSchedulesConfig{SlashPoll: " 0  *  * * * "}},
		{name: "too few fields", cfg: &PollSchedulesConfig{SlashPoll: "*/30 * * *"}, wantErr: "poll_schedules.slash_poll"},
		{name: "too many fields", cfg: &PollSchedulesConfig{EventPoll: "0 0 * * * *"}, wantErr: "poll_schedules.event_poll"},
		{name: "macro", cfg: &PollSchedulesConfig{SlashPoll: "@hourly"}, wantErr: "five fields"},
		{name: "bad character", cfg: &PollSchedulesConfig{EventPoll: "0 * * * $(id)"}, wantErr: "is not a cron field"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePollSchedules(tt.cfg)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestNormalizeCron(t *testing.T) {
	assert.Equal(t, "0 * * * *", NormalizeCron("  0   * *\t* * "))
}

func TestConfigPollSchedules_LayersPerKey(t *testing.T) {
	overlay, err := ParsePerRepoConfigWriter([]byte("poll_schedules:\n  slash_poll: \"*/30 * * * *\"\n"))
	require.NoError(t, err)
	layered, err := LayerOnBase(overlay, []byte("poll_schedules:\n  slash_poll: \"0 * * * *\"\n  event_poll: \"5 * * * *\"\n"))
	require.NoError(t, err)

	got := layered.ConfigPollSchedules()
	assert.Equal(t, "*/30 * * * *", got.SlashPoll, "the overlay overrides the base")
	assert.Equal(t, "5 * * * *", got.EventPoll, "an unset overlay key inherits the base")

	unset, err := ParsePerRepoConfig([]byte("version: \"1\"\n"))
	require.NoError(t, err)
	none := unset.ConfigPollSchedules()
	assert.True(t, none.IsZero(), "no layer sets it: compiled-in crons")
}

func TestPollSchedules_ManagedLayerMergeAndMarshal(t *testing.T) {
	var parent, child ManagedConfig
	require.NoError(t, yaml.Unmarshal([]byte("poll_schedules:\n  slash_poll: \"0 * * * *\"\n  event_poll: \"5 * * * *\"\n"), &parent))
	require.NoError(t, yaml.Unmarshal([]byte("poll_schedules:\n  event_poll: \"15 */2 * * *\"\n"), &child))

	merged := MergeManaged(parent.Writer(), child.Writer())
	got := merged.ConfigPollSchedules()
	assert.Equal(t, PollSchedulesConfig{SlashPoll: "0 * * * *", EventPoll: "15 */2 * * *"}, got)
	require.NoError(t, ValidateManagedLayer(merged))

	data, err := merged.Marshal()
	require.NoError(t, err)
	assert.Contains(t, string(data), "poll_schedules:")
	reparsed, err := ParsePerRepoConfig(data)
	require.NoError(t, err)
	assert.Equal(t, got, reparsed.ConfigPollSchedules(), "the block survives a round trip")

	// The parent's block is not changed by the merge.
	assert.Equal(t, "5 * * * *", parent.Writer().ConfigPollSchedules().EventPoll)
}

func TestPollSchedules_MarshalOmitsEmptyBlock(t *testing.T) {
	cfg, err := ParsePerRepoConfigWriter([]byte("version: \"1\"\npoll_schedules: {}\n"))
	require.NoError(t, err)
	data, err := cfg.Marshal()
	require.NoError(t, err)
	assert.NotContains(t, string(data), "poll_schedules")
}

func TestPollSchedules_ManagedRejectsUnknownKeyAndInvalidCron(t *testing.T) {
	var o ManagedConfig
	err := yaml.Unmarshal([]byte("poll_schedules:\n  jira_poll: \"0 * * * *\"\n"), &o)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "jira_poll")

	require.NoError(t, yaml.Unmarshal([]byte("poll_schedules:\n  slash_poll: \"every hour\"\n"), &o))
	err = ValidateManagedLayer(o.Writer())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "poll_schedules.slash_poll")
}

func TestEffectiveYAML_PollSchedules(t *testing.T) {
	cfg, err := ParsePerRepoConfig([]byte("version: \"1\"\npoll_schedules:\n  event_poll: \"7 * * * *\"\n"))
	require.NoError(t, err)
	out, err := EffectiveYAML(cfg)
	require.NoError(t, err)
	assert.Contains(t, string(out), "poll_schedules:")
	assert.Contains(t, string(out), "event_poll: 7 * * * *")

	plain, err := ParsePerRepoConfig([]byte("version: \"1\"\n"))
	require.NoError(t, err)
	out, err = EffectiveYAML(plain)
	require.NoError(t, err)
	assert.False(t, strings.Contains(string(out), "poll_schedules"), "an unset block leaves the view unchanged")
}
