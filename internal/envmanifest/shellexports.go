package envmanifest

import (
	"regexp"
	"slices"
	"strings"
)

// envNameRe is the POSIX shell variable-name grammar.
var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ExportedNames returns the sorted, distinct variable names the shell
// scripts place in a child process's environment. It recognizes three forms:
//
//   - export statements, both "export NAME=value" and the bare re-export
//     "export NAME";
//   - prefix assignments on a command ("NAME=value cmd" and
//     "env NAME=value cmd"), which the command's process inherits;
//   - the same forms inside the script argument of "sh -c" or "bash -c",
//     parsed recursively.
//
// A plain assignment with no command ("NAME=value" alone, or "readonly
// NAME=value") is shell-local and is not reported, nor is a prefix
// assignment on a shell builtin ("IFS= read"). "export -n NAME" removes
// the export attribute, so a name the scripts un-export after exporting
// it is not reported; scripts are read in order as one shell, and an
// "sh -c" script is its own shell whose un-exports do not reach its
// parent. Prefix assignments affect only their command's process, and a
// name exported when a process launches stays in that process's
// environment, so a later "export -n" does not retract either. "export
// -f" works on functions, so "export -fn NAME" leaves variable NAME
// exported. Parentheses are only separators, so an "export -n" inside a
// ( ... ) subshell is treated as if it ran in the parent shell.
// Assignment-shaped text
// inside a quoted value is data, not a binding, and a command
// substitution's exports stay in its subshell. The parser is a tokenizer,
// not a shell: it handles single and double quotes, backslash escapes,
// $(...) nesting, comments, and the ; && || | & ( ) { } newline
// separators, which is the subset the runner's emitters generate.
func ExportedNames(scripts ...string) []string {
	seen := map[string]bool{}
	exported := map[string]bool{}
	for _, script := range scripts {
		collectExportedNames(script, seen, exported, 0)
	}
	for name := range exported {
		seen[name] = true
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// shellBuiltins are commands that run inside the shell itself, so a prefix
// assignment on them ("IFS= read -r line") scopes to that one builtin and
// never reaches a child process's environment.
var shellBuiltins = map[string]bool{
	":": true, "[": true, "break": true, "cd": true, "continue": true,
	"echo": true, "eval": true, "exit": true, "false": true, "printf": true,
	"read": true, "return": true, "set": true, "shift": true, "test": true,
	"true": true, "unset": true,
	"readonly": true, "local": true, "declare": true, "typeset": true,
	"let": true, "pwd": true, "wait": true, "trap": true, "umask": true,
	"getopts": true, "source": true, ".": true, "alias": true,
	"hash": true, "type": true, "ulimit": true,
}

// maxShellDepth bounds sh -c recursion so a malformed input cannot loop.
const maxShellDepth = 8

// shellWord is one word of a simple command after quote removal.
// nameQuoted records that a quote opened before the word's first '=': sh
// treats 'A'=b as a command word, not an assignment.
type shellWord struct {
	text       string
	nameQuoted bool
}

// assignmentName returns NAME when w is an unquoted-name NAME=value word,
// or "" otherwise.
func (w shellWord) assignmentName() string {
	if w.nameQuoted {
		return ""
	}
	name, _, ok := strings.Cut(w.text, "=")
	if !ok || !envNameRe.MatchString(name) {
		return ""
	}
	return name
}

// collectExportedNames records the names script exports. seen collects
// names that reach a child process for certain (prefix and env
// assignments, and a finished child shell's exports); exported holds the
// current shell's export attributes, which a later "export -n" can clear.
func collectExportedNames(script string, seen, exported map[string]bool, depth int) {
	if depth > maxShellDepth {
		return
	}
	for _, words := range shellCommands(script) {
		collectFromCommand(words, seen, exported, depth)
	}
}

// collectFromCommand records the names one simple command exports.
func collectFromCommand(words []shellWord, seen, exported map[string]bool, depth int) {
	// Leading NAME=value words are prefix assignments when a command
	// follows them, and shell-local assignments when nothing does.
	i := 0
	for i < len(words) && words[i].assignmentName() != "" {
		i++
	}
	if i == len(words) {
		return
	}
	cmd, args := words[i].text, words[i+1:]
	if !shellBuiltins[cmd] {
		for _, w := range words[:i] {
			seen[w.assignmentName()] = true
		}
		// Launching a process hands it the shell's current export
		// attributes, so a later "export -n" cannot retract them. "export"
		// and "command" run inside the shell (command may run a builtin).
		if cmd != "export" && cmd != "command" {
			for name := range exported {
				seen[name] = true
			}
		}
	}
	switch cmd {
	case "export":
		// Options precede the operands and end at "--" or the first
		// operand; -n (alone or combined, as in -pn) switches the command
		// to removing the export attribute. -f selects the function
		// namespace, so a function-mode "-n" leaves variables exported.
		unexport, funcMode, optsDone := false, false, false
		for _, a := range args {
			if !optsDone && strings.HasPrefix(a.text, "-") {
				if a.text == "--" {
					optsDone = true
				} else {
					if strings.Contains(a.text[1:], "n") {
						unexport = true
					}
					if strings.Contains(a.text[1:], "f") {
						funcMode = true
					}
				}
				continue
			}
			optsDone = true
			name := a.assignmentName()
			if name == "" && envNameRe.MatchString(a.text) {
				name = a.text
			}
			switch {
			case name == "":
			case unexport && funcMode:
			case unexport:
				delete(exported, name)
			default:
				exported[name] = true
			}
		}
	case "env":
		for j, a := range args {
			if strings.HasPrefix(a.text, "-") {
				continue
			}
			name := a.assignmentName()
			if name == "" {
				// env launches an external program, which cannot run the
				// export builtin, so the command gets an isolated set.
				collectFromCommand(args[j:], seen, map[string]bool{}, depth)
				return
			}
			seen[name] = true
		}
	case "exec", "nohup":
		collectFromCommand(args, seen, map[string]bool{}, depth)
	case "command":
		collectFromCommand(args, seen, exported, depth)
	case "sh", "bash", "/bin/sh", "/bin/bash":
		for j, a := range args {
			if strings.HasPrefix(a.text, "-") && strings.Contains(a.text, "c") && j+1 < len(args) {
				// The child shell has its own export attributes: its
				// "export -n" cannot un-export a name in this shell.
				child := map[string]bool{}
				collectExportedNames(args[j+1].text, seen, child, depth+1)
				for name := range child {
					seen[name] = true
				}
				return
			}
		}
	}
}

// shellCommands splits script into simple commands, each a list of words
// with quotes removed. Command substitutions are kept as opaque word text.
func shellCommands(script string) [][]shellWord {
	var (
		cmds       [][]shellWord
		words      []shellWord
		cur        strings.Builder
		inWord     bool
		nameQuoted bool
		sawEq      bool
	)
	endWord := func() {
		if inWord {
			words = append(words, shellWord{text: cur.String(), nameQuoted: nameQuoted})
		}
		cur.Reset()
		inWord, nameQuoted, sawEq = false, false, false
	}
	endCmd := func() {
		endWord()
		if len(words) > 0 {
			cmds = append(cmds, words)
		}
		words = nil
	}
	quote := func() {
		inWord = true
		if !sawEq {
			nameQuoted = true
		}
	}
	for i := 0; i < len(script); i++ {
		c := script[i]
		switch {
		case c == '\'':
			quote()
			j := strings.IndexByte(script[i+1:], '\'')
			if j < 0 {
				cur.WriteString(script[i+1:])
				i = len(script)
				break
			}
			cur.WriteString(script[i+1 : i+1+j])
			i += j + 1
		case c == '"':
			quote()
			for i++; i < len(script) && script[i] != '"'; i++ {
				switch {
				case script[i] == '\\' && i+1 < len(script) && strings.IndexByte("\"\\$`\n", script[i+1]) >= 0:
					i++
				case strings.HasPrefix(script[i:], "$("):
					end := skipSubstitution(script, i+2)
					cur.WriteString(script[i:end])
					i = end - 1
					continue
				}
				cur.WriteByte(script[i])
			}
		case strings.HasPrefix(script[i:], "$("):
			// A command substitution runs in a subshell: its exports never
			// reach the caller, so its text is kept as opaque word data.
			inWord = true
			end := skipSubstitution(script, i+2)
			cur.WriteString(script[i:end])
			i = end - 1
		case c == '\\' && i+1 < len(script):
			inWord = true
			i++
			if script[i] != '\n' {
				cur.WriteByte(script[i])
			}
		case c == ' ' || c == '\t':
			endWord()
		case c == '\n' || c == ';' || c == '&' || c == '|' || c == '(' || c == ')':
			endCmd()
		case (c == '{' || c == '}') && !inWord:
			endCmd()
		case c == '#' && !inWord:
			for i < len(script) && script[i] != '\n' {
				i++
			}
			endCmd()
		default:
			inWord = true
			if c == '=' {
				sawEq = true
			}
			cur.WriteByte(c)
		}
	}
	endCmd()
	return cmds
}

// skipSubstitution returns the index just past the ")" closing the command
// substitution whose body starts at i (just after "$("), honoring nested
// quotes, escapes, and parentheses. An unterminated substitution consumes
// the rest of the script.
func skipSubstitution(s string, i int) int {
	depth := 1
	for i < len(s) {
		switch s[i] {
		case '\\':
			i += 2
			continue
		case '\'':
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				return len(s)
			}
			i += j + 2
			continue
		case '"':
			i = skipDoubleQuoted(s, i+1)
			continue
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
		i++
	}
	return len(s)
}

// skipDoubleQuoted returns the index just past the '"' closing the
// double-quoted string whose body starts at i, descending into command
// substitutions so their own quotes do not end it.
func skipDoubleQuoted(s string, i int) int {
	for i < len(s) {
		switch {
		case s[i] == '\\':
			i += 2
		case s[i] == '"':
			return i + 1
		case strings.HasPrefix(s[i:], "$("):
			i = skipSubstitution(s, i+2)
		default:
			i++
		}
	}
	return len(s)
}
