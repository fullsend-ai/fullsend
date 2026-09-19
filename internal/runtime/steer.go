package runtime

import (
	"context"
	"errors"
	"time"
)

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
	// Actor is the forge login that triggered the event.
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
// as it does across ClearIterationArtifacts. Both methods write into the
// running sandbox — the mailbox append and the feeder kill for the live
// runtimes, the stray-process sweep for interrupt-and-resume — and those
// races the OIDC refresher and the OpenAI re-seeder, which the runner
// already serializes through that lock. The sweep is the sharp edge: it
// kills every process of the sandbox user, so a refresher upload running
// concurrently would be killed mid-write and leave a truncated
// credential. The lock cannot be taken here: it lives in internal/cli.
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
	Steer(ctx context.Context, sandboxName string, msg SteerMessage) error
	// Settle tells the runtime no further steers will arrive. Run returns
	// after the agent finishes the turn it is on. Calling Settle on a run
	// that is not steerable or has already ended is a no-op.
	Settle(ctx context.Context, sandboxName string) error
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
