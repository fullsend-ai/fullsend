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
// update to the queued follow-up run.
var ErrSteerUnsupported = errors.New("runtime does not support steering")

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
	Steer(ctx context.Context, sandboxName string, msg SteerMessage) error
	// Settle tells the runtime no further steers will arrive. Run returns
	// after the agent finishes the turn it is on. Calling Settle on a run
	// that is not steerable or has already ended is a no-op.
	Settle(ctx context.Context, sandboxName string) error
}

// SteerResult records what a steer did, for the run summary and the
// post-run marker the queued follow-up run reads.
type SteerResult struct {
	FollowUpRunID int64
	// DeliveredAt is when the message reached the agent (live) or the
	// resumed process started (interrupt+resume).
	DeliveredAt time.Time
	// Mode is "live" or "resume".
	Mode string
}
