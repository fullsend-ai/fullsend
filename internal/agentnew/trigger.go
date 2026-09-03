package agentnew

import (
	"fmt"
	"strings"
)

// Trigger presets. The expansions below are reproduced from
// docs/guides/user/cel-triggers-reference.md rather than composed here: an
// expression that compiles but touches an absent optional field raises a
// missing-key error at dispatch time, and MatchHarnesses turns that into a
// red ::error:: annotation on every matching event
// (internal/harnessdispatch/enumerate.go:95-99). Emitting known-good text is
// the whole point of generating the trigger instead of asking the user to
// write one. TestTriggerPresetsArePinned asserts the exact strings.
const (
	// PresetCommand fires on a slash command. The change_proposal clause
	// uses has() rather than the reference doc's `!= null` because
	// change_proposal is ABSENT from state on a non-PR comment (schema
	// $defs.state requires only "labels"; see the jira-fs-triage-comment
	// and discussion-fs-vouch-comment fixtures), so `!= null` raises a
	// missing-key error on every issue comment. The guard refuses fork
	// pull requests without excluding plain issues.
	PresetCommand = "command"
	// PresetLabel fires when a label is added.
	PresetLabel = "label"
	// PresetIssueOpened fires on new work items.
	PresetIssueOpened = "issue-opened"
	// PresetPROpened fires when a non-fork pull request is opened or updated.
	PresetPROpened = "pr-opened"
)

// PresetNames lists the recognised --on presets in help order.
func PresetNames() []string {
	return []string{
		PresetCommand + ":/<command>",
		PresetLabel + ":<label>",
		PresetIssueOpened,
		PresetPROpened,
	}
}

// ExpandTrigger turns an --on preset into its CEL expression. name supplies
// the default command and label. The returned expression is not compiled
// here; the caller runs harness.ValidateTriggerExpression on it before any
// file is written.
func ExpandTrigger(on, name string) (string, error) {
	kind, arg, hasArg := strings.Cut(on, ":")
	switch kind {
	case PresetCommand:
		command := "/fs-" + name
		if hasArg && arg != "" {
			command = arg
			if !strings.HasPrefix(command, "/") {
				command = "/" + command
			}
		}
		if err := validCELStringLiteral(command); err != nil {
			return "", fmt.Errorf("--on %s: %w", on, err)
		}
		return fmt.Sprintf(`event.transition.kind == "comment_added"
  && has(event.transition.comment.command)
  && event.transition.comment.command == %q
  && (!has(event.state.change_proposal) || !event.state.change_proposal.is_fork)
`, command), nil

	case PresetLabel:
		label := name
		if hasArg && arg != "" {
			label = arg
		}
		if err := validCELStringLiteral(label); err != nil {
			return "", fmt.Errorf("--on %s: %w", on, err)
		}
		return fmt.Sprintf(`event.transition.kind == "label_changed"
  && event.transition.label.name == %q
  && event.transition.label.action == "added"
`, label), nil

	case PresetIssueOpened:
		if hasArg {
			return "", fmt.Errorf("--on %s takes no argument", PresetIssueOpened)
		}
		return `event.entity.kind == "work_item"
  && event.transition.kind == "opened"
`, nil

	case PresetPROpened:
		if hasArg {
			return "", fmt.Errorf("--on %s takes no argument", PresetPROpened)
		}
		return `event.entity.kind == "change_proposal"
  && (event.transition.kind == "opened"
      || event.transition.kind == "synchronized"
      || event.transition.kind == "marked_ready")
  && !event.state.change_proposal.is_fork
`, nil
	}

	return "", fmt.Errorf("unknown --on preset %q\n\nvalid presets: %s\nfor anything else pass raw CEL with --trigger",
		on, strings.Join(PresetNames(), ", "))
}

// validCELStringLiteral rejects values that would need escaping inside the
// generated CEL string literal. %q would escape them correctly, but a
// command or label containing a quote or newline is a mistake worth
// reporting rather than encoding.
func validCELStringLiteral(s string) error {
	if s == "" {
		return fmt.Errorf("value must not be empty")
	}
	if strings.ContainsAny(s, "\"'\\\n\r\t") {
		return fmt.Errorf("value %q must not contain quotes, backslashes or whitespace control characters", s)
	}
	return nil
}
