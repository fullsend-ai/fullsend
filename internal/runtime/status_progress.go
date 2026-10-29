package runtime

import (
	"crypto/sha256"
	"fmt"
	"os"
)

// StatusProgressEvent is advisory runtime display metadata. It carries no
// tool output, prompts, result bodies, or authoritative run/usage outcomes.
type StatusProgressEvent struct {
	Kind   string
	ID     string
	Label  string
	State  string
	Detail string
	// Clear resets the row silently. Recovery is displayed only after a
	// prior status, avoiding a routine initial "allowed" rate-limit message.
	Clear    bool
	Recovery bool
	// Occurrence is an unkeyed one-off message, not a state snapshot.
	Occurrence bool
}

func (StatusProgressEvent) agentEvent() {}

func (r *EventRenderer) statusProgress(e StatusProgressEvent) {
	if e.Kind == "" {
		return
	}
	key := sha256.Sum256([]byte(e.Kind + "\x00" + e.ID))
	previous := r.statusProgressSeen[key]
	if e.Clear {
		delete(r.statusProgressSeen, key)
		return
	}
	if e.Recovery && previous == "" {
		return
	}
	msg := r.progressText(e.Label, 120)
	if msg == "" {
		return
	}
	if state := r.progressText(e.State, 60); state != "" {
		msg += " — " + state
	}
	if detail := r.progressText(e.Detail, 240); detail != "" {
		msg += ": " + detail
	}
	msg = sanitizeOutput(msg)
	if !e.Occurrence && previous == msg {
		return
	}
	if !e.Occurrence {
		if r.statusProgressSeen == nil {
			r.statusProgressSeen = make(map[[sha256.Size]byte]string)
		}
		if len(r.statusProgressSeen) >= maxTaskProgressEntries {
			clear(r.statusProgressSeen)
		}
		r.statusProgressSeen[key] = msg
	}
	r.printProgress(msg)
}

func (r *EventRenderer) printProgress(msg string) {
	r.endBlock()
	if r.isCI {
		fmt.Fprintf(os.Stderr, "::notice::%s\n", msg)
	}
	r.printer.ToolProgress(msg)
}
