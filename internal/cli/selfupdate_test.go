package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/selfupdate"
)

// fakeSelfUpdater records the options it is run with and returns a fixed
// result. The update itself is tested in internal/selfupdate.
type fakeSelfUpdater struct {
	res  selfupdate.Result
	err  error
	ctx  context.Context
	opts selfupdate.Options
	runs int
}

func (f *fakeSelfUpdater) Run(ctx context.Context, opts selfupdate.Options) (selfupdate.Result, error) {
	f.ctx, f.opts = ctx, opts
	f.runs++
	return f.res, f.err
}

// stubSelfUpdater makes the self-update command run fake, with the running
// binary reporting current as its version.
func stubSelfUpdater(t *testing.T, current string, fake *fakeSelfUpdater) {
	t.Helper()
	origNew, origVersion := newSelfUpdaterFn, version
	t.Cleanup(func() { newSelfUpdaterFn, version = origNew, origVersion })
	version = current
	newSelfUpdaterFn = func() selfUpdater { return fake }
}

func runSelfUpdateCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append([]string{"self-update"}, args...))
	err := cmd.Execute()
	return out.String(), err
}

func TestSelfUpdateCmd_Output(t *testing.T) {
	tests := []struct {
		name string
		res  selfupdate.Result
		want string
	}{
		{
			name: "up to date",
			res:  selfupdate.Result{Status: selfupdate.StatusUpToDate, From: "v0.53.0", To: "v0.53.0", Path: "/bin/fullsend"},
			want: "fullsend v0.53.0 is already installed",
		},
		{
			name: "ahead",
			res:  selfupdate.Result{Status: selfupdate.StatusAhead, From: "v0.54.0-rc.1", To: "v0.53.0", Path: "/bin/fullsend"},
			want: "fullsend v0.54.0-rc.1 is newer than the latest release (v0.53.0); nothing to do",
		},
		{
			name: "dry run",
			res:  selfupdate.Result{Status: selfupdate.StatusWouldUpdate, From: "v0.52.0", To: "v0.53.0", Path: "/bin/fullsend"},
			want: "Would update fullsend v0.52.0 → v0.53.0 (/bin/fullsend)",
		},
		{
			name: "updated",
			res:  selfupdate.Result{Status: selfupdate.StatusUpdated, From: "v0.52.0", To: "v0.53.0", Path: "/bin/fullsend"},
			want: "Updated fullsend v0.52.0 → v0.53.0 (/bin/fullsend)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubSelfUpdater(t, "0.52.0", &fakeSelfUpdater{res: tt.res})
			out, err := runSelfUpdateCmd(t)
			require.NoError(t, err)
			assert.Contains(t, out, tt.want)
		})
	}
}

func TestSelfUpdateCmd_PassesOptions(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want selfupdate.Options
	}{
		{"defaults", nil, selfupdate.Options{CurrentVersion: "0.52.0"}},
		{"version", []string{"--version", "v0.51.0"}, selfupdate.Options{CurrentVersion: "0.52.0", TargetVersion: "v0.51.0"}},
		{"dry run", []string{"--dry-run"}, selfupdate.Options{CurrentVersion: "0.52.0", DryRun: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeSelfUpdater{res: selfupdate.Result{Status: selfupdate.StatusUpToDate}}
			stubSelfUpdater(t, "0.52.0", fake)
			_, err := runSelfUpdateCmd(t, tt.args...)
			require.NoError(t, err)
			assert.Equal(t, tt.want, fake.opts)
			// The command's context reaches Run, so Ctrl-C can cancel it.
			assert.NotNil(t, fake.ctx)
		})
	}
}

func TestSelfUpdateCmd_ReturnsErrors(t *testing.T) {
	sigErr := errors.New("release v0.53.0 failed signature verification, refusing to update: bad signature")
	stubSelfUpdater(t, "0.52.0", &fakeSelfUpdater{err: sigErr})
	out, err := runSelfUpdateCmd(t)
	require.ErrorIs(t, err, sigErr)
	assert.NotContains(t, out, "Updated")
}

func TestSelfUpdateCmd_DevBuild(t *testing.T) {
	stubSelfUpdater(t, "dev", &fakeSelfUpdater{err: fmt.Errorf("%w (version %q)", selfupdate.ErrDevBuild, "dev")})
	_, err := runSelfUpdateCmd(t)
	require.ErrorIs(t, err, selfupdate.ErrDevBuild)
}

func TestSelfUpdateCmd_RejectsArgs(t *testing.T) {
	fake := &fakeSelfUpdater{}
	stubSelfUpdater(t, "0.53.0", fake)
	_, err := runSelfUpdateCmd(t, "extra")
	require.Error(t, err)
	assert.Zero(t, fake.runs, "Run was called for invalid arguments")
}

func TestNewSelfUpdaterFn_ReturnsRealUpdater(t *testing.T) {
	assert.IsType(t, &selfupdate.Updater{}, newSelfUpdaterFn())
}
