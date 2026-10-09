package resolve

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseWorkflowMetaName(t *testing.T) {
	tests := []struct {
		name    string
		src     string
		want    string
		wantErr string
	}{
		{name: "double quotes", src: `export const meta = {name: "probe"};`, want: "probe"},
		{name: "single quotes, trailing comma", src: "export const meta = {\n  name: 'probe',\n};\n", want: "probe"},
		{name: "quoted key", src: "export const meta = { \"name\": \"probe\" } \t;", want: "probe"},
		{name: "leading comments and shebang", src: "#!/usr/bin/env node\n// a pipeline\n/* more */\nexport const meta = { name: 'probe' };", want: "probe"},
		{name: "semicolon and comment", src: "export const meta = { name: 'probe' }; // the meta\nexport default async () => {}\n", want: "probe"},
		{name: "nothing after the semicolon is read", src: "export const meta = { name: 'probe' }; run()\r.name /* \u2028", want: "probe"},
		{name: "CRLF line ends", src: "#!/usr/bin/env node\r\n// c\r\nexport const meta = {\r\n  name: 'probe', // c\r\n  /* a\r\n b */ a: 1\r\n};\r\nexport default 1\r\n", want: "probe"},
		{
			name: "literals of every kind in other keys",
			src: "export const meta = {\n  description: 'it\\'s, {x} \\u00e9\\u{1F600}\\x41\\0\\n',\n  phases: [{ title: \"a, b\" }, { title: 'c', 'quoted key': [1, -2.5, .5, 1e3, 2E-2, 0, true, false, null, [], {}] },],\n" +
				"  // name: 'not this'\n  /* name: 'nor this' */ retries: 3, nested: { name: 'inner', deep: [[[[[1]]]]] }, $k_1: 0.0,\n  name: 'probe',\n};\n",
			want: "probe",
		},
		{name: "empty", src: "", wantErr: "does not start with"},
		{name: "let instead of const", src: "export let meta = { name: 'probe' }", wantErr: "does not start with"},
		{name: "metadata is not meta", src: "export const metadata = { name: 'probe' }", wantErr: "does not start with"},
		{name: "not an object", src: "export const meta = makeMeta()", wantErr: "does not start with"},
		{name: "no name", src: "export const meta = { description: 'x' }", wantErr: "meta has no name"},
		{name: "duplicate name", src: "export const meta = { name: 'a', name: 'probe' }", wantErr: "meta declares name more than once"},
		{name: "duplicate name, quoted", src: `export const meta = { name: 'probe', "name": 'probe' }`, wantErr: "meta declares name more than once"},
		{name: "duplicate name, single-quoted", src: `export const meta = { 'name': 'probe', name: 'probe' }`, wantErr: "meta declares name more than once"},
		{name: "no semicolon", src: `export const meta = { name: "probe" }`, wantErr: "end the meta statement with `;` directly after its closing brace"},
		{name: "next statement without semicolon", src: "export const meta = { name: 'probe' }\nexport default 1;\n", wantErr: "directly after its closing brace"},
		{name: "member access after the object", src: `export const meta = { name: "probe" }.name;`, wantErr: "directly after its closing brace"},
		{name: "comment before the semicolon", src: "export const meta = { name: 'probe' } /*x*/ ;", wantErr: "directly after its closing brace"},
		{name: "line comment after the object", src: "export const meta = {name: \"probe\"} // c\r.name;", wantErr: "directly after its closing brace"},
		{name: "CR ends a comment inside the object", src: "export const meta = { a: 1, // c\rname: 'probe' };", wantErr: "save the script with LF or CRLF line endings"},
		{name: "CR between members", src: "export const meta = { a: 1,\rname: 'probe' };", wantErr: "LF or CRLF"},
		{name: "CR after the object", src: "export const meta = { name: 'probe' }\r;", wantErr: "LF or CRLF"},
		{name: "CR in a block comment", src: "/* a\r */ export const meta = { name: 'probe' };", wantErr: "LF or CRLF"},
		{name: "CR ends the shebang", src: "#!/usr/bin/env node\rexport const meta = { name: 'probe' };", wantErr: "LF or CRLF"},
		{name: "CR in a string", src: "export const meta = { a: 'x\ry', name: 'probe' };", wantErr: "LF or CRLF"},
		{name: "U+2028 ends a comment", src: "export const meta = { a: 1, // c\u2028name: 'probe' };", wantErr: "LF or CRLF"},
		{name: "U+2029 between members", src: "export const meta = { a: 1,\u2029name: 'probe' };", wantErr: "LF or CRLF"},
		{name: "U+2028 in a string", src: "export const meta = { a: 'x\u2028y', name: 'probe' };", wantErr: "LF or CRLF"},
		{name: "U+2029 after the object", src: "export const meta = { name: 'probe' }\u2029;", wantErr: "LF or CRLF"},
		{name: "escaped name key", src: `export const meta = { "n\u0061me": "x", name: "probe" }`, wantErr: "contains a backslash"},
		{name: "regex value", src: "export const meta = { a: /}/, name: 'probe' }", wantErr: "meta uses a regular expression, which is not a plain object literal"},
		{name: "template literal value", src: "export const meta = { a: `x`, name: 'probe' }", wantErr: "meta uses a template literal"},
		{name: "identifier value", src: "export const meta = { a: base, name: 'probe' }", wantErr: `meta uses the name "base" as a value`},
		{name: "call value", src: "export const meta = { a: (1, 2), name: 'probe' }", wantErr: `unexpected '(' as a meta value`},
		{name: "spread in an array", src: "export const meta = { a: [...b], name: 'probe' }", wantErr: "meta uses a spread"},
		{name: "array hole", src: "export const meta = { a: [1,,2], name: 'probe' }", wantErr: `unexpected ',' as a meta value`},
		{name: "missing comma in an array", src: "export const meta = { a: [1 2], name: 'probe' }", wantErr: `unexpected '2' in a meta array`},
		{name: "operator after a value", src: "export const meta = { a: 1 + 2, name: 'probe' }", wantErr: `unexpected '+' after the value of meta key "a"`},
		{name: "too deep", src: "export const meta = { a: [[[[[[[[1]]]]]]]], name: 'probe' }", wantErr: "more than 8 deep"},
		{name: "bigint", src: "export const meta = { a: 1n, name: 'probe' }", wantErr: `unexpected 'n' after a meta number`},
		{name: "hex number", src: "export const meta = { a: 0x1, name: 'probe' }", wantErr: `unexpected 'x' after a meta number`},
		{name: "leading zero", src: "export const meta = { a: 01, name: 'probe' }", wantErr: "leading zero"},
		{name: "lone minus", src: "export const meta = { a: -, name: 'probe' }", wantErr: `unexpected ',' in a meta number`},
		{name: "bad exponent", src: "export const meta = { a: 1e, name: 'probe' }", wantErr: `unexpected ',' in a meta number's exponent`},
		{name: "unknown escape", src: `export const meta = { a: 'x\q', name: 'probe' }`, wantErr: `the escape \q`},
		{name: "octal escape", src: `export const meta = { a: '\01', name: 'probe' }`, wantErr: `the escape \0`},
		{name: "bad hex escape", src: `export const meta = { a: '\xZZ', name: 'probe' }`, wantErr: `the escape \x`},
		{name: "bad unicode escape", src: `export const meta = { a: '\u12G4', name: 'probe' }`, wantErr: `the escape \u`},
		{name: "unicode escape out of range", src: `export const meta = { a: '\u{110000}', name: 'probe' }`, wantErr: `the escape \u`},
		{name: "unicode escape not closed", src: `export const meta = { a: '\u{12345678', name: 'probe' }`, wantErr: `the escape \u`},
		{name: "name with a backslash", src: `export const meta = { name: 'pro\'be' }`, wantErr: "contains a backslash"},
		{name: "name with a space", src: `export const meta = { name: 'pro be' }`, wantErr: `meta.name "pro be" may hold only letters, digits, _ and -`},
		{name: "empty name", src: `export const meta = { name: '' }`, wantErr: `meta.name "" may hold only`},
		{name: "template literal name", src: "export const meta = { name: `probe` }", wantErr: "meta.name is not a single- or double-quoted string"},
		{name: "concatenated name", src: "export const meta = { name: 'pro' + 'be' }", wantErr: "meta.name is not a single- or double-quoted string"},
		{name: "spread", src: "export const meta = { ...base, name: 'probe' }", wantErr: "meta uses a spread"},
		{name: "computed key", src: "export const meta = { [k]: 'probe' }", wantErr: "meta uses a computed key"},
		{name: "shorthand", src: "export const meta = { name }", wantErr: `meta key "name" has no ` + "`: value`"},
		{name: "method", src: "export const meta = { describe() { return 1 } }", wantErr: `meta key "describe" has no`},
		{name: "number key", src: "export const meta = { 1: 'x' }", wantErr: `unexpected '1' in meta`},
		{name: "second declaration", src: "export const meta = { name: 'probe' }, other = 1;\n", wantErr: "directly after its closing brace"},
		{name: "member access on the next line", src: "export const meta = { name: 'probe' }\n.name;\n", wantErr: "directly after its closing brace"},
		{name: "string across lines", src: "export const meta = { name: 'pro\nbe' }", wantErr: "runs past the end of its line"},
		{name: "unclosed object", src: "export const meta = { name: 'probe'", wantErr: "meta does not end within the first 16 KB"},
		{name: "unclosed after name", src: "export const meta = { name: 'probe' ", wantErr: "meta does not end"},
		{name: "unclosed name", src: "export const meta = { name: ", wantErr: "meta does not end"},
		{name: "unclosed key", src: "export const meta = { ", wantErr: "meta does not end"},
		{name: "key at the end", src: "export const meta = { name", wantErr: "meta does not end"},
		{name: "unclosed string key", src: "export const meta = { 'name", wantErr: "meta does not end"},
		{name: "unclosed value", src: "export const meta = { a: [1, 2", wantErr: "meta does not end"},
		{name: "unclosed string value", src: "export const meta = { a: 'x", wantErr: "meta does not end"},
		{name: "unclosed escape", src: "export const meta = { a: 'x\\", wantErr: "meta does not end"},
		{name: "unclosed comment", src: "/* export const meta = { name: 'probe' }", wantErr: "meta does not end"},
		{name: "unclosed comment in a value", src: "export const meta = { a: 1 /* x", wantErr: "meta does not end"},
		{name: "unclosed comment after the object", src: "export const meta = { name: 'probe' } /* x", wantErr: "directly after its closing brace"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseWorkflowMeta([]byte(tt.src), true)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCheckWorkflowMeta_ReadsOnlyTheHead(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "workflows"), 0o755))
	rw := &ResolvedWorkflow{Name: "probe", PluginName: "wfplug", LocalPath: dir}
	script := filepath.Join(dir, "workflows", "probe.js")

	// A meta object longer than the read bound is not read to its end.
	long := "export const meta = { description: '" + strings.Repeat("x", 20<<10) + "', name: 'probe' }\n"
	require.NoError(t, os.WriteFile(script, []byte(long), 0o644))
	err := checkWorkflowMeta(rw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "meta does not end within the first 16 KB")

	// Nothing after the `;` is read, so a long comment there is fine.
	tail := strings.Repeat("// tail\n", 10000)
	require.NoError(t, os.WriteFile(script, []byte("export const meta = { name: 'probe' };\n"+tail), 0o644))
	require.NoError(t, checkWorkflowMeta(rw))
	block := "/*" + strings.Repeat("x", 20<<10) + "*/\n"
	require.NoError(t, os.WriteFile(script, []byte("export const meta = { name: 'probe' };"+block), 0o644))
	require.NoError(t, checkWorkflowMeta(rw))

	require.NoError(t, os.WriteFile(script, []byte("export const meta = { name: 'probe' }\n"+tail), 0o644))
	err = checkWorkflowMeta(rw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "end the meta statement with `;` directly after its closing brace")
	assert.Contains(t, err.Error(), "fullsend needs `;` right after its closing brace")

	// The `;` must fall within the read bound.
	pad := "export const meta = { name: 'probe' }"
	pad += strings.Repeat(" ", maxWorkflowMetaBytes-len(pad)) + ";"
	require.NoError(t, os.WriteFile(script, []byte(pad), 0o644))
	err = checkWorkflowMeta(rw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "meta does not end within the first 16 KB")

	// A CRLF split by the read bound is not a lone carriage return.
	crlf := "export const meta = {\r\n"
	crlf += strings.Repeat("\r\n", (maxWorkflowMetaBytes-len(crlf))/2) + "name: 'probe' };"
	require.NoError(t, os.WriteFile(script, []byte(crlf[:maxWorkflowMetaBytes-1]+"\r\n"), 0o644))
	err = checkWorkflowMeta(rw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "meta does not end within the first 16 KB")

	// A script exactly at the read bound is read whole.
	exact := "export const meta = { name: 'probe' };\n"
	exact += strings.Repeat("/", maxWorkflowMetaBytes-len(exact))
	require.NoError(t, os.WriteFile(script, []byte(exact), 0o644))
	require.NoError(t, checkWorkflowMeta(rw))

	require.NoError(t, os.Remove(script))
	err = checkWorkflowMeta(rw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `reading workflows/probe.js`)
}

// metaHeader is a valid meta statement that uses every literal kind.
const metaHeader = "#!/usr/bin/env node\n// sample pipeline\nexport const meta = {\n" +
	"  description: 'it\\'s \\u00e9 \\x41 \\u{1F600}', 'quoted': \"q\",\n" +
	"  phases: [{ title: 'a', retries: -1.5e+2 }, [true, false, null]],\n" +
	"  name: 'probe', /* c */ after: {},\n};\nexport default async () => {}\n"

func TestParseWorkflowMeta_TruncatedPrefixes(t *testing.T) {
	src := []byte(metaHeader)
	name, err := parseWorkflowMeta(src, true)
	require.NoError(t, err)
	require.Equal(t, "probe", name)
	for i := 0; i <= len(src); i++ {
		for _, atEOF := range []bool{true, false} {
			got, err := parseWorkflowMeta(src[:i], atEOF)
			if err == nil {
				assert.Equal(t, "probe", got, "prefix of %d bytes, atEOF %v", i, atEOF)
			}
		}
	}
}

var validMetaName = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func FuzzParseWorkflowMetaName(f *testing.F) {
	for _, seed := range []string{
		metaHeader,
		`export const meta = { name: "probe" };`,
		`export const meta = { name: "probe" }`,
		`export const meta = { name: "probe" }.name;`,
		"export const meta = {name: \"probe\"} // c\r.name;",
		"export const meta = { a: 1, // c\rname: 'probe' };",
		"export const meta = { a: 1, // c\u2028name: 'probe' };",
		"export const meta = { a: 1,\u2029name: 'probe' };",
		"export const meta = {\r\n  name: 'probe'\r\n};\r\n",
		"export const meta = { name: 'probe' } /*x*/ ;",
		"export const meta = { name: 'probe' };/*" + strings.Repeat("x", 17<<10) + "*/",
		`export const meta = { "name": "x", name: "probe" }`,
		"export const meta = { a: /}/, name: 'probe' }",
		"export const meta = { a: `x`, name: 'probe' }",
		"export const meta = { name: 'a', name: 'b' }",
		"export const meta = { name: 'probe' }\n.name",
		"export const meta = { a: [[[[[[[[1]]]]]]]], name: 'probe' }",
		"export const meta = { a: '\\u{10FFFF}\\x7f\\0', b: 0.5e-3, name: 'probe' };",
		"export const meta = { ...b, [k]: 1, f() {}, name }",
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, src []byte) {
		for _, atEOF := range []bool{true, false} {
			name, err := parseWorkflowMeta(src, atEOF)
			if err == nil && !validMetaName.MatchString(name) {
				t.Fatalf("accepted meta.name %q from %q", name, src)
			}
		}
	})
}
