package suite

import (
	"testing"

	"github.com/cucumber/godog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/env"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

func TestSkipErrorForTagNames(t *testing.T) {
	w := &world.World{Config: env.RunnerConfig{InstallMode: "per-repo", SCM: "github"}}

	tests := []struct {
		name    string
		tags    []string
		wantErr error
		cfg     env.RunnerConfig
	}{
		{name: "no tags", tags: nil, wantErr: nil},
		{name: "skip per-repo on per-repo", tags: []string{"@skip:per-repo"}, wantErr: godog.ErrSkip},
		{name: "skip per-org on per-repo", tags: []string{"@skip:per-org"}, wantErr: nil},
		{name: "requires per-repo on per-repo", tags: []string{"@requires:per-repo"}, wantErr: nil},
		{name: "requires per-repo on per-org", tags: []string{"@requires:per-repo"}, wantErr: godog.ErrSkip, cfg: env.RunnerConfig{InstallMode: "per-org"}},
		{name: "skip gitlab on github", tags: []string{"@skip:gitlab"}, wantErr: nil},
		{name: "skip gitlab on gitlab", tags: []string{"@skip:gitlab"}, wantErr: godog.ErrSkip, cfg: env.RunnerConfig{SCM: "gitlab"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ww := w
			if tt.cfg.InstallMode != "" || tt.cfg.SCM != "" {
				ww = &world.World{Config: tt.cfg}
			}
			err := SkipErrorForTagNames(tt.tags, ww)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
