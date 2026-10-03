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
		".fullsend":       "",
		"./.fullsend":     "",
		".fullsend/":      "",
		"sub/.fullsend":   " --fullsend-dir sub/.fullsend",
		"/abs/.fullsend":  " --fullsend-dir /abs/.fullsend",
		"../repo/.config": " --fullsend-dir ../repo/.config",
	}
	for in, want := range cases {
		if got := fullsendDirArg(in); got != want {
			t.Errorf("fullsendDirArg(%q) = %q, want %q", in, got, want)
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
		"--fullsend-dir " + dir + "\n",
		"fullsend run lint-docs --fullsend-dir " + dir + " \\\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "harness", "lint-docs.yaml")); err != nil {
		t.Errorf("harness not written: %v", err)
	}
}
