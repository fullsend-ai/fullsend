package runtime

import "crypto/sha256"

type statusProgressDisplay struct {
	message string
	subject string
	context string
}

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
	if e.Recovery && previous.message == "" {
		return
	}
	msg := r.progressText(e.Label, 120)
	if msg == "" {
		return
	}
	subject := r.progressText(e.Subject, 120)
	context := r.progressText(e.Context, 80)
	if subject == "" && !e.SubjectSet {
		subject = previous.subject
	}
	if context == "" && !e.ContextSet {
		context = previous.context
	}
	if subject != "" {
		msg += " " + subject
	}
	if context != "" {
		msg += " [" + context + "]"
	}
	if state := r.progressText(e.State, 60); state != "" {
		msg += " — " + state
	}
	if detail := r.progressText(e.Detail, 240); detail != "" {
		msg += ": " + detail
	}
	msg = sanitizeOutput(msg)
	if !e.Occurrence && previous.message == msg {
		return
	}
	if !e.Occurrence {
		if r.statusProgressSeen == nil {
			r.statusProgressSeen = make(map[[sha256.Size]byte]statusProgressDisplay)
		}
		if len(r.statusProgressSeen) >= maxTaskProgressEntries {
			clear(r.statusProgressSeen)
		}
		r.statusProgressSeen[key] = statusProgressDisplay{message: msg, subject: subject, context: context}
	}
	r.printProgress(msg)
}
