package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// fullsendDirCommands lists every command that takes --fullsend-dir with the
// .fullsend default, with arguments that pass its Args validator so the
// PreRunE check is what decides the outcome.
func fullsendDirCommands() map[string]struct {
	build func() *cobra.Command
	args  []string
} {
	return map[string]struct {
		build func() *cobra.Command
		args  []string
	}{
		"agent new":    {newAgentNewCmd, []string{"lint-docs"}},
		"agent add":    {newAgentAddCmd, []string{"harness/lint.yaml"}},
		"agent list":   {newAgentListCmd, nil},
		"agent update": {newAgentUpdateCmd, []string{"triage"}},
		"agent remove": {newAgentRemoveCmd, []string{"triage"}},
		"agent set":    {newAgentSetCmd, []string{"triage", "--model", "sonnet"}},
		"lock":         {newLockCmd, []string{"triage"}},
		"run":          {newRunCmd, []string{"triage", "--target-repo", "."}},
		"lint":         {newLintCmd, nil},
	}
}

func executeCmd(cmd *cobra.Command, args []string) (string, error) {
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	err := cmd.Execute()
	return buf.String(), err
}

func TestFullsendDirFlagDefaultsAndIsOptional(t *testing.T) {
	for name, c := range fullsendDirCommands() {
		t.Run(name, func(t *testing.T) {
			flag := c.build().Flags().Lookup("fullsend-dir")
			if flag == nil {
				t.Fatal("--fullsend-dir is not registered")
			}
			if flag.DefValue != defaultFullsendDir {
				t.Errorf("--fullsend-dir default = %q, want %q", flag.DefValue, defaultFullsendDir)
			}
			if _, required := flag.Annotations[cobra.BashCompOneRequiredFlag]; required {
				t.Error("--fullsend-dir is still marked required")
			}
		})
	}
}

// TestFullsendDirMissingDefaultNamesFlag runs every command from a directory
// without .fullsend: the error must say so and name the flag, instead of
// surfacing as a failure to read a file inside the missing directory.
func TestFullsendDirMissingDefaultNamesFlag(t *testing.T) {
	t.Chdir(t.TempDir())
	for name, c := range fullsendDirCommands() {
		t.Run(name, func(t *testing.T) {
			_, err := executeCmd(c.build(), c.args)
			if err == nil {
				t.Fatal("expected an error when .fullsend does not exist")
			}
			for _, want := range []string{"no .fullsend directory", "--fullsend-dir"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
		})
	}
}

// TestAgentListUsesDefaultFullsendDir checks that omitting the flag behaves
// like passing --fullsend-dir .fullsend from the repository root.
func TestAgentListUsesDefaultFullsendDir(t *testing.T) {
	dir := newFullsendDir(t)
	t.Chdir(filepath.Dir(dir))
	if _, err := executeCmd(newAgentListCmd(), nil); err != nil {
		t.Fatalf("agent list without --fullsend-dir: %v", err)
	}
}

// TestAgentListExplicitFullsendDirOverridesDefault checks that an explicit
// --fullsend-dir is used even when the default directory is absent.
func TestAgentListExplicitFullsendDirOverridesDefault(t *testing.T) {
	dir := newFullsendDir(t)
	t.Chdir(t.TempDir())
	if _, err := executeCmd(newAgentListCmd(), []string{"--fullsend-dir", dir}); err != nil {
		t.Fatalf("agent list --fullsend-dir %s: %v", dir, err)
	}
}

// TestCheckDefaultFullsendDirLeavesExplicitPathToCommand: a missing explicit
// path is reported by the command's own checks, which name the given path.
func TestCheckDefaultFullsendDirLeavesExplicitPathToCommand(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope")
	if err := checkDefaultFullsendDir(true, missing); err != nil {
		t.Errorf("explicit path: got %v, want nil", err)
	}
	if err := checkDefaultFullsendDir(false, missing); err == nil {
		t.Error("default path that does not exist: got nil, want an error")
	}
	existing := t.TempDir()
	if err := checkDefaultFullsendDir(false, existing); err != nil {
		t.Errorf("default path that exists: got %v, want nil", err)
	}
}

func TestFullsendDirArg(t *testing.T) {
	cases := map[string]string{
		".fullsend":                            "",
		"./.fullsend":                          "",
		".fullsend/":                           "",
		"sub/.fullsend":                        " --fullsend-dir 'sub/.fullsend'",
		"/abs/.fullsend":                       " --fullsend-dir '/abs/.fullsend'",
		"../repo/.config":                      " --fullsend-dir '../repo/.config'",
		"/Users/me/My Projects/repo/.fullsend": " --fullsend-dir '/Users/me/My Projects/repo/.fullsend'",
		"/dir/it's/.fullsend":                  " --fullsend-dir '/dir/it'\\''s/.fullsend'",
	}
	for in, want := range cases {
		if got := fullsendDirArg(in); got != want {
			t.Errorf("fullsendDirArg(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestShellQuoteArg pins both branches of the command-hint argument quoter:
// shell-safe names stay bare, and a name with a space or an embedded quote is
// single-quoted with the quote escaped, so a pasted hint stays one argument.
func TestShellQuoteArg(t *testing.T) {
	cases := map[string]string{
		"code":     "code",
		"my agent": "'my agent'",
		"it's":     "'it'\\''s'",
	}
	for in, want := range cases {
		if got := shellQuoteArg(in); got != want {
			t.Errorf("shellQuoteArg(%q) = %q, want %q", in, got, want)
		}
	}
}

// assertNoControlChars fails when s carries a control character that could
// forge log lines or alter how a printed hint looks.
func assertNoControlChars(t *testing.T, s string) {
	t.Helper()
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			t.Errorf("printed hint %q carries control character %q", s, r)
		}
	}
}

// TestShellQuoteArgEscapesControlCharacters: a crafted agent name with
// newlines, carriage returns or ANSI escapes must not pass them through to
// the printed hint — they are rendered as visible backslash sequences
// inside the quotes, so the hint cannot forge log lines or alter how the
// command looks.
func TestShellQuoteArgEscapesControlCharacters(t *testing.T) {
	cases := map[string]string{
		"a\nb":       `'a\nb'`,
		"a\rb":       `'a\rb'`,
		"a\x1b[31mb": `'a\e[31mb'`,
		"a\x00b":     `'a\x00b'`,
		"a\x7fb":     `'a\x7fb'`,
		"a\u009bb":   `'a\x9bb'`,
		"a'b\nc":     `'a'\''b\nc'`,
		"café":       `'café'`,
	}
	for in, want := range cases {
		got := shellQuoteArg(in)
		if got != want {
			t.Errorf("shellQuoteArg(%q) = %q, want %q", in, got, want)
		}
		assertNoControlChars(t, got)
	}
}

// TestShellQuoteArgQuotesLeadingDash: a name that starts with '-' is never
// printed bare, so it cannot read as a flag in the hint.
func TestShellQuoteArgQuotesLeadingDash(t *testing.T) {
	cases := map[string]string{
		"-all":           "'-all'",
		"-x/y":           "'-x/y'",
		"--fullsend-dir": "'--fullsend-dir'",
	}
	for in, want := range cases {
		if got := shellQuoteArg(in); got != want {
			t.Errorf("shellQuoteArg(%q) = %q, want %q", in, got, want)
		}
	}
	// a dash anywhere but the first rune is still shell-safe and stays bare
	if got := shellQuoteArg("code-review"); got != "code-review" {
		t.Errorf("shellQuoteArg(\"code-review\") = %q, want it bare", got)
	}
}

// TestFullsendDirArgEscapesControlCharacters: a crafted --fullsend-dir value
// gets the same treatment as an agent name — quotes escaped, control
// characters visible — so it cannot forge the printed hint either.
func TestFullsendDirArgEscapesControlCharacters(t *testing.T) {
	got := fullsendDirArg("/dir\nwith-esc\x1b[2k/.fullsend")
	if want := " --fullsend-dir '/dir\\nwith-esc\\e[2k/.fullsend'"; got != want {
		t.Errorf("fullsendDirArg() = %q, want %q", got, want)
	}
	assertNoControlChars(t, got)
}

// TestLockUpdateHintGuardsLeadingDashName: a name that starts with '-' is
// printed behind the "--" end-of-options marker with every flag before it,
// because the CLI's own flag parser would read a quoted "-x" as a flag
// anyway; a normal name keeps the short form users type.
func TestLockUpdateHintGuardsLeadingDashName(t *testing.T) {
	cases := []struct {
		name, dir, want string
	}{
		{"code", ".fullsend", "fullsend lock code --update"},
		{"-all", ".fullsend", "fullsend lock --update -- '-all'"},
		{"-all", "/tmp/d", "fullsend lock --update --fullsend-dir '/tmp/d' -- '-all'"},
	}
	for _, c := range cases {
		if got := lockUpdateHint(c.name, c.dir, "", "", true); got != c.want {
			t.Errorf("lockUpdateHint(%q, %q, true) = %q, want %q", c.name, c.dir, got, c.want)
		}
	}
}

// TestLockUpdateHintOmitsUpdate: the stale-lock advice carries no --update —
// a plain lock already re-composes a stale entry from the URL index's cached
// copies, and --update would set BypassURLIndex and re-fetch every un-pinned
// relative base file from upstream.
func TestLockUpdateHintOmitsUpdate(t *testing.T) {
	cases := []struct {
		name, dir, want string
	}{
		{"code", ".fullsend", "fullsend lock code"},
		{"code", "/tmp/d", "fullsend lock code --fullsend-dir '/tmp/d'"},
		{"-all", ".fullsend", "fullsend lock -- '-all'"},
		{"-all", "/tmp/d", "fullsend lock --fullsend-dir '/tmp/d' -- '-all'"},
	}
	for _, c := range cases {
		if got := lockUpdateHint(c.name, c.dir, "", "", false); got != c.want {
			t.Errorf("lockUpdateHint(%q, %q, false) = %q, want %q", c.name, c.dir, got, c.want)
		}
	}
}

// TestLockUpdateArgsGuardsLeadingDashName: run's unpinned-dependencies hint
// prints the flags before the name; a leading-dash name goes behind "--"
// there too, with the --fullsend-dir flag moved before the guard so it
// still parses as a flag.
func TestLockUpdateArgsGuardsLeadingDashName(t *testing.T) {
	cases := []struct {
		name, dir, want string
	}{
		{"code", ".fullsend", "--update code"},
		{"code", "/tmp/d", "--update code --fullsend-dir '/tmp/d'"},
		{"-all", ".fullsend", "--update -- '-all'"},
		{"-all", "/tmp/d", "--update --fullsend-dir '/tmp/d' -- '-all'"},
	}
	for _, c := range cases {
		if got := lockUpdateArgs(c.name, c.dir, "", ""); got != c.want {
			t.Errorf("lockUpdateArgs(%q, %q) = %q, want %q", c.name, c.dir, got, c.want)
		}
	}
}

// TestAgentNewNextStepsOmitDefaultFullsendDir: with the default directory the
// printed commands are the short form users type, without --fullsend-dir.
func TestAgentNewNextStepsOmitDefaultFullsendDir(t *testing.T) {
	dir := newFullsendDir(t)
	t.Chdir(filepath.Dir(dir))
	f := defaultFlags(defaultFullsendDir, "runtime", "no-register")
	f.runtime = "pi"
	f.noRegister = true
	out, err := runNew(t, "lint-docs", f)
	if err != nil {
		t.Fatalf("runAgentNew: %v\n%s", err, out)
	}
	if strings.Contains(out, "--fullsend-dir") {
		t.Errorf("next steps should omit the default --fullsend-dir:\n%s", out)
	}
	for _, want := range []string{
		"fullsend agent add harness/lint-docs.yaml\n",
		"fullsend agent set lint-docs --runtime pi\n",
		"fullsend run lint-docs \\\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// TestAgentNewNextStepsKeepNonDefaultFullsendDir: a non-default directory is
// carried into every printed command so it can be pasted as-is.
func TestAgentNewNextStepsKeepNonDefaultFullsendDir(t *testing.T) {
	dir := newFullsendDir(t)
	f := defaultFlags(dir, "no-register")
	f.noRegister = true
	out, err := runNew(t, "lint-docs", f)
	if err != nil {
		t.Fatalf("runAgentNew: %v\n%s", err, out)
	}
	for _, want := range []string{
		"--fullsend-dir '" + dir + "'\n",
		"fullsend run lint-docs --fullsend-dir '" + dir + "' \\\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "harness", "lint-docs.yaml")); err != nil {
		t.Errorf("harness not written: %v", err)
	}
}

// TestUnpinnedDepsWarningEscapesControlCharacters: the unpinned-dependencies
// warning's prose interpolates the agent name and first dependency, so a
// crafted name with a newline or ANSI escape must not pass through — the
// whole warning renders them as visible backslash sequences (the command
// part already escapes its own arguments), so the line cannot be forged.
func TestUnpinnedDepsWarningEscapesControlCharacters(t *testing.T) {
	w := unpinnedDepsWarning("agent\x1b[31m", []string{"https://example.com/a\x00b", "https://example.com/c"}, "", "/harness/code.yaml", "")
	for _, want := range []string{
		"Lock entry for agent\\e[31m does not pin 2 dependencies (https://example.com/a\\x00b…)",
		"run fullsend lock --update 'agent\\e[31m'",
	} {
		if !strings.Contains(w, want) {
			t.Errorf("warning missing %q:\n%s", want, w)
		}
	}
	assertNoControlChars(t, w)
}

// TestUnpinnedDepsWarningGuardsLeadingDashName: a name that starts with '-'
// is guarded behind "--" in the command part of the warning, exactly as the
// other lock-update hints guard it.
func TestUnpinnedDepsWarningGuardsLeadingDashName(t *testing.T) {
	w := unpinnedDepsWarning("--all.yaml", []string{"https://example.com/a"}, "", "/harness/code.yaml", "")
	if !strings.Contains(w, "run fullsend lock --update --fullsend-dir '' -- '--all.yaml'") {
		t.Errorf("warning does not guard the leading-dash name:\n%s", w)
	}
}
