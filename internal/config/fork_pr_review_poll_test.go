package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// The tests in this file cover the fork_pr_review_poll block (ADR 0171):
// defaults, bounds validation, and how the block moves through config.yaml,
// config.base.yaml, repos.yaml layers and the layered reader.

func forkPollBool(v bool) *bool { return &v }
func forkPollInt(v int) *int    { return &v }

// localForkPollBlock returns the block a layer holds itself, read without
// the ForkPRReviewPoll accessor.
func localForkPollBlock(r PerRepoConfigReader) *ForkPRReviewPollConfig {
	c, ok := r.(*perRepoConfig)
	if !ok || c == nil {
		return nil
	}
	return c.ForkPRReviewPollBlock
}

func TestForkPRReviewPoll_Defaults(t *testing.T) {
	var nilBlock *ForkPRReviewPollConfig
	assert.False(t, nilBlock.PollEnabled(), "a nil block is off")
	assert.Equal(t, DefaultForkPRPollMaxStartsPerCycle, nilBlock.EffectiveMaxStartsPerCycle())
	assert.Equal(t, DefaultForkPRPollMaxAttemptsPerHead, nilBlock.EffectiveMaxAttemptsPerHead())

	empty := &ForkPRReviewPollConfig{}
	assert.False(t, empty.PollEnabled(), "an unset enabled is off")
	assert.Equal(t, 5, empty.EffectiveMaxStartsPerCycle())
	assert.Equal(t, 2, empty.EffectiveMaxAttemptsPerHead())

	set := &ForkPRReviewPollConfig{MaxStartsPerCycle: forkPollInt(12), MaxAttemptsPerHead: forkPollInt(4)}
	assert.Equal(t, 12, set.EffectiveMaxStartsPerCycle())
	assert.Equal(t, 4, set.EffectiveMaxAttemptsPerHead())

	assert.Nil(t, (&perRepoDefaults{}).ForkPRReviewPoll(), "the code defaults supply no block")
	r, err := ParsePerRepoConfig([]byte("version: \"1\"\n"))
	require.NoError(t, err)
	assert.Nil(t, r.ForkPRReviewPoll(), "no layer sets the block")
}

func TestForkPRReviewPoll_PollEnabled(t *testing.T) {
	assert.True(t, (&ForkPRReviewPollConfig{Enabled: forkPollBool(true)}).PollEnabled())
	assert.False(t, (&ForkPRReviewPollConfig{Enabled: forkPollBool(false)}).PollEnabled())
}

func TestValidateForkPRReviewPoll(t *testing.T) {
	tests := []struct {
		name    string
		block   *ForkPRReviewPollConfig
		wantErr string
	}{
		{name: "nil block", block: nil},
		{name: "empty block", block: &ForkPRReviewPollConfig{}},
		{name: "enabled only", block: &ForkPRReviewPollConfig{Enabled: forkPollBool(true)}},
		{name: "lower bounds", block: &ForkPRReviewPollConfig{MaxStartsPerCycle: forkPollInt(1), MaxAttemptsPerHead: forkPollInt(1)}},
		{name: "upper bounds", block: &ForkPRReviewPollConfig{MaxStartsPerCycle: forkPollInt(20), MaxAttemptsPerHead: forkPollInt(5)}},
		{name: "max_starts_per_cycle zero", block: &ForkPRReviewPollConfig{MaxStartsPerCycle: forkPollInt(0)},
			wantErr: "fork_pr_review_poll.max_starts_per_cycle must be between 1 and 20 (got 0)"},
		{name: "max_starts_per_cycle above", block: &ForkPRReviewPollConfig{MaxStartsPerCycle: forkPollInt(21)},
			wantErr: "fork_pr_review_poll.max_starts_per_cycle must be between 1 and 20 (got 21)"},
		{name: "max_starts_per_cycle negative", block: &ForkPRReviewPollConfig{MaxStartsPerCycle: forkPollInt(-1)},
			wantErr: "fork_pr_review_poll.max_starts_per_cycle"},
		{name: "max_attempts_per_head zero", block: &ForkPRReviewPollConfig{MaxAttemptsPerHead: forkPollInt(0)},
			wantErr: "fork_pr_review_poll.max_attempts_per_head must be between 1 and 5 (got 0)"},
		{name: "max_attempts_per_head above", block: &ForkPRReviewPollConfig{MaxAttemptsPerHead: forkPollInt(6)},
			wantErr: "fork_pr_review_poll.max_attempts_per_head must be between 1 and 5 (got 6)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateForkPRReviewPoll(tt.block)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestPerRepoConfigValidate_ForkPRReviewPoll(t *testing.T) {
	t.Run("config.yaml", func(t *testing.T) {
		good, err := ParsePerRepoConfigWriter([]byte("version: \"1\"\nfork_pr_review_poll:\n  enabled: true\n  max_starts_per_cycle: 20\n"))
		require.NoError(t, err)
		require.NoError(t, good.Validate())

		bad, err := ParsePerRepoConfigWriter([]byte("version: \"1\"\nfork_pr_review_poll:\n  max_attempts_per_head: 9\n"))
		require.NoError(t, err)
		err = bad.Validate()
		require.Error(t, err, "Validate rejects an out-of-range fork_pr_review_poll block")
		assert.Contains(t, err.Error(), "fork_pr_review_poll.max_attempts_per_head")
	})

	t.Run("repos.yaml config block", func(t *testing.T) {
		var layer ManagedConfig
		require.NoError(t, yaml.Unmarshal([]byte("fork_pr_review_poll:\n  max_starts_per_cycle: 50\n"), &layer))
		err := ValidateManagedLayer(layer.Writer())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "fork_pr_review_poll.max_starts_per_cycle")
	})
}

// TestForkPRReviewPoll_LayeredKeyByKey checks that the accessor merges each
// key through the parent chain: config.base.yaml sets enabled, the overlay
// sets max_starts_per_cycle, and both apply.
func TestForkPRReviewPoll_LayeredKeyByKey(t *testing.T) {
	base := []byte("version: \"1\"\nfork_pr_review_poll:\n  enabled: true\n  max_attempts_per_head: 3\n")
	overlay := []byte("version: \"1\"\nfork_pr_review_poll:\n  max_starts_per_cycle: 7\n  max_attempts_per_head: 1\n")

	w, err := ParsePerRepoConfigWriterLayered(overlay, base)
	require.NoError(t, err)
	got := w.ForkPRReviewPoll()
	require.NotNil(t, got)
	assert.True(t, got.PollEnabled(), "enabled falls through from the parent")
	assert.Equal(t, 7, got.EffectiveMaxStartsPerCycle(), "the child's max_starts_per_cycle applies")
	assert.Equal(t, 1, got.EffectiveMaxAttemptsPerHead(), "the child's value wins over the parent's")

	t.Run("child turns off a parent enable", func(t *testing.T) {
		w, err := ParsePerRepoConfigWriterLayered([]byte("fork_pr_review_poll:\n  enabled: false\n"), base)
		require.NoError(t, err)
		assert.False(t, w.ForkPRReviewPoll().PollEnabled())
		assert.Equal(t, 3, w.ForkPRReviewPoll().EffectiveMaxAttemptsPerHead())
	})

	t.Run("parent only", func(t *testing.T) {
		w, err := ParsePerRepoConfigWriterLayered([]byte("version: \"1\"\n"), base)
		require.NoError(t, err)
		assert.True(t, w.ForkPRReviewPoll().PollEnabled())
		assert.Nil(t, localForkPollBlock(w), "the overlay holds no block of its own")
	})

	t.Run("LayerOnBase over a managed overlay", func(t *testing.T) {
		var overlayLayer ManagedConfig
		require.NoError(t, yaml.Unmarshal([]byte("fork_pr_review_poll:\n  max_starts_per_cycle: 9\n"), &overlayLayer))
		layered, err := LayerOnBase(overlayLayer.Writer(), base)
		require.NoError(t, err)
		got := layered.ForkPRReviewPoll()
		assert.True(t, got.PollEnabled())
		assert.Equal(t, 9, got.EffectiveMaxStartsPerCycle())
	})

	t.Run("accessor result shares no state with the layers", func(t *testing.T) {
		w, err := ParsePerRepoConfigWriterLayered(overlay, base)
		require.NoError(t, err)
		got := w.ForkPRReviewPoll()
		*got.Enabled = false
		*got.MaxStartsPerCycle = 99
		again := w.ForkPRReviewPoll()
		assert.True(t, again.PollEnabled())
		assert.Equal(t, 7, again.EffectiveMaxStartsPerCycle())
	})
}

func TestPerRepoConfig_ForkPRReviewPollRoundTrip(t *testing.T) {
	want := &ForkPRReviewPollConfig{Enabled: forkPollBool(true), MaxStartsPerCycle: forkPollInt(4), MaxAttemptsPerHead: forkPollInt(2)}

	t.Run("config.yaml", func(t *testing.T) {
		w, err := ParsePerRepoConfigWriter([]byte("version: \"1\"\nfork_pr_review_poll:\n  enabled: true\n  max_starts_per_cycle: 4\n  max_attempts_per_head: 2\n"))
		require.NoError(t, err)
		data, err := w.Marshal()
		require.NoError(t, err)
		assert.Contains(t, string(data), "fork_pr_review_poll:", "Marshal writes the block")
		parsed, err := ParsePerRepoConfig(data)
		require.NoError(t, err)
		assert.Equal(t, want, localForkPollBlock(parsed), "the block survives Marshal then load")
	})

	t.Run("unset block is not written", func(t *testing.T) {
		w, err := ParsePerRepoConfigWriter([]byte("version: \"1\"\n"))
		require.NoError(t, err)
		data, err := w.Marshal()
		require.NoError(t, err)
		assert.NotContains(t, string(data), "fork_pr_review_poll")
	})

	t.Run("repos.yaml config block", func(t *testing.T) {
		var o ManagedConfig
		require.NoError(t, yaml.Unmarshal([]byte("fork_pr_review_poll:\n  enabled: true\n  max_starts_per_cycle: 4\n  max_attempts_per_head: 2\n"), &o))
		encoded, err := yaml.Marshal(o)
		require.NoError(t, err)
		var back ManagedConfig
		require.NoError(t, yaml.Unmarshal(encoded, &back))
		assert.Equal(t, want, localForkPollBlock(back.Writer()), "the block survives a repos.yaml write-back")
		assert.Equal(t, want, localForkPollBlock(o.Clone().Writer()), "Clone keeps the block")
	})
}

// TestMergeManaged_ForkPRReviewPoll checks that merging repos.yaml layers
// merges the block key by key and shares no state with either layer.
func TestMergeManaged_ForkPRReviewPoll(t *testing.T) {
	layer := func(b *ForkPRReviewPollConfig) PerRepoConfigWriter {
		return &perRepoConfig{ForkPRReviewPollBlock: b, parent: &perRepoDefaults{}}
	}
	parent := &ForkPRReviewPollConfig{Enabled: forkPollBool(true), MaxAttemptsPerHead: forkPollInt(3)}
	child := &ForkPRReviewPollConfig{MaxStartsPerCycle: forkPollInt(8)}

	got := localForkPollBlock(MergeManaged(layer(parent), layer(child)))
	assert.Equal(t, &ForkPRReviewPollConfig{
		Enabled: forkPollBool(true), MaxStartsPerCycle: forkPollInt(8), MaxAttemptsPerHead: forkPollInt(3),
	}, got)
	*got.Enabled = false
	*got.MaxAttemptsPerHead = 1
	*got.MaxStartsPerCycle = 1
	assert.True(t, *parent.Enabled, "the merged block shares no state with the parent")
	assert.Equal(t, 3, *parent.MaxAttemptsPerHead)
	assert.Equal(t, 8, *child.MaxStartsPerCycle, "the merged block shares no state with the child")

	assert.Equal(t, parent, localForkPollBlock(MergeManaged(layer(parent), layer(nil))), "a child without a block keeps the parent's")
	assert.Equal(t, child, localForkPollBlock(MergeManaged(layer(nil), layer(child))), "a child block onto a parent without one")
}

func TestCloneMergeForkPRReviewPoll_Nil(t *testing.T) {
	assert.Nil(t, cloneForkPRReviewPoll(nil))
	assert.Equal(t, &ForkPRReviewPollConfig{}, cloneForkPRReviewPoll(&ForkPRReviewPollConfig{}))
	assert.Nil(t, mergeForkPRReviewPoll(nil, nil))

	full := &ForkPRReviewPollConfig{Enabled: forkPollBool(false), MaxStartsPerCycle: forkPollInt(2), MaxAttemptsPerHead: forkPollInt(5)}
	assert.Equal(t, full, mergeForkPRReviewPoll(full, nil))
	assert.Equal(t, full, mergeForkPRReviewPoll(nil, full))
	assert.Equal(t, full, mergeForkPRReviewPoll(&ForkPRReviewPollConfig{}, full))
	assert.Equal(t, full, mergeForkPRReviewPoll(full, &ForkPRReviewPollConfig{}), "an empty child changes nothing")

	clone := cloneForkPRReviewPoll(full)
	require.Equal(t, full, clone)
	assert.NotSame(t, full.Enabled, clone.Enabled)
	assert.NotSame(t, full.MaxStartsPerCycle, clone.MaxStartsPerCycle)
	assert.NotSame(t, full.MaxAttemptsPerHead, clone.MaxAttemptsPerHead)
}
