package executionplan

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStoreSerializesAndHonorsCancellation(t *testing.T) {
	s := &FileStore{Dir: t.TempDir()}
	p := examplePlan()
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- s.Update(context.Background(), p.ID, func(state *State, save func() error) error {
			state.Version = 1
			state.Plan = p
			state.Digest = p.Digest()
			for _, target := range p.Targets {
				state.Targets = append(state.Targets, TargetState{Target: target})
			}
			close(entered)
			<-release
			return save()
		})
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := s.Update(ctx, p.ID, func(*State, func() error) error { t.Error("overlapping writer"); return nil })
	require.ErrorIs(t, err, context.DeadlineExceeded)
	close(release)
	require.NoError(t, <-done)
	got, err := s.Read(p.ID)
	require.NoError(t, err)
	require.Equal(t, p.ID, got.Plan.ID)
	info, err := os.Stat(filepath.Join(s.Dir, p.ID+".json"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestStoreRejectsCorruptionAndUnsafePaths(t *testing.T) {
	s := &FileStore{Dir: t.TempDir()}
	ctx := context.Background()
	require.Error(t, s.Update(ctx, "../escape", func(*State, func() error) error { return nil }))
	require.NoError(t, os.WriteFile(filepath.Join(s.Dir, "case-123.json"), []byte(`{"version":1`), 0o600))
	require.Error(t, s.Update(ctx, "case-123", func(*State, func() error) error { t.Error("corruption overwritten"); return nil }))
	require.NoError(t, os.Remove(filepath.Join(s.Dir, "case-123.json")))
	outside := filepath.Join(t.TempDir(), "outside.json")
	require.NoError(t, os.WriteFile(outside, []byte("{}"), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(s.Dir, "case-123.json")))
	require.Error(t, s.Update(ctx, "case-123", func(*State, func() error) error { return nil }))
}

func TestStoreRejectsInvalidWritesAndStorageFailures(t *testing.T) {
	ctx := context.Background()
	p := examplePlan()
	s := &FileStore{Dir: t.TempDir()}
	require.Error(t, s.Update(ctx, p.ID, func(state *State, save func() error) error { return save() }))
	require.Error(t, s.Update(ctx, p.ID, func(state *State, save func() error) error {
		state.Version = 1
		state.Plan = p
		state.Digest = p.Digest()
		for _, target := range p.Targets {
			state.Targets = append(state.Targets, TargetState{Target: target})
		}
		savedDir := s.Dir
		s.Dir = filepath.Join(savedDir, "missing")
		defer func() { s.Dir = savedDir }()
		return save()
	}))
	cancelled, cancel := context.WithCancel(ctx)
	require.ErrorIs(t, s.Update(cancelled, p.ID, func(state *State, save func() error) error {
		cancel()
		return save()
	}), context.Canceled)
	badDir := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(badDir, []byte("file"), 0o600))
	require.Error(t, (&FileStore{Dir: badDir}).Update(ctx, p.ID, func(*State, func() error) error { return nil }))
	lockPath := filepath.Join(s.Dir, p.ID+".json.lock")
	require.NoError(t, os.Remove(lockPath))
	require.NoError(t, os.Symlink(badDir, lockPath))
	require.Error(t, s.Update(ctx, p.ID, func(*State, func() error) error { return nil }))
}

func TestStoreRejectsTrailingAndUnknownCheckpointData(t *testing.T) {
	s := &FileStore{Dir: t.TempDir()}
	p := examplePlan()
	for _, data := range []string{`{} {}`, `{"unknown":true}`, `{"version":3}`} {
		require.NoError(t, os.WriteFile(filepath.Join(s.Dir, p.ID+".json"), []byte(data), 0o600))
		_, err := s.Read(p.ID)
		require.Error(t, err)
	}
	require.NoError(t, os.Remove(filepath.Join(s.Dir, p.ID+".json")))
	require.NoError(t, os.Mkdir(filepath.Join(s.Dir, p.ID+".json"), 0o700))
	_, err := s.Read(p.ID)
	require.Error(t, err)
}

func TestCheckpointIntegrityBeforeRecovery(t *testing.T) {
	p := examplePlan()
	p.Targets = p.Targets[:1]
	s := &FileStore{Dir: t.TempDir()}
	_, err := Reconcile(context.Background(), p, s, &testDriver{})
	require.NoError(t, err)
	baseline, err := s.Read(p.ID)
	require.NoError(t, err)
	encoded, err := json.Marshal(baseline)
	require.NoError(t, err)
	for _, test := range []struct {
		name    string
		corrupt func(*State)
	}{
		{"missing-target", func(s *State) { s.Targets = nil }},
		{"target-mismatch", func(s *State) { s.Targets[0].Target.Ref = "refs/heads/other" }},
		{"attempt-identity", func(s *State) { s.Targets[0].Attempts[0].ID = "other" }},
		{"missing-receipt", func(s *State) { s.Targets[0].Attempts[0].Receipt = nil }},
		{"uncertain-with-receipt", func(s *State) { s.Targets[0].Attempts[0].State = Uncertain }},
		{"state-mismatch", func(s *State) { s.Targets[0].Attempts[0].State = Succeeded }},
		{"receipt-binding", func(s *State) { s.Targets[0].Attempts[0].Receipt.SourceSHA = "other" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			var broken State
			require.NoError(t, json.Unmarshal(encoded, &broken))
			test.corrupt(&broken)
			raw, err := json.Marshal(broken)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(s.Dir, p.ID+".json"), raw, 0o600))
			d := &testDriver{}
			_, err = Reconcile(context.Background(), p, s, d)
			require.Error(t, err)
			require.Empty(t, d.submitted)
		})
	}
}
