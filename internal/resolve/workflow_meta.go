package resolve

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/fullsend-ai/fullsend/internal/harness"
)

// maxWorkflowMetaBytes bounds how much of a workflow script is read to
// find its meta object. The meta is the first statement, so it sits at
// the top of the file.
const maxWorkflowMetaBytes = 16 << 10

// maxMetaDepth bounds how deeply arrays and objects may nest inside meta.
const maxMetaDepth = 8

// checkWorkflowMeta reads the leading `export const meta = { ... }` of
// workflows/<rw.Name>.js and requires meta.name to equal rw.Name: Claude
// Code names a plugin workflow by meta.name, not by the file name, and
// fullsend finds the script by the file name. The meta is parsed as a
// literal, never run.
func checkWorkflowMeta(rw *ResolvedWorkflow) error {
	file := "workflows/" + rw.Name + ".js"
	f, err := os.Open(filepath.Join(rw.LocalPath, "workflows", rw.Name+".js"))
	if err != nil {
		return fmt.Errorf("workflow.source %s: reading %s: %w", rw.Source, file, err)
	}
	defer f.Close()
	head, err := io.ReadAll(io.LimitReader(f, maxWorkflowMetaBytes+1))
	if err != nil {
		return fmt.Errorf("workflow.source %s: reading %s: %w", rw.Source, file, err)
	}
	atEOF := len(head) <= maxWorkflowMetaBytes
	if !atEOF {
		head = head[:maxWorkflowMetaBytes]
	}
	name, err := parseWorkflowMeta(head, atEOF)
	if err != nil {
		return fmt.Errorf("workflow.source %s: %s: %w; Claude Code lists a workflow only when `export const meta = { ... }` is the script's first statement and a plain object literal with a quoted name, and fullsend needs `;` right after its closing brace", rw.Source, file, err)
	}
	if name != rw.Name {
		return fmt.Errorf("workflow.source %s: %s declares meta.name %q; Claude Code runs it as /%s:%s, so rename the file or set meta.name to %q", rw.Source, file, name, rw.PluginName, name, rw.Name)
	}
	return nil
}

// errMetaTruncated reports a meta object that does not end within
// maxWorkflowMetaBytes.
var errMetaTruncated = fmt.Errorf("meta does not end within the first %d KB", maxWorkflowMetaBytes>>10)

// errLineEnd reports a line terminator other than LF or CRLF.
var errLineEnd = errors.New("the meta statement contains a lone carriage return, U+2028 or U+2029; save the script with LF or CRLF line endings")

// errNotLiteral reports meta syntax outside the accepted literal grammar.
func errNotLiteral(what string) error {
	return fmt.Errorf("meta %s, which is not a plain object literal", what)
}

// metaScanner is a bounded, literal-only reader for the meta object at
// the top of a workflow script. It accepts a strict subset of JavaScript
// object literals (strings, numbers, true, false, null, and arrays and
// objects of these) and refuses anything else, so nothing it accepts
// could need the script to run.
type metaScanner struct {
	src []byte
	pos int
	// atEOF reports that src ends where the file ends, rather than at the
	// read bound.
	atEOF bool
}

// parseWorkflowMeta returns the name value of the script's leading
// `export const meta = { ... };`. The grammar is a whitelist: after an
// optional shebang, comments and whitespace, the statement is the object
// literal followed by spaces or tabs and `;`. Nothing after the `;` is
// read, so the statement cannot continue into a member access, call or
// operator. CRLF is read as LF; any other carriage return, and U+2028 or
// U+2029, is refused before the `;`, so a `//` comment ends only at LF.
// name must appear exactly once, as a quoted string without escapes,
// holding only letters, digits, _ and -.
func parseWorkflowMeta(src []byte, atEOF bool) (string, error) {
	if !atEOF && len(src) > 0 && src[len(src)-1] == '\r' {
		// The read bound may split a CRLF.
		src = src[:len(src)-1]
	}
	src = bytes.ReplaceAll(src, []byte("\r\n"), []byte("\n"))
	s := &metaScanner{src: src, atEOF: atEOF}
	if len(src) >= 2 && src[0] == '#' && src[1] == '!' {
		if err := s.skipLine(); err != nil {
			return "", err
		}
	}
	for _, want := range []string{"export", "const", "meta"} {
		if err := s.skipSpace(); err != nil {
			return "", err
		}
		if !s.word(want) {
			return "", errors.New("the script does not start with `export const meta = {`")
		}
	}
	for _, want := range []byte{'=', '{'} {
		if err := s.skipSpace(); err != nil {
			return "", err
		}
		if !s.byte(want) {
			return "", errors.New("the script does not start with `export const meta = {`")
		}
	}

	name, found := "", false
	err := s.object(func(key string) error {
		if key != "name" {
			return s.value(2)
		}
		if found {
			return errors.New("meta declares name more than once")
		}
		v, err := s.nameValue()
		if err != nil {
			return err
		}
		name, found = v, true
		return nil
	})
	if err != nil {
		return "", err
	}
	if !found {
		return "", errors.New("meta has no name")
	}
	if err := s.statementEnd(); err != nil {
		return "", err
	}
	return name, nil
}

// object reads the members of an object literal whose { is already
// consumed, through its closing }. member reads the value after each key
// and its colon.
func (s *metaScanner) object(member func(key string) error) error {
	for {
		if err := s.skipSpace(); err != nil {
			return err
		}
		if s.byte('}') {
			return nil
		}
		key, err := s.key()
		if err != nil {
			return err
		}
		if err := s.skipSpace(); err != nil {
			return err
		}
		if !s.byte(':') {
			if s.pos >= len(s.src) {
				return errMetaTruncated
			}
			return fmt.Errorf("meta key %q has no `: value`; shorthand properties and methods are not plain literals", key)
		}
		if err := s.skipSpace(); err != nil {
			return err
		}
		if err := member(key); err != nil {
			return err
		}
		if err := s.skipSpace(); err != nil {
			return err
		}
		if s.byte(',') {
			continue
		}
		if s.byte('}') {
			return nil
		}
		if s.pos >= len(s.src) {
			return errMetaTruncated
		}
		return s.unexpected(fmt.Sprintf("after the value of meta key %q", key))
	}
}

// value reads one literal value: a string, a number, true, false, null, or
// an array or object of these, nested at most maxMetaDepth deep.
func (s *metaScanner) value(depth int) error {
	if depth > maxMetaDepth {
		return fmt.Errorf("meta nests arrays and objects more than %d deep", maxMetaDepth)
	}
	if s.pos >= len(s.src) {
		return errMetaTruncated
	}
	switch c := s.src[s.pos]; {
	case c == '"' || c == '\'':
		_, err := s.str(true)
		return err
	case c == '[':
		s.pos++
		return s.array(depth)
	case c == '{':
		s.pos++
		return s.object(func(string) error { return s.value(depth + 1) })
	case s.at("..."):
		return errNotLiteral("uses a spread (...)")
	case c == '-' || (c >= '0' && c <= '9') || c == '.':
		return s.number()
	case c == '`':
		return errNotLiteral("uses a template literal")
	case c == '/':
		return errNotLiteral("uses a regular expression")
	case isIdentStart(c):
		start := s.pos
		for s.pos < len(s.src) && isIdentByte(s.src[s.pos]) {
			s.pos++
		}
		switch w := string(s.src[start:s.pos]); w {
		case "true", "false", "null":
			return nil
		default:
			if s.pos >= len(s.src) && !s.atEOF {
				return errMetaTruncated
			}
			return errNotLiteral(fmt.Sprintf("uses the name %q as a value", w))
		}
	default:
		return s.unexpected("as a meta value")
	}
}

// array reads the elements of an array literal whose [ is already
// consumed, through its closing ]. Holes are refused.
func (s *metaScanner) array(depth int) error {
	for {
		if err := s.skipSpace(); err != nil {
			return err
		}
		if s.byte(']') {
			return nil
		}
		if err := s.value(depth + 1); err != nil {
			return err
		}
		if err := s.skipSpace(); err != nil {
			return err
		}
		if s.byte(',') {
			continue
		}
		if s.byte(']') {
			return nil
		}
		if s.pos >= len(s.src) {
			return errMetaTruncated
		}
		return s.unexpected("in a meta array")
	}
}

// number reads a decimal number literal: an optional -, digits without a
// leading zero, an optional fraction and an optional exponent. Hex, octal,
// BigInt and numeric separators are refused by the identifier byte that
// follows the digits.
func (s *metaScanner) number() error {
	s.byte('-')
	digits := func() int {
		n := 0
		for s.pos < len(s.src) && s.src[s.pos] >= '0' && s.src[s.pos] <= '9' {
			s.pos++
			n++
		}
		return n
	}
	start := s.pos
	intDigits := digits()
	if intDigits > 1 && s.src[start] == '0' {
		return errNotLiteral("uses a number with a leading zero")
	}
	fracDigits := 0
	if s.byte('.') {
		fracDigits = digits()
	}
	if intDigits == 0 && fracDigits == 0 {
		if s.pos >= len(s.src) {
			return errMetaTruncated
		}
		return s.unexpected("in a meta number")
	}
	if s.byte('e') || s.byte('E') {
		if !s.byte('+') {
			s.byte('-')
		}
		if digits() == 0 {
			if s.pos >= len(s.src) {
				return errMetaTruncated
			}
			return s.unexpected("in a meta number's exponent")
		}
	}
	if s.pos < len(s.src) && (isIdentByte(s.src[s.pos]) || s.src[s.pos] == '.') {
		return s.unexpected("after a meta number")
	}
	return nil
}

// nameValue reads meta.name: a single- or double-quoted string with no
// backslash, holding a valid workflow name.
func (s *metaScanner) nameValue() (string, error) {
	if s.pos >= len(s.src) {
		return "", errMetaTruncated
	}
	if q := s.src[s.pos]; q != '"' && q != '\'' {
		return "", errors.New("meta.name is not a single- or double-quoted string")
	}
	v, err := s.str(false)
	if err != nil {
		return "", err
	}
	if !harness.ValidPluginBasename(v) {
		return "", fmt.Errorf("meta.name %q may hold only letters, digits, _ and -", v)
	}
	if err := s.skipSpace(); err != nil {
		return "", err
	}
	if s.pos < len(s.src) && s.src[s.pos] != ',' && s.src[s.pos] != '}' {
		return "", errors.New("meta.name is not a single- or double-quoted string")
	}
	return v, nil
}

// statementEnd requires `;` after the meta object's closing brace, with
// only spaces or tabs between. It reads nothing after the `;`.
func (s *metaScanner) statementEnd() error {
	for s.pos < len(s.src) && (s.src[s.pos] == ' ' || s.src[s.pos] == '\t') {
		s.pos++
	}
	if s.byte(';') {
		return nil
	}
	if s.pos >= len(s.src) && !s.atEOF {
		return errMetaTruncated
	}
	if s.lineEndAt() {
		return errLineEnd
	}
	return errors.New("end the meta statement with `;` directly after its closing brace")
}

// skipSpace skips whitespace and comments.
func (s *metaScanner) skipSpace() error {
	for s.pos < len(s.src) {
		switch c := s.src[s.pos]; {
		case c == ' ' || c == '\t' || c == '\n':
			s.pos++
		case s.at("//"):
			if err := s.skipLine(); err != nil {
				return err
			}
		case s.at("/*"):
			if err := s.skipBlockComment(); err != nil {
				return err
			}
		case s.lineEndAt():
			return errLineEnd
		default:
			return nil
		}
	}
	return nil
}

func (s *metaScanner) skipBlockComment() error {
	end := bytes.Index(s.src[s.pos+2:], []byte("*/"))
	if end < 0 {
		return errMetaTruncated
	}
	if hasLineEnd(s.src[s.pos+2 : s.pos+2+end]) {
		return errLineEnd
	}
	s.pos += 2 + end + 2
	return nil
}

// skipLine skips to the next LF.
func (s *metaScanner) skipLine() error {
	end := bytes.IndexByte(s.src[s.pos:], '\n')
	if end < 0 {
		end = len(s.src) - s.pos
	}
	if hasLineEnd(s.src[s.pos : s.pos+end]) {
		return errLineEnd
	}
	s.pos += end
	return nil
}

// lineEndAt reports a refused line terminator at the cursor.
func (s *metaScanner) lineEndAt() bool {
	return s.at("\r") || s.at("\u2028") || s.at("\u2029")
}

// hasLineEnd reports a refused line terminator in b. CRLF has already
// been read as LF.
func hasLineEnd(b []byte) bool {
	return bytes.IndexByte(b, '\r') >= 0 || bytes.Contains(b, []byte("\u2028")) || bytes.Contains(b, []byte("\u2029"))
}

func (s *metaScanner) at(lit string) bool {
	return len(s.src)-s.pos >= len(lit) && string(s.src[s.pos:s.pos+len(lit)]) == lit
}

// word consumes w when it is a whole identifier at the cursor.
func (s *metaScanner) word(w string) bool {
	if !s.at(w) {
		return false
	}
	if end := s.pos + len(w); end < len(s.src) && isIdentByte(s.src[end]) {
		return false
	}
	s.pos += len(w)
	return true
}

func (s *metaScanner) byte(c byte) bool {
	if s.pos < len(s.src) && s.src[s.pos] == c {
		s.pos++
		return true
	}
	return false
}

// unexpected reports the byte at the cursor.
func (s *metaScanner) unexpected(where string) error {
	return fmt.Errorf("unexpected %q %s", s.src[s.pos], where)
}

// key reads a property key: an identifier or a quoted string without
// escapes.
func (s *metaScanner) key() (string, error) {
	if s.pos >= len(s.src) {
		return "", errMetaTruncated
	}
	switch c := s.src[s.pos]; {
	case c == '"' || c == '\'':
		return s.str(false)
	case isIdentStart(c):
		start := s.pos
		for s.pos < len(s.src) && isIdentByte(s.src[s.pos]) {
			s.pos++
		}
		return string(s.src[start:s.pos]), nil
	case s.at("..."):
		return "", errNotLiteral("uses a spread (...)")
	case c == '[':
		return "", errNotLiteral("uses a computed key ([...])")
	default:
		return "", fmt.Errorf("unexpected %q in meta", c)
	}
}

// str reads a single- or double-quoted string at the cursor and returns
// its text between the quotes. With escapes false a backslash is refused;
// otherwise each escape must be one JavaScript defines and is stepped over
// whole, and the text is returned as written.
func (s *metaScanner) str(escapes bool) (string, error) {
	quote := s.src[s.pos]
	start := s.pos + 1
	for i := start; i < len(s.src); i++ {
		switch s.src[i] {
		case '\\':
			if !escapes {
				return "", errors.New("a meta key or meta.name contains a backslash; write it without escapes")
			}
			n, err := escapeLen(s.src[i+1:])
			if err != nil {
				return "", err
			}
			i += n
		case '\n':
			return "", errors.New("a meta string runs past the end of its line")
		case '\r':
			return "", errLineEnd
		case 0xE2:
			if bytes.HasPrefix(s.src[i:], []byte("\u2028")) || bytes.HasPrefix(s.src[i:], []byte("\u2029")) {
				return "", errLineEnd
			}
		case quote:
			s.pos = i + 1
			return string(s.src[start:i]), nil
		}
	}
	return "", errMetaTruncated
}

// escapeLen returns how many bytes after a backslash its escape takes:
// one of \\ \' \" \n \r \t \b \f \v, \0 not followed by a digit, \xHH,
// \uHHHH or \u{H...} up to 10FFFF. Anything else is refused.
func escapeLen(rest []byte) (int, error) {
	if len(rest) == 0 {
		return 0, errMetaTruncated
	}
	hex := func(b []byte) bool {
		for _, c := range b {
			if !isHexByte(c) {
				return false
			}
		}
		return true
	}
	bad := fmt.Errorf("a meta string has the escape \\%c, which is not one fullsend reads; use \\\\ \\' \\\" \\n \\r \\t \\b \\f \\v \\0 \\xHH or \\uHHHH", rest[0])
	switch rest[0] {
	case '\\', '\'', '"', 'n', 'r', 't', 'b', 'f', 'v':
		return 1, nil
	case '0':
		if len(rest) > 1 && rest[1] >= '0' && rest[1] <= '9' {
			return 0, bad
		}
		return 1, nil
	case 'x':
		if len(rest) < 3 {
			return 0, errMetaTruncated
		}
		if !hex(rest[1:3]) {
			return 0, bad
		}
		return 3, nil
	case 'u':
		if len(rest) > 1 && rest[1] == '{' {
			end := bytes.IndexByte(rest, '}')
			if end < 0 {
				if len(rest) < 9 {
					return 0, errMetaTruncated
				}
				return 0, bad
			}
			digits := rest[2:end]
			if len(digits) == 0 || len(digits) > 6 || !hex(digits) {
				return 0, bad
			}
			var v int
			for _, c := range digits {
				v = v*16 + hexValue(c)
			}
			if v > 0x10FFFF {
				return 0, bad
			}
			return end + 1, nil
		}
		if len(rest) < 5 {
			return 0, errMetaTruncated
		}
		if !hex(rest[1:5]) {
			return 0, bad
		}
		return 5, nil
	default:
		return 0, bad
	}
}

func isHexByte(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func hexValue(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	default:
		return int(c-'A') + 10
	}
}

func isIdentStart(c byte) bool {
	return c == '_' || c == '$' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentByte(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}
