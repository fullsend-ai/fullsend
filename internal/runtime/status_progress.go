package runtime

import "crypto/sha256"

type statusProgressDisplay struct {
	message string
	subject string
	context string
}

func (r *progressReconciler) status(e StatusProgressEvent, redact func(string) string) (string, bool) {
	if e.Kind == "" {
		return "", false
	}
	key := sha256.Sum256([]byte(e.Kind + "\x00" + e.ID))
	previous := r.statusProgressSeen[key]
	if e.Clear {
		delete(r.statusProgressSeen, key)
		return "", false
	}
	if e.Recovery && previous.message == "" {
		return "", false
	}
	msg := redactProgressText(e.Label, 120, redact)
	if msg == "" {
		return "", false
	}
	subject := redactProgressText(e.Subject, 120, redact)
	context := redactProgressText(e.Context, 80, redact)
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
	if state := redactProgressText(e.State, 60, redact); state != "" {
		msg += " — " + state
	}
	if detail := redactProgressText(e.Detail, 240, redact); detail != "" {
		msg += ": " + detail
	}
	msg = sanitizeOutput(msg)
	if !e.Occurrence && previous.message == msg {
		return "", false
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
	return msg, true
}
