package runtime

import "crypto/sha256"

type statusProgressDisplay struct {
	message string
	subject string
	context string
}

// maxStatusProgressEntries bounds the status-event display cache. It shares
// the task-progress scale because both caches retain one row per active key.
const maxStatusProgressEntries = maxTaskProgressEntries

func (r *progressReconciler) status(e StatusProgressEvent, redact func(string) string) (string, bool) {
	if e.Kind == "" {
		return "", false
	}
	key := sha256.Sum256([]byte(e.Kind + "\x00" + e.ID))
	previous, keyPresent := r.statusProgressSeen[key]
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
	// Fields are redacted individually; the joins can reconstruct a
	// credential (a tab-containing secret normalizes to the exact text a
	// label/subject join produces). The caller's callback runs first: it
	// owns literals the fallback scanner cannot know, such as
	// environment-derived credentials. Mask the joined row before dedup
	// comparison, retention and display. Retained subject/context stay
	// per-field masked; fragments appearing across separate rows are the
	// same exposure as printing the pieces separately and are accepted.
	if redact != nil {
		msg = redact(msg)
	}
	msg = scanProgressSecrets(msg)
	if !e.Occurrence && previous.message == msg {
		return "", false
	}
	if !e.Occurrence {
		if r.statusProgressSeen == nil {
			r.statusProgressSeen = make(map[[sha256.Size]byte]statusProgressDisplay)
		}
		// Only a new key needs a free slot: updating an existing key at
		// capacity must not discard the retained state of every other key.
		if !keyPresent && len(r.statusProgressSeen) >= maxStatusProgressEntries {
			clear(r.statusProgressSeen)
		}
		r.statusProgressSeen[key] = statusProgressDisplay{message: msg, subject: subject, context: context}
	}
	return msg, true
}
