// Package executionplan coordinates bounded, source-bound single-target runs.
// Domain adapters supply reviewed targets; trusted drivers supply authorization
// and execution. A plan never grants permissions by naming a repository.
package executionplan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var safeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
var sourceSHA = regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64})$`)

// ErrPlanChanged prevents reusing a plan identity with different inputs or limits.
var ErrPlanChanged = errors.New("execution plan changed: retain existing state; supersession is not implemented")

// Target is one immutable source snapshot. Context carries domain data, not authority.
type Target struct {
	ID         string          `json:"id"`
	Forge      string          `json:"forge"`
	Host       string          `json:"host"`
	Repository string          `json:"repository"`
	Ref        string          `json:"ref"`
	SHA        string          `json:"sha"`
	Context    json.RawMessage `json:"context,omitempty"`
}

// Plan is an immutable request. Authorization is a reference the driver must verify
// against trusted policy; it is never a caller's assertion of their own privileges.
type Plan struct {
	Version       int       `json:"version"`
	ID            string    `json:"id"`
	Request       string    `json:"request"`
	Authorization string    `json:"authorization"`
	Harness       string    `json:"harness"`
	ControlSHA    string    `json:"control_sha"`
	Deadline      time.Time `json:"deadline"`
	MaxParallel   int       `json:"max_parallel"`
	MaxAttempts   int       `json:"max_attempts"`
	Targets       []Target  `json:"targets"`
}

// Validate rejects incomplete and unbounded plans before any external operation.
func (p Plan) Validate() error {
	if p.Version != 1 || !safeID.MatchString(p.ID) || strings.TrimSpace(p.Request) == "" || strings.TrimSpace(p.Authorization) == "" || !safeID.MatchString(p.Harness) || !sourceSHA.MatchString(p.ControlSHA) || p.Deadline.IsZero() {
		return errors.New("version 1, safe plan/harness IDs, request, authorization, control SHA and deadline required")
	}
	// Initial safety ceilings bound state and external work in this experimental
	// driver. Larger installations need an explicitly reviewed contract revision.
	if len(p.Targets) == 0 || len(p.Targets) > 256 || p.MaxParallel < 1 || p.MaxParallel > 32 || p.MaxAttempts < 1 || p.MaxAttempts > 10 {
		return errors.New("require 1..256 targets, parallelism 1..32 and attempts 1..10")
	}
	seen := map[string]bool{}
	for _, t := range p.Targets {
		u, err := url.Parse("https://" + t.Host)
		if !safeID.MatchString(t.ID) || seen[t.ID] || !sourceSHA.MatchString(t.SHA) || err != nil || u.Hostname() == "" || u.Host != t.Host || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(t.Host, " \\\t\r\n") {
			return errors.New("target requires a unique safe ID, source SHA and host")
		}
		if t.Forge != "github" && t.Forge != "gitlab" && t.Forge != "forgejo" {
			return errors.New("unsupported source forge")
		}
		parts := strings.Split(t.Repository, "/")
		if len(parts) < 2 {
			return errors.New("repository must include owner and name")
		}
		for _, part := range parts {
			if !safeID.MatchString(part) && !repositoryPart(part) {
				return errors.New("invalid repository path")
			}
		}
		if !strings.HasPrefix(t.Ref, "refs/heads/") || strings.TrimPrefix(t.Ref, "refs/heads/") == "" || strings.ContainsAny(t.Ref, " \t\r\n~^:?*[\\") || strings.Contains(t.Ref, "..") || strings.Contains(t.Ref, "@{") || strings.HasSuffix(t.Ref, "/") || strings.HasSuffix(t.Ref, ".") || strings.Contains(t.Ref, "//") {
			return errors.New("target must name a full branch ref")
		}
		if len(t.Context) > 65536 || (len(t.Context) > 0 && !json.Valid(t.Context)) {
			return errors.New("target context must be bounded JSON")
		}
		seen[t.ID] = true
	}
	return nil
}

func repositoryPart(s string) bool {
	if s == "" || s == "." || s == ".." || strings.HasPrefix(s, "-") {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.') {
			return false
		}
	}
	return len(s) <= 255
}

// Digest binds all plan fields, including target order, context and work limits.
func (p Plan) Digest() string {
	b, _ := json.Marshal(p)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Phase describes execution status, independently of domain findings.
type Phase string

const (
	Uncertain Phase = "uncertain"
	Running   Phase = "running"
	Succeeded Phase = "succeeded"
	Failed    Phase = "failed"
)

// Attempt is an immutable launch request; its ID is also the reconciliation key.
type Attempt struct {
	ID         string   `json:"id"`
	PlanDigest string   `json:"plan_digest"`
	Target     Target   `json:"target"`
	Number     int      `json:"number"`
	State      Phase    `json:"state"`
	Receipt    *Receipt `json:"receipt,omitempty"`
}

// Receipt must come from a trusted execution backend, never from model prose.
// A terminal state asserts that the external run has stopped consuming capacity.
type Receipt struct {
	AttemptID  string `json:"attempt_id"`
	PlanDigest string `json:"plan_digest"`
	TargetID   string `json:"target_id"`
	SourceSHA  string `json:"source_sha"`
	RunID      string `json:"run_id"`
	State      Phase  `json:"state"`
}

// TargetState retains all attempts, including failures that consume the budget.
type TargetState struct {
	Target   Target    `json:"target"`
	Blocker  string    `json:"blocker,omitempty"`
	Attempts []Attempt `json:"attempts"`
}

// State is the complete persisted accounting for an immutable plan.
type State struct {
	Version int           `json:"version"`
	Plan    Plan          `json:"plan"`
	Digest  string        `json:"digest"`
	Targets []TargetState `json:"targets"`
}

// Complete requires successful execution for every required target. Consumers
// must separately validate assessment findings and authorize follow-up actions.
func (s State) Complete() bool {
	if len(s.Targets) == 0 {
		return false
	}
	for _, t := range s.Targets {
		if len(t.Attempts) == 0 || t.Attempts[len(t.Attempts)-1].State != Succeeded {
			return false
		}
	}
	return true
}

// Driver is installed by the operator, outside source/model-controlled inputs.
// Authorize verifies current target permissions, the authorization reference and
// trusted control revision. Submit must bind the actual checkout to Target.SHA.
// Lookup returns nil for an unknown launch; absence NEVER authorizes resubmission.
// All methods must honor cancellation. Forge adapters must use forge.Client.
type Driver interface {
	Authorize(context.Context, Plan, Target) error
	Submit(context.Context, Plan, Attempt) (*Receipt, error)
	Lookup(context.Context, Plan, Attempt) (*Receipt, error)
}

// Store serializes a whole reconciliation. Save must durably checkpoint the
// current state before returning. Remote implementations require fencing that
// also covers external side effects, not just an optimistic write at the end.
type Store interface {
	Update(context.Context, string, func(*State, func() error) error) error
}

// Reconcile advances a plan once. Checkpointing intent precedes Submit so crashes
// and ambiguous responses recover through Lookup instead of repeating a write.
func Reconcile(ctx context.Context, p Plan, store Store, driver Driver) (State, error) {
	var result State
	if err := p.Validate(); err != nil {
		return result, err
	}
	if store == nil || driver == nil {
		return result, errors.New("store and driver required")
	}
	err := store.Update(ctx, p.ID, func(s *State, save func() error) error {
		defer func() { result = *s }()
		if s.Version == 0 {
			s.Version = 1
			s.Plan = p
			s.Digest = p.Digest()
			for _, t := range p.Targets {
				s.Targets = append(s.Targets, TargetState{Target: t, Attempts: []Attempt{}})
			}
			if err := save(); err != nil {
				return err
			}
		}
		if err := s.validate(); err != nil {
			return err
		}
		if s.Digest != p.Digest() {
			return ErrPlanChanged
		}
		// Always inspect outstanding work, even after the deadline or loss of launch
		// authorization: observing existing work must not grant new execution rights.
		for ti := range s.Targets {
			t := &s.Targets[ti]
			for ai := range t.Attempts {
				a := &t.Attempts[ai]
				if a.State == Succeeded || a.State == Failed {
					continue
				}
				r, err := driver.Lookup(ctx, p, *a)
				if err != nil {
					return fmt.Errorf("lookup target %s: %w", t.Target.ID, err)
				}
				if r == nil {
					continue
				}
				if err := accept(a, r); err != nil {
					return err
				}
				if err := save(); err != nil {
					return err
				}
			}
		}
		active := 0
		for _, t := range s.Targets {
			for _, a := range t.Attempts {
				if a.State == Uncertain || a.State == Running {
					active++
				}
			}
		}
		for ti := range s.Targets {
			if err := ctx.Err(); err != nil {
				return err
			}
			t := &s.Targets[ti]
			t.Blocker = ""
			if len(t.Attempts) > 0 && t.Attempts[len(t.Attempts)-1].State != Failed {
				continue
			}
			if len(t.Attempts) >= p.MaxAttempts {
				t.Blocker = "exhausted"
				continue
			}
			if !time.Now().Before(p.Deadline) {
				t.Blocker = "deadline"
				continue
			}
			if active >= p.MaxParallel {
				t.Blocker = "capacity"
				continue
			}
			if err := driver.Authorize(ctx, p, t.Target); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				t.Blocker = "authorization"
				continue
			}
			// Recheck time after potentially slow authorization.
			if !time.Now().Before(p.Deadline) {
				t.Blocker = "deadline"
				continue
			}
			n := len(t.Attempts) + 1
			a := Attempt{ID: attemptID(s.Digest, t.Target.ID, n), PlanDigest: s.Digest, Target: t.Target, Number: n, State: Uncertain}
			t.Attempts = append(t.Attempts, a)
			if err := save(); err != nil {
				return err
			}
			r, err := driver.Submit(ctx, p, a)
			if err != nil {
				return fmt.Errorf("dispatch target %s uncertain; reconcile before retry: %w", t.Target.ID, err)
			}
			if r == nil {
				return errors.New("dispatch returned no receipt; attempt remains uncertain")
			}
			if err := accept(&t.Attempts[n-1], r); err != nil {
				return err
			}
			if err := save(); err != nil {
				return err
			}
			if r.State == Running {
				active++
			}
		}
		return save()
	})
	// Return a detached last-known snapshot even when a checkpoint or backend fails.
	// Store owns the authoritative state; callers must reload before taking actions.
	if reader, ok := store.(interface{ Read(string) (State, error) }); ok {
		result, _ = reader.Read(p.ID)
	}
	return result, err
}

func attemptID(planDigest, target string, number int) string {
	h := sha256.Sum256(fmt.Appendf(nil, "%s/%s/%d", planDigest, target, number))
	return hex.EncodeToString(h[:])
}
func accept(a *Attempt, r *Receipt) error {
	if r.AttemptID != a.ID || r.PlanDigest != a.PlanDigest || r.TargetID != a.Target.ID || r.SourceSHA != a.Target.SHA || strings.TrimSpace(r.RunID) == "" || (a.Receipt != nil && r.RunID != a.Receipt.RunID) || (r.State != Running && r.State != Succeeded && r.State != Failed) {
		return errors.New("execution receipt does not match admitted attempt")
	}
	copyReceipt := *r
	a.Receipt = &copyReceipt
	a.State = r.State
	return nil
}

func (s State) validate() error {
	if s.Version != 1 || s.Plan.Validate() != nil || s.Digest != s.Plan.Digest() || len(s.Targets) != len(s.Plan.Targets) {
		return errors.New("invalid execution plan checkpoint")
	}
	for i, t := range s.Targets {
		b, _ := json.Marshal(t.Target)
		expected, _ := json.Marshal(s.Plan.Targets[i])
		if string(b) != string(expected) || len(t.Attempts) > s.Plan.MaxAttempts {
			return errors.New("invalid target checkpoint")
		}
		for j, a := range t.Attempts {
			b, _ = json.Marshal(a.Target)
			if string(b) != string(expected) || a.ID != attemptID(s.Digest, t.Target.ID, j+1) || a.Number != j+1 || a.PlanDigest != s.Digest || (j < len(t.Attempts)-1 && a.State != Failed) {
				return errors.New("invalid attempt checkpoint")
			}
			if a.State == Uncertain {
				if a.Receipt != nil {
					return errors.New("uncertain attempt has receipt")
				}
				continue
			}
			if a.Receipt == nil || a.State != a.Receipt.State {
				return errors.New("attempt missing matching receipt")
			}
			if err := accept(&a, a.Receipt); err != nil {
				return err
			}
		}
	}
	return nil
}
