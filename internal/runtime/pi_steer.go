package runtime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
)

// piRPCPrompt is one line of pi's --mode rpc command input. The opening
// prompt and every steer go in the same way, so a steer is the same kind
// of message as the prompt: content, never capability.
//
// StreamingBehavior is "steer" on every message after the first. Probed on
// pi 0.84.4 both ways: a steer sent DURING a tool call is accepted
// (queue_update, response success) and delivered as a new turn before
// agent_end, and a steer sent while the agent is IDLE starts a fresh
// agent_start...agent_settled cycle of its own. Because idle works, the
// flag can be unconditional — which removes the race that choosing between
// "steer" and a plain prompt would otherwise have: the runner decides
// under its own lock, but pi reads the line later, and the agent may have
// settled in between.
type piRPCPrompt struct {
	ID                string `json:"id"`
	Type              string `json:"type"`
	Message           string `json:"message"`
	StreamingBehavior string `json:"streamingBehavior,omitempty"`
}

// piInputLine encodes one rpc prompt as NDJSON. Encoding through
// encoding/json is what keeps a multi-line steer on one line: a literal
// newline would end the record and the remainder would parse as a second,
// malformed command.
func piInputLine(id, text, behavior string) (string, error) {
	b, err := json.Marshal(piRPCPrompt{
		ID:                id,
		Type:              "prompt",
		Message:           text,
		StreamingBehavior: behavior,
	})
	if err != nil {
		return "", fmt.Errorf("encoding rpc prompt: %w", err)
	}
	return string(b), nil
}

// piSteerBehavior is the streamingBehavior every steer carries.
const piSteerBehavior = "steer"

// newPiSessionID returns the id the run will pass to `pi --session-id`.
// pi's rpc mode emits no `session` event — verified on 0.84.4, including
// with --session-dir, where the id appears only in the session file's name
// — so the runner names the session instead of discovering it. The flag
// creates the session when it does not exist ("creating a new session with
// that id"), and the file is written as <timestamp>_<id>.jsonl.
func newPiSessionID() string { return uuid.NewString() }

// Steer implements Steerer for pi: it appends the message to the mailbox
// the in-sandbox feeder is tailing into `pi --mode rpc`, and pi takes it at
// the next tool boundary.
//
// The runner MUST hold its sandbox write lock across this call; see the
// Steerer contract.
func (PiRuntime) Steer(ctx context.Context, sandboxName string, msg SteerMessage) error {
	f, ok := lookupSteerFeed(sandboxName)
	if !ok {
		return errNoSteerSession
	}
	line, err := piInputLine(uuid.NewString(), renderSteerEnvelope(msg), piSteerBehavior)
	if err != nil {
		return err
	}
	return f.appendLine(ctx, msg, line)
}

// Settle implements Steerer for pi. As on Claude Code it does not close
// stdin mid-turn: it stops the feeder only once every message written has
// been acked and no turn is in flight, which for pi means after
// agent_settled with nothing pending.
func (PiRuntime) Settle(ctx context.Context, sandboxName string) error {
	f, ok := lookupSteerFeed(sandboxName)
	if !ok {
		return nil
	}
	if f.settle() {
		return f.stopFeeder(ctx)
	}
	return nil
}

// Ensure PiRuntime implements Steerer.
var _ Steerer = PiRuntime{}
