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
		{"export -p flag is skipped", []string{"export -p BAR"}, []string{"BAR"}},
		{"export -n does not export", []string{"export -n FOO"}, []string{}},
		{"export -n retracts an earlier export", []string{"export FOO=bar; export -n FOO"}, []string{}},
		{"export -n with a value retracts", []string{"export FOO=1", "export -n FOO=2"}, []string{}},
		{"combined flags with n retract", []string{"export A B; export -pn A"}, []string{"B"}},
		{"export after export -n exports", []string{"export -n FOO; export FOO=1"}, []string{"FOO"}},
		{"export -n removes only its operands", []string{"export A B; export -n A"}, []string{"B"}},
		{"double dash ends export options", []string{"export -n -- A; export -- B"}, []string{"B"}},
		{"export -n keeps prefix assignments", []string{"FOO=1 cmd; export -n FOO"}, []string{"FOO"}},
		{"export -n inside sh -c", []string{`sh -c 'export X=1; export -n X'`}, []string{}},
		{"sh -c export -n does not reach the parent", []string{`export X=1; sh -c 'export -n X'`}, []string{"X"}},
		{"export -n after an external command keeps the name", []string{"export X=1; cmd; export -n X"}, []string{"X"}},
		{"export -n after a child shell keeps the name", []string{"export X=1; bash -c 'printenv X'; export -n X"}, []string{"X"}},
		{"export -n after a builtin still retracts", []string{"export X=1; echo hi; readonly Y=2; export -n X"}, []string{}},
		{"export -n after command export still retracts", []string{"export X=1; command export -n X"}, []string{}},
		{"export -fn leaves the variable exported", []string{"export X=1; export -fn X"}, []string{"X"}},
		{"export -f -n leaves the variable exported", []string{"export X=1; export -f -n X"}, []string{"X"}},
		{"plain export -f does not export a variable", []string{"export -f FUNC"}, []string{}},
		{"exec with no command launches nothing", []string{"export X=1; exec; export -n X"}, []string{}},
		{"exec with a command launches a process", []string{"export X=1; exec true; export -n X"}, []string{"X"}},
		{"export lone dash is an operand", []string{"export X=1; export - -n X"}, []string{"X"}},
		{"invalid export option is a no-op", []string{"export X=1; export -notvalid X"}, []string{"X"}},
		{"invalid export option does not export", []string{"export -xp X"}, []string{}},
		{"command -v launches nothing", []string{"export X=1; command -v bash; export -n X"}, []string{}},
		{"command -V launches nothing", []string{"export X=1; command -V bash; export -n X"}, []string{}},
		{"command with invalid option launches nothing", []string{"export X=1; command -z bash; export -n X"}, []string{}},
		{"command -p runs the command", []string{"export X=1; command -p cmd; export -n X"}, []string{"X"}},
		{"command -- runs the command", []string{"export X=1; command -- cmd; export -n X"}, []string{"X"}},
		{"command -p skips option words for export", []string{"export X=1; command -p export -n X"}, []string{}},
		{"command with only options runs nothing", []string{"export X=1; command -p; export -n X"}, []string{}},
		{"command after a lone dash runs it", []string{"export X=1; command - ; export -n X"}, []string{"X"}},
		{"builtin true launches nothing", []string{"export X=1; builtin true; export -n X"}, []string{}},
		{"builtin -- true launches nothing", []string{"export X=1; builtin -- true; export -n X"}, []string{}},
		{"builtin export -n retracts", []string{"export X=1; builtin export -n X"}, []string{}},
		{"builtin with a non-builtin launches nothing", []string{"export X=1; builtin cmd; export -n X"}, []string{}},
		{"builtin exec runs a process", []string{"export X=1; builtin exec cmd; export -n X"}, []string{"X"}},
		{"prefix assignment on builtin is not exported", []string{"Y=1 builtin true"}, []string{}},
		{"env cannot run the export builtin", []string{"export X=1; env export -n X"}, []string{"X"}},
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
