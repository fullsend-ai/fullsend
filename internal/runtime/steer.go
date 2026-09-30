package runtime

import (
	"context"
	"errors"
	"sort"
	"time"
)

// amendmentEvents are the forge events whose run record's actor is, by
// construction, the principal the Route job checked, so the envelope may
// name that actor as authorized. It is the one definition: steerwatch binds
// amendments from the same set (see its amendmentEvents for the per-arm
// audit of reusable-dispatch.yml). It is unexported so that no package can
// widen the trust boundary at run time; read it through IsAmendmentEvent or
// AmendmentEvents.
var amendmentEvents = map[string]bool{
	"issue_comment": true,
}

// IsAmendmentEvent reports whether a run of this event names, as its actor,
// the principal the Route job checked.
func IsAmendmentEvent(event string) bool {
	return amendmentEvents[event]
}

// AmendmentEvents returns the amendment events, sorted, as a new slice on
// every call, so a caller cannot change the set through it.
func AmendmentEvents() []string {
	out := make([]string, 0, len(amendmentEvents))
	for event := range amendmentEvents {
		out = append(out, event)
	}
	sort.Strings(out)
	return out
}

// SteerMessage is one update the runner delivers into an in-flight agent
// session. The runner authors every field: Text is already sanitized (the
// same Unicode sanitizer buildFeedbackPrompt uses) and the provenance fields
// name the follow-up workflow run whose route job authorized the event.
//
// A steer is content, never capability: it cannot change tools, model,
// role, scope, or network policy. Runtimes render it as a user message.
type SteerMessage struct {
	// FollowUpRunID is the forge-side id of the workflow run that carried
	// the update (GitHub Actions run id, GitLab pipeline id). Zero when the
	// steer did not originate from a run (local `fullsend run`).
	FollowUpRunID int64
	// Event is the forge event name that produced the run
	// (e.g. "pull_request_target", "issue_comment").
	Event string
	// Actor is the forge login that triggered the event. The envelope
	// vouches for it only when IsAmendmentEvent(Event).
	Actor string
	// CreatedAt is when the follow-up run was created.
	CreatedAt time.Time
	// HeadSHA is the work item's head after the update, when it moved.
	// Empty when only comments, labels, or the body changed.
	HeadSHA string
	// Text is the sanitized delta the agent should act on.
	Text string
}

// ErrSteerUnsupported is returned by Steer when the runtime cannot take a
// message into the running session. The runner logs it and leaves the
// update to the run queued behind this one (ADR 0106).
var ErrSteerUnsupported = errors.New("runtime does not support steering")

// ErrNoSteerSession is returned by Steer when no steerable Run is
// registered for the sandbox — Run has not started yet, or it already
// returned. It is deliberately distinct from ErrSteerUnsupported: the
// runtime *can* steer, so the caller should retry rather than write the
// run off as unsteerable.
var ErrNoSteerSession = errors.New("no steerable run is registered for this sandbox")

// ErrSteerAfterSettle is returned by Steer when the session has already
// settled, so the message was not delivered and never will be. Steer used
// to report this as success, which left a caller unable to tell a
// delivered steer from a discarded one.
var ErrSteerAfterSettle = errors.New("steer arrived after the session began settling")

// ErrSteerSessionLost is returned by Steer when an interrupt-and-resume
// runtime stopped the sandbox for a steer and the sandbox did not come back:
// it never reached Ready, or it left Ready right after the start. The
// session cannot be resumed, so the steer was not delivered and never will
// be. Steer returns it again on every later call for that session, so a
// caller never stops and starts a sandbox that is already gone. Run ends
// with an error that wraps it. It is terminal, not transient: the caller
// stops steering and leaves the update to the run queued behind this one.
var ErrSteerSessionLost = errors.New("the sandbox did not come back from the interrupt; the session is gone")

// Steerer is implemented by runtimes that can take a message into a running
// session while Run is still executing. It is only consulted when
// RunParams.Steerable is true; Run then keeps the session open until Settle
// is called and the current turn has completed.
//
// Live runtimes (Claude Code stream-json input, pi rpc) queue the message
// for the agent's next tool boundary in the same process. Runtimes without
// a live channel (Codex exec) stop the current process and resume the same
// session with the message as the next prompt.
//
// Both methods are called from a goroutine other than the one blocked in
// Run, and must be safe against Run returning early on error or timeout.
//
// CALLER OBLIGATION: the runner must hold its sandbox write lock
// (internal/cli's sandboxMu) across every Steer and Settle call, exactly
// as it does across ClearIterationArtifacts. Both methods act on the
// running sandbox — the mailbox append and the feeder kill for the live
// runtimes, a sandbox stop and start for interrupt-and-resume — and those
// race the OIDC refresher and the OpenAI re-seeder, which the runner
// already serializes through that lock. The stop is the sharp edge: it
// ends every process in the sandbox, so a refresher upload running
// concurrently would be cut off mid-write and leave a truncated
// credential, and any exec during the stop fails. Holding the lock makes
// the refreshers wait the interrupt out (see CodexSteerInterruptCost). The
// lock cannot be taken here: it lives in internal/cli.
type Steerer interface {
	// Steer delivers msg into the session started by the in-flight Run.
	//
	// A nil return means ACCEPTED, not delivered. The runtimes differ in
	// how much that covers: the live ones have written the message into
	// the mailbox by then, while codex has only queued it for the next
	// resume. Neither proves the agent received it — that is what
	// RunMetrics.Steers records, one entry per steer the runtime saw
	// acknowledged.
	//
	// The gap is not merely bookkeeping. A run can stop between accepting
	// a steer and delivering it: Run commits to exiting, and a Steer that
	// was already past the settled check returns nil for a message nothing
	// will now deliver. The runtimes narrow that window to the lock, and
	// ErrSteerAfterSettle covers every case they can see — but a caller
	// that treats nil as proof of delivery, and marks the update handled
	// on that basis, will lose one. Confirm against RunMetrics.Steers.
	//
	// ErrSteerSessionLost is terminal: the sandbox did not survive the
	// interrupt, so no steer on this session will be delivered.
	Steer(ctx context.Context, sandboxName string, msg SteerMessage) error
	// Settle tells the runtime no further steers will arrive. Run returns
	// after the agent finishes the turn it is on. Calling Settle on a run
	// that is not steerable or has already ended is a no-op.
	Settle(ctx context.Context, sandboxName string) error
}

// SteerDecliner is implemented by a Steerer that refuses some runs up front.
// The runner asks it before starting the follow-up run watcher, so a run the
// runtime would refuse is announced once and spends no poll or steer slot
// finding that out from Steer.
type SteerDecliner interface {
	// SteerDeclineReason reports why a run over params will not take
	// steers. It reads SandboxName, Model, FallbackModels and ModelAliases,
	// and is called after Bootstrap.
	SteerDeclineReason(params RunParams) (reason string, declined bool)
}

// Steer modes recorded on SteerResult.
const (
	// SteerModeLive and SteerModeResume are the values of SteerResult.Mode:
	// the message reached a running session, or the session was restarted
	// with it. Exported so a caller can branch on the mode without matching
	// a bare string literal.
	SteerModeLive   = "live"
	SteerModeResume = "resume"
)

// SteerResult records what a steer did, for the run summary and for
// whatever the runner reports about the run afterwards.
type SteerResult struct {
	FollowUpRunID int64 `json:"follow_up_run_id"`
	// DeliveredAt is when the message reached the agent (live) or the
	// resumed process started (interrupt+resume).
	DeliveredAt time.Time `json:"delivered_at"`
	// Mode is SteerModeLive or SteerModeResume.
	Mode string `json:"mode"`
}
