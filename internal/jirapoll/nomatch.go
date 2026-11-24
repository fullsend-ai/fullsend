package jirapoll

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"

	"github.com/fullsend-ai/fullsend/internal/normevent"
)

// annotationWriter is where the poller writes GitHub Actions workflow
// command annotations (::warning::). It defaults to os.Stderr; tests
// replace it to capture output. An annotation must start a line, so it is
// written directly rather than through the log package's prefixed output.
var annotationWriter io.Writer = os.Stderr

// maxNoMatchEvents bounds how many unmatched events the no-match warning
// lists, keeping the single-line annotation readable on a busy cycle.
const maxNoMatchEvents = 10

// CandidateReporter is optionally implemented by an EventMatcher so the
// poller can name the agents it considered in its no-match warning.
type CandidateReporter interface {
	// DispatchCandidates returns the names of the agents the matcher
	// evaluates events against, and whether it drops every event on
	// purpose (an active kill switch), in which case no warning is due.
	DispatchCandidates() (agents []string, suppressed bool)
}

// unmatchedEvent is a validated event of the cycle for which the matcher
// returned no dispatch record.
type unmatchedEvent struct {
	issueKey string
	event    string
}

// annotationEscaper encodes the characters GitHub Actions treats specially
// in a workflow command message: '%' first (so a literal "%0A" in a value
// stays visible as text), then CR and LF. Breaking up "::" is done separately
// in sanitizeAnnotation, because a non-overlapping replacer leaves "::" in
// runs of three or more colons.
var annotationEscaper = strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A")

// sanitizeAnnotation makes a dynamic string safe to embed in a workflow
// command annotation: control characters (including ANSI escape
// sequences' ESC) are dropped, the characters annotationEscaper names are
// encoded, and "::" is broken up until none remains so a value cannot read
// as the start of another workflow command. Jira comment commands and label
// names are user-controlled.
func sanitizeAnnotation(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	s = annotationEscaper.Replace(s)
	for strings.Contains(s, "::") {
		s = strings.ReplaceAll(s, "::", ": :")
	}
	return s
}

// describeEvent summarizes a normalized event for the no-match warning: its
// transition kind and, where present, the comment's slash command or the
// label change.
func describeEvent(ne *normevent.Event) string {
	desc := string(ne.Transition.Kind)
	switch {
	case ne.Transition.Comment != nil && ne.Transition.Comment.Command != "":
		desc += " " + ne.Transition.Comment.Command
	case ne.Transition.Label != nil:
		desc += " " + ne.Transition.Label.Name + " " + ne.Transition.Label.Action
	}
	return desc
}

// warnNoMatch writes a ::warning:: annotation when the cycle selected
// issues and evaluated events on them, but no agent matched any event, so
// nothing was dispatched (#9503). It names each unmatched event (bounded by
// maxNoMatchEvents), the agents the matcher considered, and what to check.
// It writes nothing when the cycle dispatched work, when no event was
// evaluated (the selected issues had no new activity), or when the matcher
// drops every event on purpose.
func (p *Poller) warnNoMatch(selected int) {
	if selected == 0 || len(p.dispatches) > 0 || len(p.unmatched) == 0 {
		return
	}
	agentsClause := ""
	if r, ok := p.matcher.(CandidateReporter); ok {
		agents, suppressed := r.DispatchCandidates()
		if suppressed {
			return
		}
		considered := "none"
		if len(agents) > 0 {
			considered = sanitizeAnnotation(strings.Join(agents, ", "))
		}
		agentsClause = " Agents considered: " + considered + "."
	}
	events := make([]string, 0, min(len(p.unmatched), maxNoMatchEvents))
	for i, u := range p.unmatched {
		if i == maxNoMatchEvents {
			events = append(events, fmt.Sprintf("and %d more", len(p.unmatched)-maxNoMatchEvents))
			break
		}
		events = append(events, sanitizeAnnotation(u.issueKey+" ("+u.event+")"))
	}
	fmt.Fprintf(annotationWriter, "::warning::jira-poll: selected %d issue(s) but no agent matched any of their events, so nothing was dispatched: %s.%s "+
		"Check that the agent is not disabled under agents: in .fullsend/config.yaml, that its harness trigger matches the event, "+
		"and that the actor has the role the agent requires (see Actor role resolution in the Jira integration guide).\n",
		selected, strings.Join(events, ", "), agentsClause)
}
