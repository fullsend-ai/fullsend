package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// MarshalPreservingLayout serializes w for writing back over orig, the
// current contents of the same config.yaml. Marshal re-renders the whole
// file, which drops every comment and normalizes key order, indentation
// and scalar quoting. Here, when w differs from orig only in its
// agents: and allowed_remote_resources: lists, just the changed entries
// are edited in orig and every other line is left byte-identical.
//
// The edit is accepted only when the result decodes to the same config
// Marshal would write. When orig is empty, cannot be edited in place, or
// the check fails, the result of w.Marshal() is returned instead.
func MarshalPreservingLayout(orig []byte, w ConfigWriter) ([]byte, error) {
	full, err := w.Marshal()
	if err != nil {
		return nil, err
	}
	c, ok := w.(*perRepoConfig)
	if !ok || len(bytes.TrimSpace(orig)) == 0 {
		return full, nil
	}
	out, err := spliceAgentLists(orig, c.Agents, c.AllowedRemoteResources)
	if err != nil || !marshalsTo(out, full) {
		return full, nil
	}
	return out, nil
}

// marshalsTo reports whether data parses as a per-repo config that
// Marshal renders as want.
func marshalsTo(data, want []byte) bool {
	var got perRepoConfig
	if err := unmarshalPerRepo(data, &got); err != nil {
		return false
	}
	b, err := got.Marshal()
	return err == nil && bytes.Equal(b, want)
}

var errNoSplice = errors.New("config cannot be edited in place")

// lineEdit replaces lines start..end (1-based, inclusive) of the
// original file. An insertion has end == start-1 and places its lines
// before line start.
type lineEdit struct {
	start, end int
	lines      []string
	depth      int // nesting depth; orders insertions at one position
	seq        int // creation order; orders insertions at one depth
}

func (l lineEdit) insertion() bool { return l.end < l.start }

// layoutEditor collects line edits against the original file.
type layoutEditor struct {
	lines     []string
	crlf      bool
	indent    int // nested mapping indentation
	seqIndent int // dash offset of a block sequence under its key
	edits     []lineEdit
}

// spliceAgentLists returns orig with its top-level agents: and
// allowed_remote_resources: lists edited to hold agents and resources.
// Matching entries are left untouched, changed entries are edited field
// by field, and new entries are appended in the existing style.
func spliceAgentLists(orig []byte, agents []AgentEntry, resources []string) ([]byte, error) {
	dec := yaml.NewDecoder(bytes.NewReader(orig))
	var doc, extra yaml.Node
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errNoSplice // a second document, or trailing garbage
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		return nil, errNoSplice
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode || root.Style&yaml.FlowStyle != 0 || len(root.Content) == 0 || hasMergeKey(root) {
		return nil, errNoSplice
	}
	text := string(orig)
	e := &layoutEditor{
		lines:  strings.Split(text, "\n"),
		crlf:   strings.Contains(text, "\r\n"),
		indent: detectMappingIndent(root),
	}
	e.seqIndent = e.detectSequenceIndent(root)

	agentNodes := make([]*yaml.Node, len(agents))
	for i := range agents {
		n, err := encodeNode(agents[i])
		if err != nil {
			return nil, err
		}
		agentNodes[i] = n
	}
	resourceNodes := make([]*yaml.Node, len(resources))
	for i, r := range resources {
		resourceNodes[i] = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: r}
	}

	rootEnd := len(e.lines)
	// A missing key goes where Marshal would put it relative to its
	// neighbours, when they are present.
	if err := e.editTopList(root, rootEnd, "agents", agentNodes, len(agents) > 0, agentListOps, "roles"); err != nil {
		return nil, err
	}
	if err := e.editTopList(root, rootEnd, "allowed_remote_resources", resourceNodes, resources != nil, stringListOps, "agents", "roles"); err != nil {
		return nil, err
	}
	return e.apply()
}

// listOps identifies and compares the items of one list.
type listOps struct {
	key   func(*yaml.Node) (string, bool)
	equal func(old, new *yaml.Node) bool
}

var agentListOps = listOps{
	key: func(n *yaml.Node) (string, bool) {
		var a AgentEntry
		if err := n.Decode(&a); err != nil {
			return "", false
		}
		return strings.ToLower(a.DerivedName()), true
	},
	equal: func(old, new *yaml.Node) bool {
		var a, b AgentEntry
		if old.Decode(&a) != nil || new.Decode(&b) != nil {
			return false
		}
		return reflect.DeepEqual(a, b)
	},
}

var stringListOps = listOps{
	key: func(n *yaml.Node) (string, bool) {
		var s string
		if n.Kind != yaml.ScalarNode || n.Decode(&s) != nil {
			return "", false
		}
		return s, true
	},
	equal: func(old, new *yaml.Node) bool {
		var a, b string
		return old.Decode(&a) == nil && new.Decode(&b) == nil && a == b
	},
}

// editTopList edits the value of the top-level key to the list items.
// present reports whether the key belongs in the file at all. An absent
// key with present set is inserted after the first of the after keys the
// file has, else after the last top-level key.
func (e *layoutEditor) editTopList(root *yaml.Node, rootEnd int, key string, items []*yaml.Node, present bool, ops listOps, after ...string) error {
	value := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: items}
	ki := mappingKeyIndexOf(root, key)
	if ki < 0 {
		if !present {
			return nil
		}
		anchor := len(root.Content) - 2
		for _, k := range after {
			if i := mappingKeyIndexOf(root, k); i >= 0 {
				anchor = i
				break
			}
		}
		_, end, err := e.childSpan(root, anchor, rootEnd)
		if err != nil {
			return err
		}
		lines, err := e.renderPair(&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value, root.Content[0].Column)
		if err != nil {
			return err
		}
		e.insert(end+1, lines, 0)
		return nil
	}
	if !present {
		return e.remove(root, ki, rootEnd)
	}
	old := root.Content[ki+1]
	if old.Kind == yaml.SequenceNode && old.Style&yaml.FlowStyle == 0 && len(old.Content) > 0 && len(items) > 0 {
		_, end, err := e.childSpan(root, ki, rootEnd)
		if err != nil {
			return err
		}
		return e.editSequence(old, end, items, ops)
	}
	if listEqual(old, items, ops) {
		return nil
	}
	return e.replacePair(root, ki, rootEnd, value)
}

// editSequence edits the block sequence seq, whose last line is seqEnd,
// to hold items. Items are matched by key in order: an unmatched old item
// is removed, an unmatched new item is inserted at its position, and a
// matched pair that differs is edited in place.
func (e *layoutEditor) editSequence(seq *yaml.Node, seqEnd int, items []*yaml.Node, ops listOps) error {
	dashCol, contentCol, err := e.itemColumns(seq, 0)
	if err != nil {
		return err
	}
	styleRef := seq.Content[len(seq.Content)-1]
	render := func(ns []*yaml.Node) ([]string, error) {
		var out []string
		for _, n := range ns {
			n = withScalarStyle(n, styleRef)
			lines, err := e.renderItem(n, dashCol, contentCol)
			if err != nil {
				return nil, err
			}
			out = append(out, lines...)
		}
		return out, nil
	}

	next := 0
	lastEnd := 0
	for i, old := range seq.Content {
		start, end, err := e.childSpan(seq, i, seqEnd)
		if err != nil {
			return err
		}
		lastEnd = end
		oldKey, ok := ops.key(old)
		if !ok {
			return errNoSplice
		}
		j := -1
		for x := next; x < len(items); x++ {
			if k, ok := ops.key(items[x]); ok && k == oldKey {
				j = x
				break
			}
		}
		if j < 0 {
			if err := e.remove(seq, i, seqEnd); err != nil {
				return err
			}
			continue
		}
		if j > next {
			lines, err := render(items[next:j])
			if err != nil {
				return err
			}
			e.insert(e.headStart(start, dashCol), lines, 1)
		}
		if !ops.equal(old, items[j]) {
			if err := e.editItem(seq, i, seqEnd, items[j], dashCol, contentCol); err != nil {
				return err
			}
		}
		next = j + 1
	}
	if next < len(items) {
		lines, err := render(items[next:])
		if err != nil {
			return err
		}
		e.insert(lastEnd+1, lines, 1)
	}
	return nil
}

// editItem changes item idx of seq to want: field by field when both are
// block mappings, else by replacing the whole item.
func (e *layoutEditor) editItem(seq *yaml.Node, idx, seqEnd int, want *yaml.Node, dashCol, contentCol int) error {
	old := seq.Content[idx]
	start, end, err := e.childSpan(seq, idx, seqEnd)
	if err != nil {
		return err
	}
	if isBlockMapping(old) && want.Kind == yaml.MappingNode && len(want.Content) > 0 {
		return e.editMapping(old, end, want, 2)
	}
	// Keep a source-only entry written as a plain string in that form.
	if old.Kind == yaml.ScalarNode && want.Kind == yaml.MappingNode && len(want.Content) == 2 && want.Content[0].Value == "source" {
		want = withScalarStyle(want.Content[1], old)
	}
	lines, err := e.renderItem(want, dashCol, contentCol)
	if err != nil {
		return err
	}
	e.replace(start, end, lines)
	return nil
}

// editMapping edits the block mapping m, whose last line is mEnd, to
// match want: unchanged pairs stay, changed ones are re-rendered (nested
// block mappings recursively), removed ones are deleted and new ones are
// appended after the last pair.
func (e *layoutEditor) editMapping(m *yaml.Node, mEnd int, want *yaml.Node, depth int) error {
	keyCol := m.Content[0].Column
	seen := make(map[string]bool, len(want.Content)/2)
	lastEnd := 0
	for i := 0; i+1 < len(m.Content); i += 2 {
		_, end, err := e.childSpan(m, i, mEnd)
		if err != nil {
			return err
		}
		lastEnd = end
		key := m.Content[i].Value
		wi := mappingKeyIndexOf(want, key)
		if wi < 0 {
			if err := e.remove(m, i, mEnd); err != nil {
				return err
			}
			continue
		}
		seen[key] = true
		oldV, newV := m.Content[i+1], want.Content[wi+1]
		if nodesEqual(oldV, newV) {
			continue
		}
		if isBlockMapping(oldV) && newV.Kind == yaml.MappingNode && len(newV.Content) > 0 {
			if err := e.editMapping(oldV, end, newV, depth+1); err != nil {
				return err
			}
			continue
		}
		if err := e.replacePair(m, i, mEnd, newV); err != nil {
			return err
		}
	}
	var added []string
	for i := 0; i+1 < len(want.Content); i += 2 {
		if seen[want.Content[i].Value] {
			continue
		}
		lines, err := e.renderPair(want.Content[i], want.Content[i+1], keyCol)
		if err != nil {
			return err
		}
		added = append(added, lines...)
	}
	if len(added) > 0 {
		e.insert(lastEnd+1, added, depth)
	}
	return nil
}

// replacePair re-renders pair idx of mapping m with value v, keeping the
// old value's quoting style and line comment when both are scalars.
func (e *layoutEditor) replacePair(m *yaml.Node, idx, mEnd int, v *yaml.Node) error {
	start, end, err := e.childSpan(m, idx, mEnd)
	if err != nil {
		return err
	}
	old := m.Content[idx+1]
	v = withScalarStyle(v, old)
	if old.Kind == yaml.ScalarNode && v.Kind == yaml.ScalarNode && v.LineComment == "" && old.LineComment != "" {
		cp := *v
		cp.LineComment = old.LineComment
		v = &cp
	}
	key := *m.Content[idx]
	key.HeadComment, key.LineComment, key.FootComment = "", "", ""
	lines, err := e.renderPair(&key, v, m.Content[idx].Column)
	if err != nil {
		return err
	}
	e.replace(start, end, lines)
	return nil
}

// remove deletes child idx of container c together with the comment
// lines directly above it at its own indentation.
func (e *layoutEditor) remove(c *yaml.Node, idx, cEnd int) error {
	start, end, err := e.childSpan(c, idx, cEnd)
	if err != nil {
		return err
	}
	col := c.Content[idx].Column
	if c.Kind == yaml.SequenceNode {
		if col, _, err = e.itemColumns(c, idx); err != nil {
			return err
		}
	}
	e.replace(e.headStart(start, col), end, nil)
	return nil
}

func (e *layoutEditor) replace(start, end int, lines []string) {
	e.edits = append(e.edits, lineEdit{start: start, end: end, lines: lines, seq: len(e.edits)})
}

func (e *layoutEditor) insert(at int, lines []string, depth int) {
	e.edits = append(e.edits, lineEdit{start: at, end: at - 1, lines: lines, depth: depth, seq: len(e.edits)})
}

// apply returns the original lines with the collected edits applied.
func (e *layoutEditor) apply() ([]byte, error) {
	edits := append([]lineEdit(nil), e.edits...)
	sort.SliceStable(edits, func(i, j int) bool {
		a, b := edits[i], edits[j]
		if a.start != b.start {
			return a.start < b.start
		}
		if a.insertion() != b.insertion() {
			return a.insertion() // insert before a span that starts here
		}
		if a.depth != b.depth {
			return a.depth > b.depth // close the inner list first
		}
		return a.seq < b.seq
	})
	out := make([]string, 0, len(e.lines))
	cursor := 1
	for _, ed := range edits {
		if ed.start < cursor || ed.end > len(e.lines) {
			return nil, errNoSplice
		}
		out = append(out, e.lines[cursor-1:ed.start-1]...)
		for _, l := range ed.lines {
			if e.crlf {
				l += "\r"
			}
			out = append(out, l)
		}
		cursor = ed.end + 1
	}
	out = append(out, e.lines[cursor-1:]...)
	return []byte(strings.Join(out, "\n")), nil
}

// childSpan returns the 1-based inclusive line range of child idx of
// container c (the key line through the value for mappings), excluding
// trailing blank and comment lines, which stay with what follows. cEnd
// is the last line of c.
func (e *layoutEditor) childSpan(c *yaml.Node, idx, cEnd int) (int, int, error) {
	step := 1
	if c.Kind == yaml.MappingNode {
		step = 2
	}
	start := c.Content[idx].Line
	next := cEnd + 1
	if n := idx + step; n < len(c.Content) {
		next = c.Content[n].Line
	}
	if start < 1 || start >= next || next-1 > len(e.lines) {
		return 0, 0, errNoSplice
	}
	end := next - 1
	for end > start && isBlankOrCommentLine(e.lines[end-1]) {
		end--
	}
	return start, end, nil
}

// headStart moves start up over the comment lines directly above it
// that sit at column col.
func (e *layoutEditor) headStart(start, col int) int {
	prefix := strings.Repeat(" ", col-1) + "#"
	for start > 1 && strings.HasPrefix(e.lines[start-2], prefix) {
		start--
	}
	return start
}

// itemColumns returns the column of the dash of item idx of seq and the
// column its content starts at.
func (e *layoutEditor) itemColumns(seq *yaml.Node, idx int) (int, int, error) {
	n := seq.Content[idx]
	if n.Line < 1 || n.Line > len(e.lines) || n.Column < 2 {
		return 0, 0, errNoSplice
	}
	line := e.lines[n.Line-1]
	for i := min(n.Column-2, len(line)-1); i >= 0; i-- {
		switch line[i] {
		case '-':
			return i + 1, n.Column, nil
		case ' ':
		default:
			return 0, 0, errNoSplice
		}
	}
	return 0, 0, errNoSplice
}

// renderItem renders n as a sequence item with its dash at dashCol and
// its content at contentCol.
func (e *layoutEditor) renderItem(n *yaml.Node, dashCol, contentCol int) ([]string, error) {
	lines, err := e.encodeLines(n)
	if err != nil {
		return nil, err
	}
	if contentCol <= dashCol {
		contentCol = dashCol + 2
	}
	pad := strings.Repeat(" ", contentCol-1)
	lines[0] = strings.Repeat(" ", dashCol-1) + "-" + strings.Repeat(" ", contentCol-dashCol-1) + lines[0]
	for i := 1; i < len(lines); i++ {
		if lines[i] != "" {
			lines[i] = pad + lines[i]
		}
	}
	return lines, nil
}

// renderPair renders key: value with the key at keyCol. A non-empty
// sequence value gets its items rendered at the file's sequence
// indentation.
func (e *layoutEditor) renderPair(key, value *yaml.Node, keyCol int) ([]string, error) {
	pad := strings.Repeat(" ", keyCol-1)
	if value.Kind == yaml.SequenceNode && len(value.Content) > 0 && value.Style&yaml.FlowStyle == 0 {
		head, err := e.encodeLines(&yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
			key, {Kind: yaml.ScalarNode, Tag: "!!null", Value: ""},
		}})
		if err != nil {
			return nil, err
		}
		if len(head) != 1 {
			return nil, errNoSplice
		}
		lines := []string{pad + strings.TrimRight(head[0], " ")}
		dashCol := keyCol + e.seqIndent
		for _, item := range value.Content {
			il, err := e.renderItem(item, dashCol, dashCol+2)
			if err != nil {
				return nil, err
			}
			lines = append(lines, il...)
		}
		return lines, nil
	}
	lines, err := e.encodeLines(&yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{key, value}})
	if err != nil {
		return nil, err
	}
	for i := range lines {
		if lines[i] != "" {
			lines[i] = pad + lines[i]
		}
	}
	return lines, nil
}

// encodeLines encodes n as block YAML at the file's indentation and
// returns its lines.
func (e *layoutEditor) encodeLines(n *yaml.Node) ([]string, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(e.indent)
	if err := enc.Encode(n); err != nil {
		return nil, fmt.Errorf("encoding config: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("encoding config: %w", err)
	}
	return strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n"), nil
}

// encodeNode encodes v into a yaml.Node.
func encodeNode(v any) (*yaml.Node, error) {
	var n yaml.Node
	if err := n.Encode(v); err != nil {
		return nil, fmt.Errorf("encoding config: %w", err)
	}
	return &n, nil
}

// withScalarStyle returns n with ref's quoting style when both are
// string scalars and ref is quoted, so an edited value keeps the quotes
// it was written with.
func withScalarStyle(n, ref *yaml.Node) *yaml.Node {
	const quoted = yaml.DoubleQuotedStyle | yaml.SingleQuotedStyle
	if n.Kind != yaml.ScalarNode || ref.Kind != yaml.ScalarNode || n.Tag != "!!str" || ref.Style&quoted == 0 {
		return n
	}
	if ref.Style&yaml.SingleQuotedStyle != 0 && strings.ContainsAny(n.Value, "\n") {
		return n
	}
	cp := *n
	cp.Style = ref.Style & quoted
	return &cp
}

// listEqual reports whether old is a sequence whose items equal items.
func listEqual(old *yaml.Node, items []*yaml.Node, ops listOps) bool {
	if old.Kind != yaml.SequenceNode || len(old.Content) != len(items) {
		return false
	}
	for i := range items {
		if !ops.equal(old.Content[i], items[i]) {
			return false
		}
	}
	return true
}

// nodesEqual reports whether a and b decode to the same data.
func nodesEqual(a, b *yaml.Node) bool {
	var x, y any
	if a.Decode(&x) != nil || b.Decode(&y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

func isBlockMapping(n *yaml.Node) bool {
	return n.Kind == yaml.MappingNode && n.Style&yaml.FlowStyle == 0 && len(n.Content) > 0 && !hasMergeKey(n)
}

func hasMergeKey(m *yaml.Node) bool {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Tag == "!!merge" {
			return true
		}
	}
	return false
}

func mappingKeyIndexOf(m *yaml.Node, key string) int {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return i
		}
	}
	return -1
}

func isBlankOrCommentLine(line string) bool {
	t := strings.TrimSpace(line)
	return t == "" || strings.HasPrefix(t, "#")
}

// detectMappingIndent returns the indentation the file uses for a block
// mapping nested under a key, defaulting to 2.
func detectMappingIndent(n *yaml.Node) int {
	if d, ok := findMappingIndent(n); ok {
		return d
	}
	return 2
}

func findMappingIndent(n *yaml.Node) (int, bool) {
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			v := n.Content[i+1]
			if isBlockMapping(v) {
				if d := v.Content[0].Column - n.Content[i].Column; d > 0 {
					return d, true
				}
			}
			if d, ok := findMappingIndent(v); ok {
				return d, true
			}
		}
	case yaml.SequenceNode:
		for _, c := range n.Content {
			if d, ok := findMappingIndent(c); ok {
				return d, true
			}
		}
	}
	return 0, false
}

// detectSequenceIndent returns how far the dashes of a top-level block
// sequence sit right of its key, defaulting to the mapping indentation.
func (e *layoutEditor) detectSequenceIndent(root *yaml.Node) int {
	for i := 0; i+1 < len(root.Content); i += 2 {
		v := root.Content[i+1]
		if v.Kind != yaml.SequenceNode || v.Style&yaml.FlowStyle != 0 || len(v.Content) == 0 {
			continue
		}
		dashCol, _, err := e.itemColumns(v, 0)
		if err != nil {
			continue
		}
		if d := dashCol - root.Content[i].Column; d >= 0 {
			return d
		}
	}
	return e.indent
}
