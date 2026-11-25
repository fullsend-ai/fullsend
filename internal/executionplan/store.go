package executionplan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// FileStore checkpoints plans in a private operator-owned directory. Its lock
// covers backend calls, and the OS releases it after process loss. Use a shared
// filesystem with working flock semantics for multiple local processes. Separate
// CI machines need a fenced remote Store, not copies of this directory.
// Neither the directory nor its ancestors may be writable by untrusted actors.
type FileStore struct{ Dir string }

const maxCheckpointBytes = 64 << 20

func (s *FileStore) path(id string) (string, error) {
	if !safeID.MatchString(id) || s.Dir == "" {
		return "", errors.New("state directory and safe plan ID required")
	}
	return filepath.Join(s.Dir, id+".json"), nil
}

// Read loads an atomic snapshot without taking the writer lock.
func (s *FileStore) Read(id string) (State, error) {
	var state State
	path, err := s.path(id)
	if err != nil {
		return state, err
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return state, err
	}
	defer f.Close() //nolint:errcheck
	info, err := f.Stat()
	if err != nil {
		return state, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxCheckpointBytes {
		return state, errors.New("bounded regular checkpoint required")
	}
	decoder := json.NewDecoder(io.LimitReader(f, maxCheckpointBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return state, fmt.Errorf("decode checkpoint: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return state, errors.New("trailing checkpoint data")
	}
	if err := state.validate(); err != nil {
		return state, err
	}
	return state, nil
}

// Update holds a context-aware exclusive lock through every checkpoint and
// external operation. Save fsyncs the private temporary file and directory.
func (s *FileStore) Update(ctx context.Context, id string, fn func(*State, func() error) error) error {
	path, err := s.path(id)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// The parent must already exist durably. Sync its directory entry even
	// when another reconciler won Mkdir, before either process can dispatch.
	if err := os.Mkdir(s.Dir, 0o700); err != nil && !os.IsExist(err) {
		return err
	}
	parent, err := os.Open(filepath.Dir(filepath.Clean(s.Dir)))
	if err != nil {
		return err
	}
	syncErr := parent.Sync()
	closeErr := parent.Close()
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close() //nolint:errcheck
	for {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck
	if err := ctx.Err(); err != nil {
		return err
	}
	state, err := s.Read(id)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return fn(&state, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := state.validate(); err != nil {
			return err
		}
		b, err := json.MarshalIndent(state, "", "  ")
		if err != nil {
			return err
		}
		if len(b) > maxCheckpointBytes {
			return errors.New("checkpoint exceeds size limit")
		}
		temp, err := os.CreateTemp(s.Dir, ".execution-plan-*")
		if err != nil {
			return err
		}
		defer os.Remove(temp.Name()) //nolint:errcheck
		if _, err := temp.Write(b); err != nil {
			_ = temp.Close()
			return err
		}
		if err := temp.Sync(); err != nil {
			_ = temp.Close()
			return err
		}
		if err := temp.Close(); err != nil {
			return err
		}
		if err := os.Rename(temp.Name(), path); err != nil {
			return err
		}
		dir, err := os.Open(s.Dir)
		if err != nil {
			return err
		}
		defer dir.Close() //nolint:errcheck
		return dir.Sync()
	})
}
