package prpoll

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

// StateVersion is the version of the state file format.
const StateVersion = 1

// Head statuses kept in the state file. An empty status is a head the poll
// may still dispatch.
const (
	HeadFinished  = "finished"
	HeadExhausted = "exhausted"
)

const (
	// maxStateBytes is the largest state file LoadState reads; a larger
	// file is treated as corrupt.
	maxStateBytes = 1 << 20
	// stateMaxAge is how long a head entry is kept after its last update.
	stateMaxAge = 14 * 24 * time.Hour
	// stateMaxEntries caps the head entries; the oldest go first.
	stateMaxEntries = 2000
)

// HeadState is the poll's record of one pull request head commit.
type HeadState struct {
	// Dispatches is the number of review dispatches the poll emitted for
	// the head.
	Dispatches int `json:"dispatches"`
	// Status is "", HeadFinished or HeadExhausted.
	Status string `json:"status"`
	// UpdatedAt is the RFC 3339 time of the entry's last change.
	UpdatedAt string `json:"updated_at"`
}

// State is the poll's per-head state across cycles. The poll workflow
// restores it from, and saves it to, the Actions cache, so its content is
// never trusted to start a review: it can only stop or bound one.
type State struct {
	Version int                   `json:"version"`
	Heads   map[string]*HeadState `json:"heads"`

	changed bool
}

// NewState returns an empty state.
func NewState() *State {
	return &State{Version: StateVersion, Heads: map[string]*HeadState{}}
}

// LoadState reads the state file at path. A missing file is an empty
// state; an unreadable, oversized or malformed file is an empty state with
// a warning on warn, marked changed so that the next save replaces it. It
// never fails: a bad cache entry must not stop the poll.
func LoadState(path string, warn io.Writer) *State {
	if warn == nil {
		warn = io.Discard
	}
	s := NewState()
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s
	}
	if err != nil {
		return s.reset(warn, path, err.Error())
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxStateBytes+1))
	if err != nil {
		return s.reset(warn, path, err.Error())
	}
	if len(data) > maxStateBytes {
		return s.reset(warn, path, fmt.Sprintf("larger than %d bytes", maxStateBytes))
	}
	if len(data) == 0 {
		return s.reset(warn, path, "empty")
	}
	var loaded State
	if err := json.Unmarshal(data, &loaded); err != nil {
		return s.reset(warn, path, "invalid JSON: "+err.Error())
	}
	if loaded.Version != StateVersion {
		return s.reset(warn, path, fmt.Sprintf("unsupported version %d", loaded.Version))
	}
	for key, h := range loaded.Heads {
		if h == nil || h.Dispatches < 0 || (h.Status != "" && h.Status != HeadFinished && h.Status != HeadExhausted) {
			fmt.Fprintf(warn, "warning: poll state %s: dropping invalid entry %q\n", path, key)
			s.changed = true
			continue
		}
		s.Heads[key] = h
	}
	return s
}

// reset warns that the state file is unusable and returns s, empty and
// marked changed.
func (s *State) reset(warn io.Writer, path, why string) *State {
	fmt.Fprintf(warn, "warning: poll state %s is unusable (%s); starting with an empty state\n", path, why)
	s.changed = true
	return s
}

// Changed reports whether the state differs from the file it was loaded
// from.
func (s *State) Changed() bool {
	return s.changed
}

// Save writes the state to path, replacing the file atomically.
func (s *State) Save(path string) error {
	data, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("marshal poll state: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("write poll state: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return fmt.Errorf("write poll state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write poll state: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("write poll state: %w", err)
	}
	return nil
}

// Prune drops entries last updated more than stateMaxAge before now (or
// with an unreadable time), then the oldest entries beyond
// stateMaxEntries.
func (s *State) Prune(now time.Time) {
	cutoff := now.Add(-stateMaxAge)
	type aged struct {
		key string
		at  time.Time
	}
	var kept []aged
	for key, h := range s.Heads {
		at, err := time.Parse(time.RFC3339, h.UpdatedAt)
		if err != nil || at.Before(cutoff) {
			delete(s.Heads, key)
			s.changed = true
			continue
		}
		kept = append(kept, aged{key, at})
	}
	if len(kept) <= stateMaxEntries {
		return
	}
	sort.Slice(kept, func(i, j int) bool {
		if !kept[i].at.Equal(kept[j].at) {
			return kept[i].at.Before(kept[j].at)
		}
		return kept[i].key < kept[j].key
	})
	for _, a := range kept[:len(kept)-stateMaxEntries] {
		delete(s.Heads, a.key)
	}
	s.changed = true
}

// headKey is the state key of pull request number's head commit sha.
func headKey(number int, sha string) string {
	return strconv.Itoa(number) + ":" + sha
}

// head returns the entry for key, or nil.
func (s *State) head(key string) *HeadState {
	return s.Heads[key]
}

// entry returns the entry for key, creating it.
func (s *State) entry(key string) *HeadState {
	h := s.Heads[key]
	if h == nil {
		h = &HeadState{}
		s.Heads[key] = h
	}
	return h
}

// setStatus records status for key at now.
func (s *State) setStatus(key, status string, now time.Time) {
	h := s.entry(key)
	if h.Status == status {
		return
	}
	h.Status = status
	h.UpdatedAt = now.UTC().Format(time.RFC3339)
	s.changed = true
}

// recordDispatch counts one emitted dispatch for key at now.
func (s *State) recordDispatch(key string, now time.Time) {
	h := s.entry(key)
	h.Dispatches++
	h.UpdatedAt = now.UTC().Format(time.RFC3339)
	s.changed = true
}

// dispatches returns the dispatches recorded for key.
func (s *State) dispatches(key string) int {
	if h := s.Heads[key]; h != nil {
		return h.Dispatches
	}
	return 0
}
