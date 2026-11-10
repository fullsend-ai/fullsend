package envmanifest

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExportedNames(t *testing.T) {
	tests := []struct {
		name    string
		scripts []string
		want    []string
	}{
		{"export with value", []string{"export FOO=bar"}, []string{"FOO"}},
		{"bare re-export", []string{"export OPENCODE_CONFIG_CONTENT"}, []string{"OPENCODE_CONFIG_CONTENT"}},
		{"several names in one export", []string{"export A=1 B C=3"}, []string{"A", "B", "C"}},
		{"export flags are skipped", []string{"export -n FOO"}, []string{"FOO"}},
		{"plain assignment is shell-local", []string{"FOO=bar"}, []string{}},
		{"readonly assignment is shell-local", []string{"readonly FOO=bar"}, []string{}},
		{"dropping export drops the name", []string{"PI_OFFLINE=1 && export PI_TELEMETRY=0"}, []string{"PI_TELEMETRY"}},
		{"assignment text inside a quoted value", []string{`export FULLSEND_OUTPUT_FILE='NAME=value'`}, []string{"FULLSEND_OUTPUT_FILE"}},
		{"assignment text inside double quotes", []string{`echo "PHANTOM=1" && export REAL=1`}, []string{"REAL"}},
		{"quoted name is not an assignment", []string{`'FOO'=bar cmd`}, []string{}},
		{"prefix assignment reaches the command", []string{`HOME="$X" PATH="$Y" opencode run`}, []string{"HOME", "PATH"}},
		{"prefix assignment on a builtin does not", []string{"IFS= read -r line"}, []string{}},
		{"env command assignments", []string{"env -i A=1 B=2 node app.js"}, []string{"A", "B"}},
		{"exec passes prefix through", []string{"exec env A=1 node"}, []string{"A"}},
		{"sh -c script is parsed", []string{`sh -c 'export INNER=1 && run'`}, []string{"INNER"}},
		{"bash -lc script is parsed", []string{`bash -lc "export INNER=1"`}, []string{"INNER"}},
		{"command substitution does not leak", []string{`X="$(export SUB=1; echo "$SUB")" && export OUTER=1`}, []string{"OUTER"}},
		{"separators split commands", []string{"export A=1; export B=2 && export C=3 || export D=4 | cat"}, []string{"A", "B", "C", "D"}},
		{"newlines split commands", []string{"export A=1\nB=2\nexport C=3\n"}, []string{"A", "C"}},
		{"comments are ignored", []string{"# export NOPE=1\nexport YES=1"}, []string{"YES"}},
		{"brace group", []string{"{ export A=1; } && ( export B=2 )"}, []string{"A", "B"}},
		{"escaped quote stays in the value", []string{`export A="x\"B=1"`}, []string{"A"}},
		{"dedupes and sorts across scripts", []string{"export Z=1", "export A=1 Z=2"}, []string{"A", "Z"}},
		{"invalid names are ignored", []string{"export 1BAD=x -flag"}, []string{}},
		{"unterminated quote does not panic", []string{`export A="x`, `export B='y`, `X="$(unterminated`}, []string{"A", "B"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ExportedNames(tt.scripts...))
		})
	}
}
