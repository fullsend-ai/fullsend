package agentnew

import (
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

// TestTriggerPresetsArePinned asserts the exact text each preset emits.
// String equality, not a compile check: an expression can compile and still
// touch an absent optional field, which fails only at dispatch. That is the
// failure mode this command exists to remove, so the text is pinned.
func TestTriggerPresetsArePinned(t *testing.T) {
	tests := []struct {
		name string
		on   string
		want string
	}{
		{
			name: "command default",
			on:   "command",
			want: `event.transition.kind == "comment_added"
  && event.entity.kind == "work_item"
  && has(event.transition.comment.command)
  && event.transition.comment.command == "/fs-lint-docs"
  && (!has(event.state.change_proposal) || !event.state.change_proposal.is_fork)
`,
		},
		{
			name: "command explicit",
			on:   "command:/review-docs",
			want: `event.transition.kind == "comment_added"
  && event.entity.kind == "work_item"
  && has(event.transition.comment.command)
  && event.transition.comment.command == "/review-docs"
  && (!has(event.state.change_proposal) || !event.state.change_proposal.is_fork)
`,
		},
		{
			name: "command without leading slash is normalised",
			on:   "command:review-docs",
			want: `event.transition.kind == "comment_added"
  && event.entity.kind == "work_item"
  && has(event.transition.comment.command)
  && event.transition.comment.command == "/review-docs"
  && (!has(event.state.change_proposal) || !event.state.change_proposal.is_fork)
`,
		},
		{
			name: "label default",
			on:   "label",
			want: `event.transition.kind == "label_changed"
  && event.transition.label.name == "lint-docs"
  && event.transition.label.action == "added"
`,
		},
		{
			name: "label explicit",
			on:   "label:needs-docs",
			want: `event.transition.kind == "label_changed"
  && event.transition.label.name == "needs-docs"
  && event.transition.label.action == "added"
`,
		},
		{
			name: "issue-opened",
			on:   "issue-opened",
			want: `event.entity.kind == "work_item"
  && event.transition.kind == "opened"
`,
		},
		{
			name: "pr-opened",
			on:   "pr-opened",
			want: `event.entity.kind == "change_proposal"
  && (event.transition.kind == "opened"
      || event.transition.kind == "synchronized"
      || event.transition.kind == "marked_ready")
  && !event.state.change_proposal.is_fork
`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ExpandTrigger(tc.on, "lint-docs")
			if err != nil {
				t.Fatalf("ExpandTrigger(%q): %v", tc.on, err)
			}
			if got != tc.want {
				t.Errorf("expansion drifted\n got: %q\nwant: %q", got, tc.want)
			}
			// Every preset must also survive the same check the harness
			// loader applies, including the bool-output-type assertion.
			if err := harness.ValidateTriggerExpression(got); err != nil {
				t.Errorf("preset does not compile as a harness trigger: %v", err)
			}
		})
	}
}

func TestExpandTriggerRejects(t *testing.T) {
	for _, tc := range []struct{ name, on string }{
		{"unknown preset", "on-tuesday"},
		{"empty", ""},
		{"issue-opened with argument", "issue-opened:x"},
		{"pr-opened with argument", "pr-opened:x"},
		{"command with quote", `command:/a"b`},
		{"label with backslash", `label:a\b`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ExpandTrigger(tc.on, "lint-docs"); err == nil {
				t.Fatalf("ExpandTrigger(%q) succeeded; want error", tc.on)
			}
		})
	}
}

// TestUnknownPresetErrorListsAlternatives keeps the error actionable.
func TestUnknownPresetErrorListsAlternatives(t *testing.T) {
	_, err := ExpandTrigger("on-tuesday", "x")
	if err == nil {
		t.Fatal("want error")
	}
	for _, want := range []string{"command", "label", "issue-opened", "pr-opened", "--trigger"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q, got: %v", want, err)
		}
	}
}

// TestRoleResourcesExistInScaffold proves every provider and profile path the
// role table names is a file the CLI can actually produce. A path that names
// nothing would pass harness validation and then fail at run time as "agent
// crashes at 0s", because the embedded provider fallback covers only the
// OpenAI provider.
func TestRoleResourcesExistInScaffold(t *testing.T) {
	for _, name := range RoleNames() {
		role, err := LookupRole(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range append(append([]string{}, role.Providers...), role.Profiles...) {
			if _, err := scaffold.FullsendRepoFile(p); err != nil {
				t.Errorf("role %q references %q, which is not in the embedded scaffold: %v", name, p, err)
			}
		}
	}
}
