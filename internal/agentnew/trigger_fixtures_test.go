package agentnew

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/harness"
)

// fixtureDir holds the normative NormalizedEvent v1 examples. Evaluating the
// generated presets against these, rather than against hand-written maps,
// means the test fails if the event shape the presets depend on ever moves.
const fixtureDir = "../../docs/normative/normalized-event/v1/examples"

func loadFixture(t *testing.T, name string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixtureDir, name+".json"))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	var event map[string]any
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatalf("parsing fixture %s: %v", name, err)
	}
	return event
}

// forkVariant returns a fixture with state.change_proposal.is_fork flipped.
// There is no fork pull-request COMMENT fixture in the corpus, so the fork
// case is derived from the real non-fork one rather than hand-written.
func forkVariant(t *testing.T, event map[string]any) map[string]any {
	t.Helper()
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var clone map[string]any
	if err := json.Unmarshal(data, &clone); err != nil {
		t.Fatal(err)
	}
	cp, ok := clone["state"].(map[string]any)["change_proposal"].(map[string]any)
	if !ok {
		t.Fatal("fixture has no state.change_proposal to flip")
	}
	cp["is_fork"] = true
	return clone
}

// TestCommandPresetForkGuard is the security-critical test of this feature.
// The command preset is the DEFAULT trigger, so every generated agent gets
// it, including agents with role coder (contents:write, pull_requests:write).
// It must fire for a maintainer on a non-fork PR and for an issue comment,
// and must NOT fire for a comment on a fork pull request.
func TestCommandPresetForkGuard(t *testing.T) {
	prComment := loadFixture(t, "fs-fix-comment")            // PR comment, is_fork false
	issueComment := loadFixture(t, "jira-fs-triage-comment") // issue comment, no change_proposal
	forkComment := forkVariant(t, prComment)

	tests := []struct {
		name    string
		command string
		event   map[string]any
		want    bool
	}{
		{"non-fork pull request comment fires", "/fs-fix", prComment, true},
		{"issue comment fires (no change_proposal present)", "/fs-triage", issueComment, true},
		{"fork pull request comment does NOT fire", "/fs-fix", forkComment, false},
		{"different command does not fire", "/fs-other", prComment, false},
		{"different command does not fire on issue", "/fs-other", issueComment, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			expr, err := ExpandTrigger("command:"+tc.command, "irrelevant")
			if err != nil {
				t.Fatal(err)
			}
			got, err := harness.EvaluateTrigger(expr, tc.event)
			// An error here is the failure this preset exists to avoid: a
			// missing-key error becomes a red ::error:: annotation on every
			// matching event, because MatchHarnesses logs and skips.
			if err != nil {
				t.Fatalf("trigger evaluation errored (this would be ::error:: spam at dispatch): %v", err)
			}
			if got != tc.want {
				t.Errorf("EvaluateTrigger = %v, want %v\nexpr:\n%s", got, tc.want, expr)
			}
		})
	}
}

// TestCommandPresetNeverErrorsOnAnyFixture: the whole point of the has()
// guard over the reference doc's `!= null` is that it must not raise a
// missing-key error on ANY event shape, including ones with no
// change_proposal and no comment at all.
func TestCommandPresetNeverErrorsOnAnyFixture(t *testing.T) {
	entries, err := os.ReadDir(fixtureDir)
	if err != nil {
		t.Fatal(err)
	}
	expr, err := ExpandTrigger("command:/fs-fix", "irrelevant")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		name := e.Name()[:len(e.Name())-len(".json")]
		t.Run(name, func(t *testing.T) {
			if _, err := harness.EvaluateTrigger(expr, loadFixture(t, name)); err != nil {
				t.Errorf("preset errored on %s: %v", name, err)
			}
		})
	}
}

// TestPROpenedPresetForkGuard: the pr-opened preset carries the reference
// doc's fork guard verbatim, and change_proposal is always present on a
// change_proposal event, so `!has()` is not needed there.
func TestPROpenedPresetForkGuard(t *testing.T) {
	expr, err := ExpandTrigger("pr-opened", "irrelevant")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		fixture string
		want    bool
	}{
		{"pr-opened", true},
		{"pr-opened-fork", false},
		{"pr-synchronized", true},
		{"issue-opened", false},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			got, err := harness.EvaluateTrigger(expr, loadFixture(t, tc.fixture))
			if err != nil {
				t.Fatalf("evaluation errored: %v", err)
			}
			if got != tc.want {
				t.Errorf("EvaluateTrigger(%s) = %v, want %v", tc.fixture, got, tc.want)
			}
		})
	}
}

// TestIssueOpenedPreset against the real fixture.
func TestIssueOpenedPreset(t *testing.T) {
	expr, err := ExpandTrigger("issue-opened", "irrelevant")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		fixture string
		want    bool
	}{
		{"issue-opened", true},
		{"pr-opened", false},
	} {
		got, err := harness.EvaluateTrigger(expr, loadFixture(t, tc.fixture))
		if err != nil {
			t.Fatalf("%s: %v", tc.fixture, err)
		}
		if got != tc.want {
			t.Errorf("EvaluateTrigger(%s) = %v, want %v", tc.fixture, got, tc.want)
		}
	}
}

// TestLabelPreset against the real fixture.
func TestLabelPreset(t *testing.T) {
	expr, err := ExpandTrigger("label:ready-to-code", "irrelevant")
	if err != nil {
		t.Fatal(err)
	}
	got, err := harness.EvaluateTrigger(expr, loadFixture(t, "ready-to-code-labeled"))
	if err != nil {
		t.Fatalf("evaluation errored: %v", err)
	}
	if !got {
		t.Error("label preset did not match the ready-to-code-labeled fixture")
	}
}

// TestCommandPresetMatchesTheReferenceDoc keeps the generator and the
// documentation from drifting apart. The reference page tells users to write
// this expression by hand; `agent new --on command:` emits it for them. If
// either changes without the other, this fails.
func TestCommandPresetMatchesTheReferenceDoc(t *testing.T) {
	const docPath = "../../docs/guides/user/cel-triggers-reference.md"
	data, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatal(err)
	}

	const heading = "**Run on a slash command (issues and non-fork PRs):**"
	idx := strings.Index(string(data), heading)
	if idx < 0 {
		t.Fatalf("could not find the slash-command section in %s", docPath)
	}
	block, ok := firstYAMLBlock(string(data)[idx:])
	if !ok {
		t.Fatalf("could not find a yaml block after %q", heading)
	}

	// The doc uses a folded scalar (trigger: >) and two-space continuation;
	// compare the expression's significant tokens, not its layout.
	docExpr := strings.TrimPrefix(strings.TrimSpace(block), "trigger: >")
	preset, err := ExpandTrigger("command:/my-command", "irrelevant")
	if err != nil {
		t.Fatal(err)
	}
	if normaliseCEL(docExpr) != normaliseCEL(preset) {
		t.Errorf("the reference doc and the --on command: preset have drifted\n doc: %s\npreset: %s",
			normaliseCEL(docExpr), normaliseCEL(preset))
	}
}

func firstYAMLBlock(s string) (string, bool) {
	start := strings.Index(s, "```yaml\n")
	if start < 0 {
		return "", false
	}
	rest := s[start+len("```yaml\n"):]
	end := strings.Index(rest, "```")
	if end < 0 {
		return "", false
	}
	return rest[:end], true
}

// normaliseCEL collapses all whitespace so layout differences between a
// folded YAML scalar and generated text do not matter.
func normaliseCEL(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
