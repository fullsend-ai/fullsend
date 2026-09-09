package jira

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// asMap is a small helper to keep test assertions terse when reaching into
// nested ADF map[string]any structures.
func asMap(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any, got %T (%v)", v, v)
	}
	return m
}

func asSlice(t *testing.T, v any) []any {
	t.Helper()
	s, ok := v.([]any)
	if !ok {
		t.Fatalf("expected []any, got %T (%v)", v, v)
	}
	return s
}

// mustADF calls MarkdownToADF and fails the test if it returns an error,
// for the vast majority of test cases that expect src to convert
// successfully.
func mustADF(t *testing.T, src string) map[string]any {
	t.Helper()
	doc, err := MarkdownToADF(src)
	if err != nil {
		t.Fatalf("MarkdownToADF(%q) returned unexpected error: %v", src, err)
	}
	return doc
}

// ---------------------------------------------------------------------------
// MarkdownToADF
// ---------------------------------------------------------------------------

func TestMarkdownToADF_PlainParagraph(t *testing.T) {
	doc := mustADF(t, "hello world")

	if doc["type"] != "doc" {
		t.Errorf("doc type = %v, want %q", doc["type"], "doc")
	}
	if doc["version"] != 1 {
		t.Errorf("doc version = %v, want 1", doc["version"])
	}

	content := asSlice(t, doc["content"])
	if len(content) != 1 {
		t.Fatalf("doc content len = %d, want 1", len(content))
	}
	para := asMap(t, content[0])
	if para["type"] != "paragraph" {
		t.Errorf("block type = %v, want %q", para["type"], "paragraph")
	}
	paraContent := asSlice(t, para["content"])
	if len(paraContent) != 1 {
		t.Fatalf("paragraph content len = %d, want 1", len(paraContent))
	}
	text := asMap(t, paraContent[0])
	if text["type"] != "text" || text["text"] != "hello world" {
		t.Errorf("text node = %+v, want type=text text=%q", text, "hello world")
	}
}

func TestMarkdownToADF_ResolvesBackslashEscapesAndEntities(t *testing.T) {
	doc := mustADF(t, `\*not em\* and &copy; and &amp;`)

	para := asMap(t, asSlice(t, doc["content"])[0])
	nodes := asSlice(t, para["content"])

	var got strings.Builder
	for _, n := range nodes {
		node := asMap(t, n)
		got.WriteString(fmt.Sprint(node["text"]))
	}
	want := "*not em* and \u00a9 and &"
	if got.String() != want {
		t.Errorf("text = %q, want %q (escapes/entities should be resolved, as goldmark's HTML renderer does)", got.String(), want)
	}
}

func TestMarkdownToADF_CodeSpanKeepsBackslashesLiteral(t *testing.T) {
	doc := mustADF(t, "`\\*literal\\*`")

	para := asMap(t, asSlice(t, doc["content"])[0])
	nodes := asSlice(t, para["content"])
	text := asMap(t, nodes[0])
	want := `\*literal\*`
	if text["text"] != want {
		t.Errorf("code span text = %v, want %q (raw/code text must not be unescaped)", text["text"], want)
	}
}

func TestMarkdownToADF_MultipleParagraphs(t *testing.T) {
	doc := mustADF(t, "first\n\nsecond")

	content := asSlice(t, doc["content"])
	if len(content) != 2 {
		t.Fatalf("doc content len = %d, want 2", len(content))
	}
	for i, want := range []string{"first", "second"} {
		para := asMap(t, content[i])
		text := asMap(t, asSlice(t, para["content"])[0])
		if text["text"] != want {
			t.Errorf("paragraph %d text = %v, want %q", i, text["text"], want)
		}
	}
}

func TestMarkdownToADF_HeadingLevels(t *testing.T) {
	for level := 1; level <= 6; level++ {
		src := strings.Repeat("#", level) + " Heading"
		doc := mustADF(t, src)
		content := asSlice(t, doc["content"])
		if len(content) != 1 {
			t.Fatalf("level %d: doc content len = %d, want 1", level, len(content))
		}
		heading := asMap(t, content[0])
		if heading["type"] != "heading" {
			t.Fatalf("level %d: block type = %v, want %q", level, heading["type"], "heading")
		}
		attrs := asMap(t, heading["attrs"])
		gotLevel, ok := attrs["level"].(int)
		if !ok || gotLevel != level {
			t.Errorf("level %d: attrs.level = %v, want %d", level, attrs["level"], level)
		}
	}
}

func TestMarkdownToADF_BoldItalicInlineCode(t *testing.T) {
	doc := mustADF(t, "**bold** and *italic* and `code`")

	para := asMap(t, asSlice(t, doc["content"])[0])
	nodes := asSlice(t, para["content"])

	var foundStrong, foundEm, foundCode bool
	for _, n := range nodes {
		node := asMap(t, n)
		marks, ok := node["marks"].([]any)
		if !ok {
			continue
		}
		for _, m := range marks {
			mark := asMap(t, m)
			switch mark["type"] {
			case "strong":
				if node["text"] != "bold" {
					t.Errorf("strong text = %v, want %q", node["text"], "bold")
				}
				foundStrong = true
			case "em":
				if node["text"] != "italic" {
					t.Errorf("em text = %v, want %q", node["text"], "italic")
				}
				foundEm = true
			case "code":
				if node["text"] != "code" {
					t.Errorf("code text = %v, want %q", node["text"], "code")
				}
				foundCode = true
			}
		}
	}
	if !foundStrong {
		t.Error("expected a text node with a strong mark")
	}
	if !foundEm {
		t.Error("expected a text node with an em mark")
	}
	if !foundCode {
		t.Error("expected a text node with a code mark")
	}
}

func TestMarkdownToADF_BoldInlineCodeDoesNotCombineMarks(t *testing.T) {
	doc := mustADF(t, "**`--flag`**")

	para := asMap(t, asSlice(t, doc["content"])[0])
	nodes := asSlice(t, para["content"])

	var found bool
	for _, n := range nodes {
		node := asMap(t, n)
		if node["text"] != "--flag" {
			continue
		}
		found = true
		marks := asSlice(t, node["marks"])
		var sawCode, sawStrong bool
		for _, m := range marks {
			switch asMap(t, m)["type"] {
			case "code":
				sawCode = true
			case "strong", "em":
				sawStrong = true
			}
		}
		if !sawCode {
			t.Errorf("marks = %v, want a code mark", marks)
		}
		if sawStrong {
			t.Errorf("marks = %v, want code combined only with link, not strong/em", marks)
		}
	}
	if !found {
		t.Fatalf("expected a text node with value %q", "--flag")
	}
}

func TestMarkdownToADF_NestedSameTypeEmphasisDoesNotDuplicateMarks(t *testing.T) {
	for _, src := range []string{"*outer _inner_ text*", "**outer __inner__ text**"} {
		doc := mustADF(t, src)

		para := asMap(t, asSlice(t, doc["content"])[0])
		nodes := asSlice(t, para["content"])

		var found bool
		for _, n := range nodes {
			node := asMap(t, n)
			if node["text"] != "inner" {
				continue
			}
			found = true
			marks := asSlice(t, node["marks"])
			if len(marks) != 1 {
				t.Errorf("%q: marks on %q = %v, want exactly one mark", src, "inner", marks)
			}
		}
		if !found {
			t.Fatalf("%q: expected a text node with value %q", src, "inner")
		}
	}
}

func TestMarkdownToADF_FencedCodeBlockWithLanguage(t *testing.T) {
	doc := mustADF(t, "```go\nfmt.Println(\"hi\")\n```")

	content := asSlice(t, doc["content"])
	if len(content) != 1 {
		t.Fatalf("doc content len = %d, want 1", len(content))
	}
	block := asMap(t, content[0])
	if block["type"] != "codeBlock" {
		t.Fatalf("block type = %v, want %q", block["type"], "codeBlock")
	}
	attrs := asMap(t, block["attrs"])
	if attrs["language"] != "go" {
		t.Errorf("attrs.language = %v, want %q", attrs["language"], "go")
	}
	codeContent := asSlice(t, block["content"])
	text := asMap(t, codeContent[0])
	if !strings.Contains(fmt.Sprint(text["text"]), "fmt.Println") {
		t.Errorf("code text = %v, want it to contain %q", text["text"], "fmt.Println")
	}
}

func TestMarkdownToADF_EmptyFencedCodeBlock(t *testing.T) {
	doc := mustADF(t, "```\n```")

	content := asSlice(t, doc["content"])
	if len(content) != 1 {
		t.Fatalf("doc content len = %d, want 1", len(content))
	}
	block := asMap(t, content[0])
	if block["type"] != "codeBlock" {
		t.Fatalf("block type = %v, want %q", block["type"], "codeBlock")
	}
	codeContent, _ := block["content"].([]any)
	for _, c := range codeContent {
		text := asMap(t, c)
		if text["text"] == "" {
			t.Errorf("codeBlock content = %v, want no zero-length text node (ADF forbids text.text minLength 0)", codeContent)
		}
	}
}

func TestMarkdownToADF_BulletList(t *testing.T) {
	doc := mustADF(t, "- one\n- two\n")

	content := asSlice(t, doc["content"])
	list := asMap(t, content[0])
	if list["type"] != "bulletList" {
		t.Fatalf("block type = %v, want %q", list["type"], "bulletList")
	}
	items := asSlice(t, list["content"])
	if len(items) != 2 {
		t.Fatalf("bulletList content len = %d, want 2", len(items))
	}
	for i, want := range []string{"one", "two"} {
		item := asMap(t, items[i])
		if item["type"] != "listItem" {
			t.Errorf("item %d type = %v, want %q", i, item["type"], "listItem")
		}
		itemPara := asMap(t, asSlice(t, item["content"])[0])
		text := asMap(t, asSlice(t, itemPara["content"])[0])
		if text["text"] != want {
			t.Errorf("item %d text = %v, want %q", i, text["text"], want)
		}
	}
}

func TestMarkdownToADF_OrderedListNonOneStart(t *testing.T) {
	doc := mustADF(t, "5. five\n6. six\n")

	content := asSlice(t, doc["content"])
	list := asMap(t, content[0])
	if list["type"] != "orderedList" {
		t.Fatalf("block type = %v, want %q", list["type"], "orderedList")
	}
	attrs := asMap(t, list["attrs"])
	if attrs["order"] != 5 {
		t.Errorf("attrs.order = %v, want 5", attrs["order"])
	}
	items := asSlice(t, list["content"])
	if len(items) != 2 {
		t.Fatalf("orderedList content len = %d, want 2", len(items))
	}
}

func TestMarkdownToADF_Blockquote(t *testing.T) {
	doc := mustADF(t, "> quoted text")

	content := asSlice(t, doc["content"])
	bq := asMap(t, content[0])
	if bq["type"] != "blockquote" {
		t.Fatalf("block type = %v, want %q", bq["type"], "blockquote")
	}
	para := asMap(t, asSlice(t, bq["content"])[0])
	text := asMap(t, asSlice(t, para["content"])[0])
	if text["text"] != "quoted text" {
		t.Errorf("blockquote text = %v, want %q", text["text"], "quoted text")
	}
}

func TestMarkdownToADF_Link(t *testing.T) {
	doc := mustADF(t, "see [the docs](https://example.com/docs)")

	para := asMap(t, asSlice(t, doc["content"])[0])
	nodes := asSlice(t, para["content"])

	var found bool
	for _, n := range nodes {
		node := asMap(t, n)
		marks, ok := node["marks"].([]any)
		if !ok {
			continue
		}
		for _, m := range marks {
			mark := asMap(t, m)
			if mark["type"] != "link" {
				continue
			}
			attrs := asMap(t, mark["attrs"])
			if attrs["href"] != "https://example.com/docs" {
				t.Errorf("link href = %v, want %q", attrs["href"], "https://example.com/docs")
			}
			if node["text"] != "the docs" {
				t.Errorf("link text = %v, want %q", node["text"], "the docs")
			}
			found = true
		}
	}
	if !found {
		t.Error("expected a text node with a link mark")
	}
}

func TestMarkdownToADF_LinkMarkDoesNotLeakToFollowingText(t *testing.T) {
	doc := mustADF(t, "before [link](https://example.com) after")

	para := asMap(t, asSlice(t, doc["content"])[0])
	nodes := asSlice(t, para["content"])

	var sawTrailingText bool
	for _, n := range nodes {
		node := asMap(t, n)
		text, _ := node["text"].(string)
		if !strings.Contains(text, "after") {
			continue
		}
		sawTrailingText = true
		if marks, ok := node["marks"].([]any); ok && len(marks) > 0 {
			t.Errorf("text node %q has marks %v, want none (link mark must not leak to a sibling)", text, marks)
		}
	}
	if !sawTrailingText {
		t.Fatal("expected a text node containing \"after\"")
	}
}

func TestMarkdownToADF_NestedAutolinkKeepsInnerHref(t *testing.T) {
	// goldmark parses "[a <inner>](outer)" as an AutoLink nested inside a
	// Link — deviating from CommonMark, which refuses nested links, but
	// reachable input nonetheless. withMark's type-only dedup on "link"
	// must not let the outer link's href silently overwrite the inner
	// autolink's own href.
	doc := mustADF(t, "[a <https://inner.example> b](https://outer.example)")

	para := asMap(t, asSlice(t, doc["content"])[0])
	var innerHref string
	var found bool
	for _, n := range asSlice(t, para["content"]) {
		node := asMap(t, n)
		if node["text"] != "https://inner.example" {
			continue
		}
		found = true
		marks := asSlice(t, node["marks"])
		for _, m := range marks {
			mark := asMap(t, m)
			if mark["type"] != "link" {
				continue
			}
			attrs := asMap(t, mark["attrs"])
			innerHref = fmt.Sprint(attrs["href"])
		}
	}
	if !found {
		t.Fatalf("expected a text node with value %q", "https://inner.example")
	}
	if innerHref != "https://inner.example" {
		t.Errorf("inner autolink href = %q, want %q (the outer link's href leaked in)", innerHref, "https://inner.example")
	}
}

func TestMarkdownToADF_ImagePreservesDestinationAsLink(t *testing.T) {
	// ADF has no plain image node outside "media" (which requires an
	// uploaded attachment id, not a bare URL), so the load-bearing
	// destination is preserved as a link on the alt text — the closest
	// faithful degradation available.
	doc := mustADF(t, "see ![diagram](https://img.example/d.png) here")

	href, found := linkMarkHref(t, doc)
	if !found {
		t.Fatalf("MarkdownToADF(image) produced no link mark; want the image destination preserved as a link")
	}
	if href != "https://img.example/d.png" {
		t.Errorf("image link href = %q, want %q", href, "https://img.example/d.png")
	}

	para := asMap(t, asSlice(t, doc["content"])[0])
	var sawAlt bool
	for _, n := range asSlice(t, para["content"]) {
		if asMap(t, n)["text"] == "diagram" {
			sawAlt = true
		}
	}
	if !sawAlt {
		t.Errorf("MarkdownToADF(image) = %+v, want alt text %q preserved", doc, "diagram")
	}
}

func TestMarkdownToADF_ImageRejectsDangerousScheme(t *testing.T) {
	doc := mustADF(t, "see ![diagram](javascript:alert(1)) here")
	if href, found := linkMarkHref(t, doc); found {
		t.Errorf("MarkdownToADF(image with javascript: scheme) produced a link mark with href %q; want it dropped", href)
	}
}

func TestMarkdownToADF_UnknownBlockFallsBackToPlainText(t *testing.T) {
	// convertBlockNode's default case previously returned nil for any
	// block-level node outside its supported vocabulary (e.g. a raw HTML
	// block), silently dropping the content with no trace. Mirror
	// walkInline's own default case, which falls back to plain text
	// rather than losing readable content. (<details> blocks are no
	// longer "unknown" — they convert to ADF expand nodes — so this
	// test uses <div> to exercise the generic fallback.)
	doc := mustADF(t, "before\n\n<div>some content</div>\n\nafter")

	content := asSlice(t, doc["content"])
	var sawFallbackText bool
	for _, c := range content {
		block := asMap(t, c)
		if block["type"] != "paragraph" {
			continue
		}
		for _, n := range asSlice(t, block["content"]) {
			node := asMap(t, n)
			text, _ := node["text"].(string)
			if strings.Contains(text, "<div>") {
				sawFallbackText = true
			}
		}
	}
	if !sawFallbackText {
		t.Errorf("MarkdownToADF(html block) = %+v, want the raw HTML block content preserved as fallback text somewhere, not silently dropped", doc)
	}
}

// linkMarkHref returns the href of the first "link" mark found among
// para's inline content, and whether one was found at all.
func linkMarkHref(t *testing.T, doc map[string]any) (href string, found bool) {
	t.Helper()
	para := asMap(t, asSlice(t, doc["content"])[0])
	for _, n := range asSlice(t, para["content"]) {
		node := asMap(t, n)
		marks, ok := node["marks"].([]any)
		if !ok {
			continue
		}
		for _, m := range marks {
			mark := asMap(t, m)
			if mark["type"] != "link" {
				continue
			}
			attrs := asMap(t, mark["attrs"])
			return fmt.Sprint(attrs["href"]), true
		}
	}
	return "", false
}

func TestMarkdownToADF_LinkRejectsDangerousSchemes(t *testing.T) {
	for _, src := range []string{
		"[click me](javascript:alert(1))",
		"[click me](data:text/html,<script>alert(1)</script>)",
		"[click me](vbscript:msgbox(1))",
	} {
		doc := mustADF(t, src)
		if _, found := linkMarkHref(t, doc); found {
			t.Errorf("MarkdownToADF(%q) produced a link mark; want the dangerous-scheme href dropped", src)
		}
	}
}

func TestMarkdownToADF_LinkAllowsSafeSchemes(t *testing.T) {
	for _, tc := range []struct{ src, want string }{
		{"[docs](https://example.com/docs)", "https://example.com/docs"},
		{"[docs](http://example.com/docs)", "http://example.com/docs"},
		{"[me](mailto:me@example.com)", "mailto:me@example.com"},
		{"[section](#section)", "#section"},
		{"[rel](/path/to/page)", "/path/to/page"},
	} {
		doc := mustADF(t, tc.src)
		href, found := linkMarkHref(t, doc)
		if !found {
			t.Errorf("MarkdownToADF(%q): expected a link mark, got none", tc.src)
			continue
		}
		if href != tc.want {
			t.Errorf("MarkdownToADF(%q) href = %q, want %q", tc.src, href, tc.want)
		}
	}
}

func TestMarkdownToADF_LinkRejectsProtocolRelativeHost(t *testing.T) {
	doc := mustADF(t, "[click me](//evil.example)")
	if href, found := linkMarkHref(t, doc); found {
		t.Errorf("MarkdownToADF(protocol-relative link) produced a link mark with href %q; want it dropped", href)
	}
}

func TestMarkdownToADF_AutoLinkRejectsDangerousScheme(t *testing.T) {
	// goldmark's autolink extension isn't enabled by default, so this
	// exercises the CommonMark <...> autolink form instead.
	doc := mustADF(t, "<javascript:alert(1)>")
	if _, found := linkMarkHref(t, doc); found {
		t.Errorf("MarkdownToADF(autolink with javascript: scheme) produced a link mark; want it dropped")
	}
}

// assertNoEmptyContainers recursively checks that no blockquote,
// bulletList, orderedList, or listItem node in the ADF tree has empty
// content, which violates ADF's minItems: 1 for those node types.
func assertNoEmptyContainers(t *testing.T, node any) {
	t.Helper()
	m, ok := node.(map[string]any)
	if !ok {
		return
	}
	nodeType, _ := m["type"].(string)
	content, hasContent := m["content"].([]any)
	switch nodeType {
	case "blockquote", "bulletList", "orderedList", "listItem":
		if hasContent && len(content) == 0 {
			t.Errorf("%s node has content: [], which violates ADF minItems: 1: %+v", nodeType, m)
		}
	}
	for _, c := range content {
		assertNoEmptyContainers(t, c)
	}
}

func TestMarkdownToADF_HeadingInsideBlockquoteDegradesToBoldParagraph(t *testing.T) {
	doc := mustADF(t, "> # title")

	bq := asMap(t, asSlice(t, doc["content"])[0])
	if bq["type"] != "blockquote" {
		t.Fatalf("block type = %v, want %q", bq["type"], "blockquote")
	}
	children := asSlice(t, bq["content"])
	if len(children) != 1 {
		t.Fatalf("blockquote content len = %d, want 1", len(children))
	}
	para := asMap(t, children[0])
	if para["type"] != "paragraph" {
		t.Errorf("heading-in-blockquote degraded type = %v, want %q (ADF blockquote schema doesn't allow heading children)", para["type"], "paragraph")
	}
	text := asMap(t, asSlice(t, para["content"])[0])
	if text["text"] != "title" {
		t.Errorf("degraded heading text = %v, want %q", text["text"], "title")
	}
	marks := asSlice(t, text["marks"])
	var sawStrong bool
	for _, mk := range marks {
		if asMap(t, mk)["type"] == "strong" {
			sawStrong = true
		}
	}
	if !sawStrong {
		t.Errorf("degraded heading marks = %v, want a strong mark", marks)
	}
	assertNoEmptyContainers(t, doc)
}

func TestMarkdownToADF_NestedBlockquoteFlattens(t *testing.T) {
	doc := mustADF(t, "> > inner")

	outer := asMap(t, asSlice(t, doc["content"])[0])
	if outer["type"] != "blockquote" {
		t.Fatalf("block type = %v, want %q", outer["type"], "blockquote")
	}
	for _, c := range asSlice(t, outer["content"]) {
		if asMap(t, c)["type"] == "blockquote" {
			t.Fatalf("outer blockquote content = %+v, want the nested blockquote flattened away (ADF blockquote schema doesn't allow blockquote children)", outer["content"])
		}
	}
	assertNoEmptyContainers(t, doc)
}

func TestMarkdownToADF_HeadingInsideListItemDegradesToBoldParagraph(t *testing.T) {
	doc := mustADF(t, "- # h")

	list := asMap(t, asSlice(t, doc["content"])[0])
	item := asMap(t, asSlice(t, list["content"])[0])
	para := asMap(t, asSlice(t, item["content"])[0])
	if para["type"] != "paragraph" {
		t.Errorf("heading-in-listItem degraded type = %v, want %q (ADF listItem schema doesn't allow heading children)", para["type"], "paragraph")
	}
	assertNoEmptyContainers(t, doc)
}

func TestMarkdownToADF_BlockquoteInsideListItemFlattens(t *testing.T) {
	doc := mustADF(t, "- > q")

	list := asMap(t, asSlice(t, doc["content"])[0])
	item := asMap(t, asSlice(t, list["content"])[0])
	for _, c := range asSlice(t, item["content"]) {
		if asMap(t, c)["type"] == "blockquote" {
			t.Fatalf("listItem content = %+v, want the blockquote flattened away (ADF listItem schema doesn't allow blockquote children)", item["content"])
		}
	}
	assertNoEmptyContainers(t, doc)
}

func TestMarkdownToADF_ThematicBreakInsideBlockquoteDropped(t *testing.T) {
	doc := mustADF(t, "> above\n>\n> ---\n>\n> below")

	bq := asMap(t, asSlice(t, doc["content"])[0])
	if bq["type"] != "blockquote" {
		t.Fatalf("block type = %v, want %q", bq["type"], "blockquote")
	}
	for _, c := range asSlice(t, bq["content"]) {
		if asMap(t, c)["type"] == "rule" {
			t.Errorf("blockquote content = %+v, want the rule dropped (ADF blockquote schema doesn't allow a rule child)", bq["content"])
		}
	}
	assertNoEmptyContainers(t, doc)
}

func TestMarkdownToADF_DeepNestingProducesNoEmptyContainers(t *testing.T) {
	// The maxADFWriteDepth cutoff truncates a deeply nested blockquote's
	// content, which — before the container-emptiness check — left an
	// innermost {"type":"blockquote","content":[]} at the cap boundary:
	// schema-invalid ADF Jira would reject outright. A sibling paragraph
	// keeps the overall doc non-empty despite the nested blockquote
	// collapsing entirely, so this exercises the container-emptiness
	// check on its own, independent of MarkdownToADF's separate
	// empty-doc error (see TestMarkdownToADF_AllContentDroppedReturnsError).
	const depth = 60 // > maxADFWriteDepth (50)
	src := strings.Repeat("> ", depth) + "leaf" + "\n\nsibling paragraph"
	doc := mustADF(t, src)
	assertNoEmptyContainers(t, doc)
}

func TestMarkdownToADF_AllContentDroppedReturnsError(t *testing.T) {
	// A chain of nested blockquotes deeper than maxADFWriteDepth, with no
	// other top-level content, collapses all the way up to an empty doc:
	// each level past the cutoff drops its empty container, which empties
	// its parent, and so on to the top. Posting that to Jira would create
	// a visibly empty comment with no error. MarkdownToADF must fail
	// instead of returning {"type":"doc","content":[]}.
	const depth = 60 // > maxADFWriteDepth (50)
	src := strings.Repeat("> ", depth) + "leaf"

	_, err := MarkdownToADF(src)
	if err == nil {
		t.Error("MarkdownToADF(all-dropped nested blockquotes) returned no error; want one rather than an empty ADF doc")
	}
}

func TestMarkdownToADF_OversizedInputReturnsError(t *testing.T) {
	src := strings.Repeat("a", maxMarkdownParseBytes+1)

	_, err := MarkdownToADF(src)
	if err == nil {
		t.Errorf("MarkdownToADF(%d bytes) returned no error; want one for input over the %d byte limit", len(src), maxMarkdownParseBytes)
	}
}

func TestMarkdownToADF_ThematicBreak(t *testing.T) {
	doc := mustADF(t, "above\n\n---\n\nbelow")

	content := asSlice(t, doc["content"])
	if len(content) != 3 {
		t.Fatalf("doc content len = %d, want 3", len(content))
	}
	rule := asMap(t, content[1])
	if rule["type"] != "rule" {
		t.Errorf("block type = %v, want %q", rule["type"], "rule")
	}
}

func TestMarkdownToADF_DeepNestingIsBounded(t *testing.T) {
	// Mirrors TestADFToPlainText_DeepNestingIsBounded: MarkdownToADF's
	// block/inline converters recurse once per markdown nesting level with
	// no cap, so deeply nested input (e.g. thousands of ">" blockquote
	// markers) must not be walked to full depth. A sibling paragraph keeps
	// the doc non-empty despite the nested blockquote collapsing entirely
	// past the depth cap.
	const depth = 10000
	src := strings.Repeat("> ", depth) + "leaf" + "\n\nsibling paragraph"

	doc := mustADF(t, src)

	walked := 0
	content := doc["content"]
	for {
		items, ok := content.([]any)
		if !ok || len(items) == 0 {
			break
		}
		node, ok := items[0].(map[string]any)
		if !ok || node["type"] != "blockquote" {
			break
		}
		walked++
		content = node["content"]
	}
	if walked >= depth {
		t.Errorf("MarkdownToADF walked all %d nesting levels; want it capped well below that", depth)
	}
}

func TestMarkdownToADF_ParseTimeIsBounded(t *testing.T) {
	// maxADFWriteDepth only bounds the post-parse AST walk; goldmark's own
	// Parse() is ~O(N^2) on deeply nested blockquotes and dominates the
	// cost long before the walk-depth cap is ever reached (confirmed by
	// benchmark: 80,000 nesting levels take ~3.2s to parse alone). Without
	// an input size limit rejected before Parse(), MarkdownToADF blocks
	// for seconds on adversarial input regardless of maxADFWriteDepth.
	// This input is well over maxMarkdownParseBytes, so MarkdownToADF
	// returns an error rather than parsing it at all — both return values
	// are discarded since this test only cares about timing.
	const depth = 80000
	src := strings.Repeat("> ", depth) + "leaf"

	start := time.Now()
	MarkdownToADF(src) //nolint:errcheck // timing-only test, error is expected
	elapsed := time.Since(start)

	const budget = 750 * time.Millisecond
	if elapsed > budget {
		t.Errorf("MarkdownToADF(%d nesting levels) took %s, want under %s (parse time must be bounded independent of maxADFWriteDepth)", depth, elapsed, budget)
	}
}

func TestMarkdownToADF_HardBreak(t *testing.T) {
	doc := mustADF(t, "line one  \nline two")

	para := asMap(t, asSlice(t, doc["content"])[0])
	nodes := asSlice(t, para["content"])

	var foundHardBreak bool
	for _, n := range nodes {
		node := asMap(t, n)
		if node["type"] == "hardBreak" {
			foundHardBreak = true
		}
	}
	if !foundHardBreak {
		t.Errorf("expected a hardBreak node in paragraph content, got %+v", nodes)
	}
}

// ---------------------------------------------------------------------------
// ADFToPlainText
// ---------------------------------------------------------------------------

func TestADFToPlainText_String(t *testing.T) {
	got := ADFToPlainText("plain text body")
	if got != "plain text body" {
		t.Errorf("ADFToPlainText(string) = %q, want %q", got, "plain text body")
	}
}

func TestADFToPlainText_Paragraph(t *testing.T) {
	adf := map[string]any{
		"type":    "doc",
		"version": 1,
		"content": []any{
			map[string]any{
				"type": "paragraph",
				"content": []any{
					map[string]any{"type": "text", "text": "hello there"},
				},
			},
		},
	}
	got := ADFToPlainText(adf)
	if got != "hello there" {
		t.Errorf("ADFToPlainText(paragraph) = %q, want %q", got, "hello there")
	}
}

func TestADFToPlainText_MultiParagraphAndHeading(t *testing.T) {
	adf := map[string]any{
		"type":    "doc",
		"version": 1,
		"content": []any{
			map[string]any{
				"type": "heading",
				"content": []any{
					map[string]any{"type": "text", "text": "Title"},
				},
			},
			map[string]any{
				"type": "paragraph",
				"content": []any{
					map[string]any{"type": "text", "text": "Body"},
				},
			},
		},
	}
	got := ADFToPlainText(adf)
	want := "Title\nBody"
	if got != want {
		t.Errorf("ADFToPlainText(heading+paragraph) = %q, want %q", got, want)
	}
}

func TestADFToPlainText_List(t *testing.T) {
	adf := map[string]any{
		"type": "bulletList",
		"content": []any{
			map[string]any{
				"type": "listItem",
				"content": []any{
					map[string]any{
						"type": "paragraph",
						"content": []any{
							map[string]any{"type": "text", "text": "item one"},
						},
					},
				},
			},
			map[string]any{
				"type": "listItem",
				"content": []any{
					map[string]any{
						"type": "paragraph",
						"content": []any{
							map[string]any{"type": "text", "text": "item two"},
						},
					},
				},
			},
		},
	}
	got := ADFToPlainText(adf)
	want := "item one\nitem two"
	if got != want {
		t.Errorf("ADFToPlainText(list) = %q, want %q", got, want)
	}
}

func TestADFToPlainText_HardBreak(t *testing.T) {
	adf := map[string]any{
		"type": "paragraph",
		"content": []any{
			map[string]any{"type": "text", "text": "line one"},
			map[string]any{"type": "hardBreak"},
			map[string]any{"type": "text", "text": "line two"},
		},
	}
	got := ADFToPlainText(adf)
	want := "line one\nline two"
	if got != want {
		t.Errorf("ADFToPlainText(hardBreak) = %q, want %q", got, want)
	}
}

func TestADFToPlainText_DeepNestingIsBounded(t *testing.T) {
	// Mirrors jirapoll's TestExtractPlainText_DeepNestingIsBounded: the
	// depth cap must match since this is a fresh implementation of the
	// same defensive behavior for attacker-controlled ADF bodies.
	const depth = 10000
	root := map[string]any{
		"type": "paragraph",
		"text": "level-0",
	}
	leaf := root
	for i := 1; i < depth; i++ {
		child := map[string]any{
			"type": "paragraph",
			"text": fmt.Sprintf("level-%d", i),
		}
		leaf["content"] = []any{child}
		leaf = child
	}

	got := ADFToPlainText(root)
	if !strings.HasPrefix(got, "level-0") {
		t.Errorf("ADFToPlainText(deeply nested ADF) = %q, want it to start with %q", got, "level-0")
	}
	if strings.Contains(got, fmt.Sprintf("level-%d", depth-1)) {
		t.Errorf("ADFToPlainText(deeply nested ADF) walked all %d levels; want it capped well below that", depth)
	}
}

func TestADFToPlainText_UnexpectedType(t *testing.T) {
	got := ADFToPlainText(42)
	if got != "" {
		t.Errorf("ADFToPlainText(int) = %q, want empty string", got)
	}
}

// ---------------------------------------------------------------------------
// ADFToMarkdown
// ---------------------------------------------------------------------------

func TestADFToMarkdown_String(t *testing.T) {
	got := ADFToMarkdown("plain text body")
	if got != "plain text body" {
		t.Errorf("ADFToMarkdown(string) = %q, want %q", got, "plain text body")
	}
}

func TestADFToMarkdown_UnexpectedType(t *testing.T) {
	got := ADFToMarkdown(42)
	if got != "" {
		t.Errorf("ADFToMarkdown(int) = %q, want empty string", got)
	}
}

func TestADFToMarkdown_Paragraph(t *testing.T) {
	adf := map[string]any{
		"type":    "doc",
		"version": 1,
		"content": []any{
			map[string]any{
				"type": "paragraph",
				"content": []any{
					map[string]any{"type": "text", "text": "hello there"},
				},
			},
		},
	}
	got := ADFToMarkdown(adf)
	if got != "hello there" {
		t.Errorf("ADFToMarkdown(paragraph) = %q, want %q", got, "hello there")
	}
}

func TestADFToMarkdown_MultipleParagraphsSeparatedByBlankLine(t *testing.T) {
	// Unlike ADFToPlainText, which joins blocks with a single newline,
	// Markdown paragraphs must be separated by a blank line, or a
	// Markdown parser (including this package's own MarkdownToADF) reads
	// them back as one paragraph instead of two.
	adf := map[string]any{
		"type": "doc",
		"content": []any{
			map[string]any{
				"type":    "paragraph",
				"content": []any{map[string]any{"type": "text", "text": "first"}},
			},
			map[string]any{
				"type":    "paragraph",
				"content": []any{map[string]any{"type": "text", "text": "second"}},
			},
		},
	}
	got := ADFToMarkdown(adf)
	want := "first\n\nsecond"
	if got != want {
		t.Errorf("ADFToMarkdown(two paragraphs) = %q, want %q", got, want)
	}
}

func TestADFToMarkdown_Heading(t *testing.T) {
	adf := map[string]any{
		"type": "doc",
		"content": []any{
			map[string]any{
				"type":    "heading",
				"attrs":   map[string]any{"level": 2},
				"content": []any{map[string]any{"type": "text", "text": "Title"}},
			},
		},
	}
	got := ADFToMarkdown(adf)
	want := "## Title"
	if got != want {
		t.Errorf("ADFToMarkdown(heading level 2) = %q, want %q", got, want)
	}
}

func TestADFToMarkdown_StrongEmCodeMarks(t *testing.T) {
	adf := map[string]any{
		"type": "doc",
		"content": []any{
			map[string]any{
				"type": "paragraph",
				"content": []any{
					map[string]any{"type": "text", "text": "bold", "marks": []any{map[string]any{"type": "strong"}}},
					map[string]any{"type": "text", "text": " and "},
					map[string]any{"type": "text", "text": "italic", "marks": []any{map[string]any{"type": "em"}}},
					map[string]any{"type": "text", "text": " and "},
					map[string]any{"type": "text", "text": "code", "marks": []any{map[string]any{"type": "code"}}},
				},
			},
		},
	}
	got := ADFToMarkdown(adf)
	want := "**bold** and *italic* and `code`"
	if got != want {
		t.Errorf("ADFToMarkdown(marks) = %q, want %q", got, want)
	}
}

func TestADFToMarkdown_MultipleMarksNestInApplicationOrder(t *testing.T) {
	// MarkdownToADF builds a text node's marks slice outer-to-inner (see
	// walkInline's Emphasis/Link cases): the outermost enclosing mark is
	// appended first, so a "**[text](url)**" source produces
	// marks: [strong, link]. Rendering must reverse that order, applying
	// link (innermost) first and strong (outermost) last, or the
	// asymmetric marks (link, strong) round-trip into the wrong nesting.
	adf := map[string]any{
		"type": "doc",
		"content": []any{
			map[string]any{
				"type": "paragraph",
				"content": []any{
					map[string]any{
						"type": "text",
						"text": "text",
						"marks": []any{
							map[string]any{"type": "strong"},
							map[string]any{"type": "link", "attrs": map[string]any{"href": "https://example.com"}},
						},
					},
				},
			},
		},
	}
	got := ADFToMarkdown(adf)
	want := "**[text](https://example.com)**"
	if got != want {
		t.Errorf("ADFToMarkdown(nested marks) = %q, want %q", got, want)
	}
}

func TestADFToMarkdown_Link(t *testing.T) {
	adf := map[string]any{
		"type": "doc",
		"content": []any{
			map[string]any{
				"type": "paragraph",
				"content": []any{
					map[string]any{
						"type": "text",
						"text": "the docs",
						"marks": []any{
							map[string]any{"type": "link", "attrs": map[string]any{"href": "https://example.com/docs"}},
						},
					},
				},
			},
		},
	}
	got := ADFToMarkdown(adf)
	want := "[the docs](https://example.com/docs)"
	if got != want {
		t.Errorf("ADFToMarkdown(link) = %q, want %q", got, want)
	}
}

func TestADFToMarkdown_CodeBlockWithLanguage(t *testing.T) {
	adf := map[string]any{
		"type": "doc",
		"content": []any{
			map[string]any{
				"type":  "codeBlock",
				"attrs": map[string]any{"language": "go"},
				"content": []any{
					map[string]any{"type": "text", "text": "fmt.Println(\"hi\")"},
				},
			},
		},
	}
	got := ADFToMarkdown(adf)
	want := "```go\nfmt.Println(\"hi\")\n```"
	if got != want {
		t.Errorf("ADFToMarkdown(codeBlock) = %q, want %q", got, want)
	}
}

func TestADFToMarkdown_CodeBlockEmpty(t *testing.T) {
	// An ADF codeBlock with no text children must render as an empty fenced
	// block (no blank line between the fences), not "```\n\n```" which
	// introduces a spurious blank line on round-trip.
	adf := map[string]any{
		"type": "doc",
		"content": []any{
			map[string]any{"type": "codeBlock"},
		},
	}
	got := ADFToMarkdown(adf)
	want := "```\n```"
	if got != want {
		t.Errorf("ADFToMarkdown(empty codeBlock) = %q, want %q", got, want)
	}
}

func TestADFToMarkdown_CodeBlockEmptyWithLanguage(t *testing.T) {
	adf := map[string]any{
		"type": "doc",
		"content": []any{
			map[string]any{
				"type":  "codeBlock",
				"attrs": map[string]any{"language": "go"},
			},
		},
	}
	got := ADFToMarkdown(adf)
	want := "```go\n```"
	if got != want {
		t.Errorf("ADFToMarkdown(empty codeBlock with language) = %q, want %q", got, want)
	}
}

func TestADFToMarkdown_CodeBlockWithoutLanguage(t *testing.T) {
	adf := map[string]any{
		"type": "doc",
		"content": []any{
			map[string]any{
				"type":    "codeBlock",
				"content": []any{map[string]any{"type": "text", "text": "plain"}},
			},
		},
	}
	got := ADFToMarkdown(adf)
	want := "```\nplain\n```"
	if got != want {
		t.Errorf("ADFToMarkdown(codeBlock without language) = %q, want %q", got, want)
	}
}

func TestADFToMarkdown_CodeBlockFenceLongerThanContentBackticks(t *testing.T) {
	// A fixed 3-backtick fence can be broken out of by content that
	// itself contains a ``` line: the closing fence needs only be *at
	// least as many* backticks as the opener per CommonMark, so a
	// content line of exactly 3 backticks would close the block early.
	adf := map[string]any{
		"type": "doc",
		"content": []any{
			map[string]any{
				"type":    "codeBlock",
				"content": []any{map[string]any{"type": "text", "text": "example:\n```go\nfunc main() {}\n```"}},
			},
		},
	}
	got := ADFToMarkdown(adf)
	want := "````\nexample:\n```go\nfunc main() {}\n```\n````"
	if got != want {
		t.Errorf("ADFToMarkdown(codeBlock with fence-length content) = %q, want %q", got, want)
	}
}

func TestADFToMarkdown_CodeBlockSanitizesLanguageAttr(t *testing.T) {
	adf := map[string]any{
		"type": "doc",
		"content": []any{
			map[string]any{
				"type":    "codeBlock",
				"attrs":   map[string]any{"language": "go\n```\n# injected"},
				"content": []any{map[string]any{"type": "text", "text": "code"}},
			},
		},
	}
	got := ADFToMarkdown(adf)
	want := "```\ncode\n```"
	if got != want {
		t.Errorf("ADFToMarkdown(codeBlock with hostile language attr) = %q, want %q (language dropped)", got, want)
	}
}

func TestADFToMarkdown_CodeBlockAllowsSafeLanguageAttr(t *testing.T) {
	adf := map[string]any{
		"type": "doc",
		"content": []any{
			map[string]any{
				"type":    "codeBlock",
				"attrs":   map[string]any{"language": "c++"},
				"content": []any{map[string]any{"type": "text", "text": "code"}},
			},
		},
	}
	got := ADFToMarkdown(adf)
	want := "```c++\ncode\n```"
	if got != want {
		t.Errorf("ADFToMarkdown(codeBlock with safe language attr) = %q, want %q", got, want)
	}
}

func TestADFToMarkdown_BulletList(t *testing.T) {
	adf := map[string]any{
		"type": "doc",
		"content": []any{
			map[string]any{
				"type": "bulletList",
				"content": []any{
					map[string]any{
						"type":    "listItem",
						"content": []any{map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": "one"}}}},
					},
					map[string]any{
						"type":    "listItem",
						"content": []any{map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": "two"}}}},
					},
				},
			},
		},
	}
	got := ADFToMarkdown(adf)
	want := "- one\n- two"
	if got != want {
		t.Errorf("ADFToMarkdown(bulletList) = %q, want %q", got, want)
	}
}

func TestADFToMarkdown_OrderedListStartsAtAttrsOrder(t *testing.T) {
	adf := map[string]any{
		"type": "doc",
		"content": []any{
			map[string]any{
				"type":  "orderedList",
				"attrs": map[string]any{"order": 5},
				"content": []any{
					map[string]any{
						"type":    "listItem",
						"content": []any{map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": "five"}}}},
					},
					map[string]any{
						"type":    "listItem",
						"content": []any{map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": "six"}}}},
					},
				},
			},
		},
	}
	got := ADFToMarkdown(adf)
	want := "5. five\n6. six"
	if got != want {
		t.Errorf("ADFToMarkdown(orderedList) = %q, want %q", got, want)
	}
}

func TestADFToMarkdown_Blockquote(t *testing.T) {
	adf := map[string]any{
		"type": "doc",
		"content": []any{
			map[string]any{
				"type": "blockquote",
				"content": []any{
					map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": "quoted text"}}},
				},
			},
		},
	}
	got := ADFToMarkdown(adf)
	want := "> quoted text"
	if got != want {
		t.Errorf("ADFToMarkdown(blockquote) = %q, want %q", got, want)
	}
}

func TestADFToMarkdown_Rule(t *testing.T) {
	adf := map[string]any{
		"type": "doc",
		"content": []any{
			map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": "above"}}},
			map[string]any{"type": "rule"},
			map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": "below"}}},
		},
	}
	got := ADFToMarkdown(adf)
	want := "above\n\n---\n\nbelow"
	if got != want {
		t.Errorf("ADFToMarkdown(rule) = %q, want %q", got, want)
	}
}

func TestADFToMarkdown_HardBreak(t *testing.T) {
	adf := map[string]any{
		"type": "paragraph",
		"content": []any{
			map[string]any{"type": "text", "text": "line one"},
			map[string]any{"type": "hardBreak"},
			map[string]any{"type": "text", "text": "line two"},
		},
	}
	got := ADFToMarkdown(adf)
	// A backslash-newline hard break, rather than trailing spaces:
	// trailing whitespace is silently stripped by enough editors, git
	// diffs, and web forms that a hard break built from it wouldn't
	// reliably survive a copy/paste round trip.
	want := "line one\\\nline two"
	if got != want {
		t.Errorf("ADFToMarkdown(hardBreak) = %q, want %q", got, want)
	}
}

func TestADFToMarkdown_UnknownContainerNodeRecursesIntoBlockChildren(t *testing.T) {
	// Unknown ADF container types (panel, table, taskList) wrap
	// block-level content (paragraphs, tableRows, ...), not inline text.
	// adfMarkdownInline alone can't see any of it, since it only reads
	// direct children's top-level "text" fields. (expand is no longer
	// unknown — it has dedicated <details>/<summary> handling.)
	for _, tc := range []struct {
		name string
		node map[string]any
		want string
	}{
		{
			name: "panel",
			node: map[string]any{
				"type":  "panel",
				"attrs": map[string]any{"panelType": "info"},
				"content": []any{
					map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": "important warning text"}}},
				},
			},
			want: "important warning text",
		},
		{
			name: "table",
			node: map[string]any{
				"type": "table",
				"content": []any{
					map[string]any{"type": "tableRow", "content": []any{
						map[string]any{"type": "tableCell", "content": []any{
							map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": "a"}}},
						}},
						map[string]any{"type": "tableCell", "content": []any{
							map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": "b"}}},
						}},
					}},
				},
			},
			want: "a\n\nb",
		},
		{
			name: "taskList",
			node: map[string]any{
				"type": "taskList",
				"content": []any{
					map[string]any{
						"type":    "taskItem",
						"attrs":   map[string]any{"localId": "1", "state": "TODO"},
						"content": []any{map[string]any{"type": "text", "text": "buy milk"}},
					},
				},
			},
			want: "buy milk",
		},
	} {
		doc := map[string]any{"type": "doc", "content": []any{tc.node}}
		got := ADFToMarkdown(doc)
		if got != tc.want {
			t.Errorf("%s: ADFToMarkdown(%+v) = %q, want %q (content dropped)", tc.name, tc.node, got, tc.want)
		}
	}
}

func TestADFToMarkdown_InlineAtomNodesFallBackToText(t *testing.T) {
	// mention/emoji/status/date/inlineCard carry their visible text in
	// attrs, not a top-level "text" field; without a case for them,
	// adfMarkdownInline silently drops them.
	for _, tc := range []struct {
		name string
		node map[string]any
		want string
	}{
		{
			name: "mention",
			node: map[string]any{"type": "mention", "attrs": map[string]any{"id": "557058:abc", "text": "@Alice"}},
			want: "@Alice",
		},
		{
			name: "emoji with text",
			node: map[string]any{"type": "emoji", "attrs": map[string]any{"shortName": ":smile:", "text": "😄"}},
			want: "😄",
		},
		{
			name: "emoji falls back to shortName",
			node: map[string]any{"type": "emoji", "attrs": map[string]any{"shortName": ":smile:"}},
			want: ":smile:",
		},
		{
			name: "status",
			node: map[string]any{"type": "status", "attrs": map[string]any{"text": "DONE", "color": "green"}},
			want: "DONE",
		},
		{
			name: "date",
			node: map[string]any{"type": "date", "attrs": map[string]any{"timestamp": "1584645600000"}},
			want: "1584645600000",
		},
		{
			name: "inlineCard",
			node: map[string]any{"type": "inlineCard", "attrs": map[string]any{"url": "https://example.com/issue/1"}},
			want: "https://example.com/issue/1",
		},
	} {
		para := map[string]any{
			"type": "paragraph",
			"content": []any{
				map[string]any{"type": "text", "text": "ping "},
				tc.node,
				map[string]any{"type": "text", "text": " please review"},
			},
		}
		got := ADFToMarkdown(para)
		want := "ping " + tc.want + " please review"
		if got != want {
			t.Errorf("%s: ADFToMarkdown(%+v) = %q, want %q", tc.name, tc.node, got, want)
		}
	}
}

func TestADFToMarkdown_DeepNestingIsBounded(t *testing.T) {
	// Unlike ADFToPlainText's single generic node walker, ADFToMarkdown
	// only recurses through container types that can genuinely nest in
	// real ADF (blockquote, bulletList/orderedList, listItem); a
	// paragraph's own content is inline runs, not further blocks. So the
	// attacker-controlled-nesting vector here is a blockquote chain,
	// mirroring MarkdownToADF's own deep-blockquote-nesting tests, rather
	// than ADFToPlainText's paragraph-chain shape.
	const depth = 10000
	leaf := map[string]any{
		"type":    "paragraph",
		"content": []any{map[string]any{"type": "text", "text": "leaf"}},
	}
	nested := leaf
	for i := 0; i < depth; i++ {
		nested = map[string]any{"type": "blockquote", "content": []any{nested}}
	}
	doc := map[string]any{
		"type": "doc",
		"content": []any{
			nested,
			map[string]any{
				"type":    "paragraph",
				"content": []any{map[string]any{"type": "text", "text": "sibling"}},
			},
		},
	}

	got := ADFToMarkdown(doc)
	if strings.Contains(got, "leaf") {
		t.Errorf("ADFToMarkdown(%d nested blockquotes) walked all the way to the leaf; want it capped well below that", depth)
	}
	if !strings.Contains(got, "sibling") {
		t.Errorf("ADFToMarkdown(deeply nested blockquote + sibling) = %q, want it to still contain the sibling paragraph", got)
	}
}

func TestADFToMarkdown_Float64Attrs(t *testing.T) {
	// JSON-decoded ADF has float64 for numeric attrs, not int. Verify
	// ADFToMarkdown handles this correctly (the real shape from json.Decode).
	heading := map[string]any{
		"type":    "doc",
		"version": float64(1),
		"content": []any{
			map[string]any{
				"type":  "heading",
				"attrs": map[string]any{"level": float64(3)},
				"content": []any{
					map[string]any{"type": "text", "text": "Title"},
				},
			},
		},
	}
	got := ADFToMarkdown(heading)
	if got != "### Title" {
		t.Errorf("ADFToMarkdown(heading with float64 level 3) = %q, want %q", got, "### Title")
	}

	orderedList := map[string]any{
		"type":    "doc",
		"version": float64(1),
		"content": []any{
			map[string]any{
				"type":  "orderedList",
				"attrs": map[string]any{"order": float64(5)},
				"content": []any{
					map[string]any{
						"type": "listItem",
						"content": []any{
							map[string]any{
								"type":    "paragraph",
								"content": []any{map[string]any{"type": "text", "text": "item"}},
							},
						},
					},
				},
			},
		},
	}
	got = ADFToMarkdown(orderedList)
	if got != "5. item" {
		t.Errorf("ADFToMarkdown(orderedList with float64 order 5) = %q, want %q", got, "5. item")
	}
}

func TestADFToMarkdown_HeadingLevelOutOfRangeDoesNotPanic(t *testing.T) {
	for _, tc := range []struct {
		name  string
		level any
		want  string
	}{
		{"negative int", -1, "# hi"},
		{"negative float64", float64(-1), "# hi"},
		{"zero", 0, "# hi"},
		{"seven", 7, "###### hi"},
	} {
		adf := map[string]any{
			"type": "doc",
			"content": []any{
				map[string]any{
					"type":    "heading",
					"attrs":   map[string]any{"level": tc.level},
					"content": []any{map[string]any{"type": "text", "text": "hi"}},
				},
			},
		}
		got := ADFToMarkdown(adf)
		if got != tc.want {
			t.Errorf("%s: ADFToMarkdown(heading level %v) = %q, want %q", tc.name, tc.level, got, tc.want)
		}
	}
}

func TestADFToMarkdown_OrderedListOrderOutOfRangeIsBounded(t *testing.T) {
	adf := map[string]any{
		"type": "doc",
		"content": []any{
			map[string]any{
				"type":  "orderedList",
				"attrs": map[string]any{"order": -5},
				"content": []any{
					map[string]any{
						"type":    "listItem",
						"content": []any{map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": "item"}}}},
					},
				},
			},
		},
	}
	got := ADFToMarkdown(adf)
	want := "0. item"
	if got != want {
		t.Errorf("ADFToMarkdown(orderedList order -5) = %q, want %q", got, want)
	}
}

func TestADFToMarkdown_EscapesMarkdownSignificantChars(t *testing.T) {
	// Jira-native ADF (not from a MarkdownToADF round-trip) can contain
	// literal markdown-significant characters in text nodes. Without
	// escaping, wrapping "a * b" in **...** produces "**a * b**" which
	// is ambiguous markdown.
	adf := map[string]any{
		"type": "doc",
		"content": []any{
			map[string]any{
				"type": "paragraph",
				"content": []any{
					map[string]any{"type": "text", "text": "a * b and [c]"},
				},
			},
		},
	}
	got := ADFToMarkdown(adf)
	want := `a \* b and \[c\]`
	if got != want {
		t.Errorf("ADFToMarkdown(text with markdown chars) = %q, want %q", got, want)
	}
}

func TestADFToMarkdown_EscapesLineLeadingBlockSyntax(t *testing.T) {
	// Unmarked text that begins a rendered line with block syntax
	// changes node type on a round trip through MarkdownToADF (e.g. a
	// paragraph "# not a heading" reparses as a real heading), even
	// though escapeMDText already covers inline-significant characters.
	for _, tc := range []struct {
		name, text, want string
	}{
		{"heading hash", "# not a heading", `\# not a heading`},
		{"bullet dash", "- not a list item", `\- not a list item`},
		{"blockquote", "> not a quote", `\> not a quote`},
		{"plus list", "+ not a list item", `\+ not a list item`},
		{"ordered list dot", "1. not a list item", `1\. not a list item`},
		{"ordered list paren", "1) not a list item", `1\) not a list item`},
		{"mid-text hash not escaped", "see the C# language", "see the C# language"},
		{"mid-text dash not escaped", "well-known issue", "well-known issue"},
	} {
		adf := map[string]any{
			"type":    "paragraph",
			"content": []any{map[string]any{"type": "text", "text": tc.text}},
		}
		got := ADFToMarkdown(adf)
		if got != tc.want {
			t.Errorf("%s: ADFToMarkdown(%q) = %q, want %q", tc.name, tc.text, got, tc.want)
		}
	}
}

func TestADFToMarkdown_LineLeadingBlockSyntaxRoundTripsAsParagraph(t *testing.T) {
	for _, text := range []string{"# not a heading", "- not a list item", "> not a quote", "1. not a list item"} {
		adf := map[string]any{
			"type":    "paragraph",
			"content": []any{map[string]any{"type": "text", "text": text}},
		}
		md := ADFToMarkdown(adf)
		doc, err := MarkdownToADF(md)
		if err != nil {
			t.Fatalf("MarkdownToADF(%q) returned unexpected error: %v", md, err)
		}
		block := asMap(t, asSlice(t, doc["content"])[0])
		if block["type"] != "paragraph" {
			t.Errorf("round trip of %q via markdown %q reparsed as %v, want paragraph", text, md, block["type"])
		}
	}
}

func TestADFToMarkdown_EscapesAmpersand(t *testing.T) {
	// escapeMDText doesn't escape "&"; textValue on the MarkdownToADF
	// side resolves HTML entity/numeric references, so literal "&copy;"
	// text mutates into "©" on a round trip without escaping.
	adf := map[string]any{
		"type":    "paragraph",
		"content": []any{map[string]any{"type": "text", "text": "&copy; and &amp; stay literal"}},
	}
	got := ADFToMarkdown(adf)
	want := `\&copy; and \&amp; stay literal`
	if got != want {
		t.Errorf("ADFToMarkdown(ampersand text) = %q, want %q", got, want)
	}

	doc, err := MarkdownToADF(got)
	if err != nil {
		t.Fatalf("MarkdownToADF(%q) returned unexpected error: %v", got, err)
	}
	para := asMap(t, asSlice(t, doc["content"])[0])
	text := asMap(t, asSlice(t, para["content"])[0])
	wantText := "&copy; and &amp; stay literal"
	if text["text"] != wantText {
		t.Errorf("round trip text = %q, want %q", text["text"], wantText)
	}
}

func TestADFToMarkdown_NoEscapeInsideCodeMark(t *testing.T) {
	// Text inside a code span is verbatim in Markdown; escaping would
	// insert visible backslashes.
	adf := map[string]any{
		"type": "doc",
		"content": []any{
			map[string]any{
				"type": "paragraph",
				"content": []any{
					map[string]any{
						"type":  "text",
						"text":  "a * b",
						"marks": []any{map[string]any{"type": "code"}},
					},
				},
			},
		},
	}
	got := ADFToMarkdown(adf)
	want := "`a * b`"
	if got != want {
		t.Errorf("ADFToMarkdown(code with markdown chars) = %q, want %q", got, want)
	}
}

func TestADFToMarkdown_CoalescesAdjacentSameMarkedTextNodes(t *testing.T) {
	// Real Jira-Cloud-authored ADF commonly splits one bold run across
	// adjacent text nodes carrying identical marks. Wrapping each node
	// independently produces "**hello ****world**", which re-parses
	// with the middle "****" as literal asterisks rather than as one
	// continuous bold run.
	adf := map[string]any{
		"type": "doc",
		"content": []any{
			map[string]any{
				"type": "paragraph",
				"content": []any{
					map[string]any{"type": "text", "text": "hello ", "marks": []any{map[string]any{"type": "strong"}}},
					map[string]any{"type": "text", "text": "world", "marks": []any{map[string]any{"type": "strong"}}},
				},
			},
		},
	}
	got := ADFToMarkdown(adf)
	want := "**hello world**"
	if got != want {
		t.Errorf("ADFToMarkdown(adjacent same-marked text) = %q, want %q", got, want)
	}

	doc, err := MarkdownToADF(got)
	if err != nil {
		t.Fatalf("MarkdownToADF(%q) returned unexpected error: %v", got, err)
	}
	para := asMap(t, asSlice(t, doc["content"])[0])
	text := asMap(t, asSlice(t, para["content"])[0])
	if text["text"] != "hello world" {
		t.Errorf("round trip text = %q, want %q", text["text"], "hello world")
	}
	marks := asSlice(t, text["marks"])
	if len(marks) != 1 || asMap(t, marks[0])["type"] != "strong" {
		t.Errorf("round trip marks = %v, want a single strong mark", marks)
	}
}

func TestADFToMarkdown_MarkSpanningSoftBreakRoundTrips(t *testing.T) {
	// walkInline's soft-break case emits a " " text node carrying
	// inherited marks, so writing "**bold\nsoft**" through
	// MarkdownToADF produces three adjacent strong-marked text nodes:
	// "bold", " ", "soft". Reading that back naively (wrapping each
	// independently) produces "**bold**** ****soft**", which does not
	// round trip.
	src := "**bold\nsoft**"
	doc := mustADF(t, src)
	got := ADFToMarkdown(doc)
	want := "**bold soft**"
	if got != want {
		t.Errorf("ADFToMarkdown(MarkdownToADF(%q)) = %q, want %q", src, got, want)
	}
}

func TestADFToMarkdown_HoistsWhitespaceOutsideEmphasisDelimiters(t *testing.T) {
	// A single marked text node with leading/trailing whitespace can't
	// be wrapped directly ("**hello **") since CommonMark's flanking
	// rule refuses to treat a "**" preceded by whitespace as a closer;
	// the whitespace must be hoisted outside the delimiters.
	adf := map[string]any{
		"type":    "paragraph",
		"content": []any{map[string]any{"type": "text", "text": " hello ", "marks": []any{map[string]any{"type": "strong"}}}},
	}
	got := ADFToMarkdown(adf)
	want := " **hello** "
	if got != want {
		t.Errorf("ADFToMarkdown(leading/trailing whitespace strong) = %q, want %q", got, want)
	}

	doc, err := MarkdownToADF(got)
	if err != nil {
		t.Fatalf("MarkdownToADF(%q) returned unexpected error: %v", got, err)
	}
	para := asMap(t, asSlice(t, doc["content"])[0])
	content := asSlice(t, para["content"])
	var boldFound bool
	for _, item := range content {
		m := asMap(t, item)
		if m["text"] == "hello" {
			marks := asSlice(t, m["marks"])
			if len(marks) == 1 && asMap(t, marks[0])["type"] == "strong" {
				boldFound = true
			}
		}
	}
	if !boldFound {
		t.Errorf("round trip content = %v, want a strong-marked %q text node", content, "hello")
	}
}

func TestADFToMarkdown_RoundTripsThroughMarkdownToADF(t *testing.T) {
	for _, src := range []string{
		"hello world",
		"**bold** and *italic* and `code`",
		"# Heading\n\nsome body text",
		"- one\n- two",
		"> quoted text",
		"[the docs](https://example.com/docs)",
	} {
		doc := mustADF(t, src)
		got := ADFToMarkdown(doc)
		if got != src {
			t.Errorf("ADFToMarkdown(MarkdownToADF(%q)) = %q, want the original markdown back unchanged", src, got)
		}
	}
}

// ---------------------------------------------------------------------------
// MarkdownToADF — <details> → expand
// ---------------------------------------------------------------------------

func TestMarkdownToADF_DetailsToExpandNode(t *testing.T) {
	// A <details><summary>…</summary>…</details> HTML block should
	// convert to an ADF expand node, not fall back to raw text.
	doc := mustADF(t, "text\n\n<details><summary>Prior run</summary>\n- item one\n- item two\n</details>\n\nmore text")

	content := asSlice(t, doc["content"])
	if len(content) != 3 {
		t.Fatalf("doc content len = %d, want 3 (paragraph, expand, paragraph)", len(content))
	}

	// First block: paragraph "text"
	para := asMap(t, content[0])
	if para["type"] != "paragraph" {
		t.Errorf("block 0 type = %v, want %q", para["type"], "paragraph")
	}

	// Second block: expand
	expand := asMap(t, content[1])
	if expand["type"] != "expand" {
		t.Fatalf("block 1 type = %v, want %q", expand["type"], "expand")
	}
	attrs := asMap(t, expand["attrs"])
	if attrs["title"] != "Prior run" {
		t.Errorf("expand attrs.title = %v, want %q", attrs["title"], "Prior run")
	}
	expandContent := asSlice(t, expand["content"])
	if len(expandContent) != 1 {
		t.Fatalf("expand content len = %d, want 1 (bulletList)", len(expandContent))
	}
	list := asMap(t, expandContent[0])
	if list["type"] != "bulletList" {
		t.Errorf("expand content[0] type = %v, want %q", list["type"], "bulletList")
	}

	// Third block: paragraph "more text"
	para2 := asMap(t, content[2])
	if para2["type"] != "paragraph" {
		t.Errorf("block 2 type = %v, want %q", para2["type"], "paragraph")
	}
}

func TestMarkdownToADF_DetailsWithStickySentinelsStripped(t *testing.T) {
	// Sticky history sentinels (<!-- sticky:history-start/end -->) are
	// parser metadata used by sticky.BuildUpdatedBody for history
	// reconstruction. They must be consumed during the <details> →
	// expand conversion and must not appear as visible text inside the
	// rendered expansion.
	input := "text\n\n<details><summary>Prior run</summary>\n" +
		"<!-- sticky:history-start -->\n- item one\n- item two\n" +
		"<!-- sticky:history-end -->\n</details>\n\nmore text"
	doc := mustADF(t, input)

	content := asSlice(t, doc["content"])
	if len(content) != 3 {
		t.Fatalf("doc content len = %d, want 3", len(content))
	}

	expand := asMap(t, content[1])
	if expand["type"] != "expand" {
		t.Fatalf("block 1 type = %v, want %q", expand["type"], "expand")
	}

	// Walk the entire expand subtree and verify no text node contains
	// the sentinel marker strings.
	var walk func(any)
	walk = func(v any) {
		m, ok := v.(map[string]any)
		if !ok {
			return
		}
		if text, ok := m["text"].(string); ok {
			if strings.Contains(text, "sticky:history-start") || strings.Contains(text, "sticky:history-end") {
				t.Errorf("expand subtree contains sentinel text %q; sentinels should be stripped", text)
			}
		}
		if c, ok := m["content"].([]any); ok {
			for _, child := range c {
				walk(child)
			}
		}
	}
	walk(expand)
}

func TestMarkdownToADF_DetailsMultiBlock(t *testing.T) {
	// When sticky.BuildUpdatedBody produces <details> blocks with blank
	// lines inside (between <summary> and the sentinel, and between the
	// sentinel and </details>), goldmark splits the markup across
	// multiple AST siblings. tryDetailsExpand must collect them all into
	// a single expand node.
	input := "text\n\n" +
		"<details>\n<summary>Previous run</summary>\n\n" +
		"<!-- sticky:history-start -->\n" +
		"- item one\n- item two\n" +
		"<!-- sticky:history-end -->\n\n" +
		"</details>\n\nmore text"
	doc := mustADF(t, input)

	content := asSlice(t, doc["content"])
	if len(content) != 3 {
		t.Fatalf("doc content len = %d, want 3 (paragraph, expand, paragraph)", len(content))
	}

	expand := asMap(t, content[1])
	if expand["type"] != "expand" {
		t.Fatalf("block 1 type = %v, want %q", expand["type"], "expand")
	}
	attrs := asMap(t, expand["attrs"])
	if attrs["title"] != "Previous run" {
		t.Errorf("expand attrs.title = %v, want %q", attrs["title"], "Previous run")
	}
	expandContent := asSlice(t, expand["content"])
	if len(expandContent) != 1 {
		t.Fatalf("expand content len = %d, want 1 (bulletList)", len(expandContent))
	}
	if asMap(t, expandContent[0])["type"] != "bulletList" {
		t.Errorf("expand content[0] type = %v, want %q", asMap(t, expandContent[0])["type"], "bulletList")
	}

	// Verify sentinels are stripped.
	var walk func(any)
	walk = func(v any) {
		m, ok := v.(map[string]any)
		if !ok {
			return
		}
		if text, ok := m["text"].(string); ok {
			if strings.Contains(text, "sticky:history") {
				t.Errorf("expand subtree contains sentinel text %q", text)
			}
		}
		if c, ok := m["content"].([]any); ok {
			for _, child := range c {
				walk(child)
			}
		}
	}
	walk(expand)
}

func TestMarkdownToADF_DetailsWithoutSummary(t *testing.T) {
	// <details> with no <summary> tag should still produce an expand
	// node, just without a title.
	doc := mustADF(t, "before\n\n<details>\nbody text\n</details>\n\nafter")

	content := asSlice(t, doc["content"])
	if len(content) != 3 {
		t.Fatalf("doc content len = %d, want 3", len(content))
	}
	expand := asMap(t, content[1])
	if expand["type"] != "expand" {
		t.Fatalf("block 1 type = %v, want %q", expand["type"], "expand")
	}
	if _, hasAttrs := expand["attrs"]; hasAttrs {
		t.Errorf("expand has attrs %v, want no attrs (no summary)", expand["attrs"])
	}
}

func TestMarkdownToADF_DetailsSingleBlockEmptyBody(t *testing.T) {
	// When a single-block <details> has an empty body (or the body
	// becomes empty after sentinel stripping), the function should emit
	// an expand node with a single empty paragraph, consistent with
	// the multi-block path's handling.
	doc := mustADF(t, "before\n\n<details><summary>Title</summary></details>\n\nafter")

	content := asSlice(t, doc["content"])
	if len(content) != 3 {
		t.Fatalf("doc content len = %d, want 3 (paragraph, expand, paragraph)", len(content))
	}

	expand := asMap(t, content[1])
	if expand["type"] != "expand" {
		t.Fatalf("block 1 type = %v, want %q", expand["type"], "expand")
	}
	attrs := asMap(t, expand["attrs"])
	if attrs["title"] != "Title" {
		t.Errorf("expand attrs.title = %v, want %q", attrs["title"], "Title")
	}
	expandContent := asSlice(t, expand["content"])
	if len(expandContent) != 1 {
		t.Fatalf("expand content len = %d, want 1 (empty paragraph)", len(expandContent))
	}
	para := asMap(t, expandContent[0])
	if para["type"] != "paragraph" {
		t.Errorf("expand content[0] type = %v, want %q", para["type"], "paragraph")
	}
}

func TestMarkdownToADF_DetailsMultiBlockEmptyBody(t *testing.T) {
	// When all siblings between <details> and </details> are sticky
	// sentinels (producing no ADF content), the function should still
	// emit an expand node with a single empty paragraph rather than
	// falling through to raw-text processing.
	input := "text\n\n" +
		"<details>\n<summary>History</summary>\n\n" +
		"<!-- sticky:history-start -->\n\n" +
		"<!-- sticky:history-end -->\n\n" +
		"</details>\n\nmore text"
	doc := mustADF(t, input)

	content := asSlice(t, doc["content"])
	if len(content) != 3 {
		t.Fatalf("doc content len = %d, want 3 (paragraph, expand, paragraph)", len(content))
	}

	expand := asMap(t, content[1])
	if expand["type"] != "expand" {
		t.Fatalf("block 1 type = %v, want %q", expand["type"], "expand")
	}
	attrs := asMap(t, expand["attrs"])
	if attrs["title"] != "History" {
		t.Errorf("expand attrs.title = %v, want %q", attrs["title"], "History")
	}
	expandContent := asSlice(t, expand["content"])
	if len(expandContent) != 1 {
		t.Fatalf("expand content len = %d, want 1 (empty paragraph)", len(expandContent))
	}
	para := asMap(t, expandContent[0])
	if para["type"] != "paragraph" {
		t.Errorf("expand content[0] type = %v, want %q", para["type"], "paragraph")
	}
}

func TestMarkdownToADF_NestedDetailsLimitation(t *testing.T) {
	// Known limitation: in the multi-block path, isDetailsClose matches
	// the first </details> HTMLBlock without tracking nesting depth. If
	// an inner <details> block's closing tag is split into its own
	// HTMLBlock (requires blank lines), the outer expand closes
	// prematurely. In practice, goldmark keeps inner <details> blocks
	// as a single HTMLBlock, so this does not arise with real-world
	// input. This test documents the limitation with the single-block
	// layout where nesting works correctly.
	input := "<details><summary>Outer</summary>\n" +
		"<details><summary>Inner</summary>\ninner body\n</details>\n" +
		"outer body\n</details>"
	doc := mustADF(t, input)

	content := asSlice(t, doc["content"])
	if len(content) != 1 {
		t.Fatalf("doc content len = %d, want 1 (expand)", len(content))
	}
	expand := asMap(t, content[0])
	if expand["type"] != "expand" {
		t.Fatalf("block type = %v, want %q", expand["type"], "expand")
	}
	attrs := asMap(t, expand["attrs"])
	if attrs["title"] != "Outer" {
		t.Errorf("expand attrs.title = %v, want %q", attrs["title"], "Outer")
	}
}

func TestMarkdownToADF_DetailsExpandRoundTrips(t *testing.T) {
	// Verify that <details> → expand → <details> round-trips stably.
	src := "<details><summary>Prior run</summary>\n- item one\n- item two\n</details>"
	doc := mustADF(t, src)

	// Should be an expand node, not fallback text.
	content := asSlice(t, doc["content"])
	if len(content) != 1 {
		t.Fatalf("doc content len = %d, want 1 (expand)", len(content))
	}
	expand := asMap(t, content[0])
	if expand["type"] != "expand" {
		t.Fatalf("block type = %v, want %q", expand["type"], "expand")
	}

	// Render back to Markdown.
	md := ADFToMarkdown(doc)

	// Re-parse: should produce the same ADF structure.
	doc2 := mustADF(t, md)
	content2 := asSlice(t, doc2["content"])
	if len(content2) != 1 {
		t.Fatalf("round-trip doc content len = %d, want 1", len(content2))
	}
	expand2 := asMap(t, content2[0])
	if expand2["type"] != "expand" {
		t.Fatalf("round-trip block type = %v, want %q", expand2["type"], "expand")
	}

	// Render again: should be identical to the first render.
	md2 := ADFToMarkdown(doc2)
	if md != md2 {
		t.Errorf("round-trip is not stable:\n  first:  %q\n  second: %q", md, md2)
	}
}

func TestMarkdownToADF_DetailsSummaryWithHTMLEntities(t *testing.T) {
	// A summary containing pre-existing HTML entities (e.g. &amp;)
	// must not be double-encoded on the round-trip. extractSummary
	// decodes entities so the ADF title is plain text, and
	// adfMarkdownBlock re-encodes with html.EscapeString.
	doc := mustADF(t, "<details><summary>A &amp; B</summary>\nbody\n</details>")

	content := asSlice(t, doc["content"])
	if len(content) != 1 {
		t.Fatalf("doc content len = %d, want 1 (expand)", len(content))
	}
	expand := asMap(t, content[0])
	if expand["type"] != "expand" {
		t.Fatalf("block type = %v, want %q", expand["type"], "expand")
	}
	attrs := asMap(t, expand["attrs"])
	if attrs["title"] != "A & B" {
		t.Errorf("expand attrs.title = %v, want %q (decoded)", attrs["title"], "A & B")
	}

	// Round-trip: the rendered Markdown should re-encode the & as &amp;.
	md := ADFToMarkdown(doc)
	want := "<details><summary>A &amp; B</summary>\nbody\n</details>"
	if md != want {
		t.Errorf("ADFToMarkdown = %q, want %q", md, want)
	}

	// Re-parse should produce the same decoded title.
	doc2 := mustADF(t, md)
	expand2 := asMap(t, asSlice(t, doc2["content"])[0])
	attrs2 := asMap(t, expand2["attrs"])
	if attrs2["title"] != "A & B" {
		t.Errorf("round-trip attrs.title = %v, want %q", attrs2["title"], "A & B")
	}
}

func TestMarkdownToADF_DetailsSummaryStripsHTMLTags(t *testing.T) {
	// A <summary> containing nested HTML tags (e.g. <b>, <a>, <img>,
	// <script>) must have those tags stripped so the ADF expand title is
	// plain text. Without stripping, HTML tags in the summary would be
	// stored verbatim in the ADF title attribute, creating an injection
	// risk if the consuming renderer interprets the title as HTML.
	tests := []struct {
		name  string
		input string
		title string
	}{
		{
			name:  "bold tag",
			input: "<details><summary><b>Bold</b> Title</summary>\nbody\n</details>",
			title: "Bold Title",
		},
		{
			name:  "anchor tag",
			input: "<details><summary>Click <a href=\"http://example.com\">here</a></summary>\nbody\n</details>",
			title: "Click here",
		},
		{
			name:  "img tag with onerror",
			input: "<details><summary>Title<img src=x onerror=alert(1)></summary>\nbody\n</details>",
			title: "Title",
		},
		{
			name:  "script tag",
			input: "<details><summary><script>alert(1)</script></summary>\nbody\n</details>",
			title: "alert(1)",
		},
		{
			name:  "entity-encoded tags decoded then stripped",
			input: "<details><summary>&lt;script&gt;alert(1)&lt;/script&gt;</summary>\nbody\n</details>",
			title: "alert(1)",
		},
		{
			name:  "mixed text and tags",
			input: "<details><summary>Hello <em>world</em> &amp; <strong>friends</strong></summary>\nbody\n</details>",
			title: "Hello world & friends",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc := mustADF(t, tc.input)
			content := asSlice(t, doc["content"])
			if len(content) != 1 {
				t.Fatalf("doc content len = %d, want 1", len(content))
			}
			expand := asMap(t, content[0])
			if expand["type"] != "expand" {
				t.Fatalf("block type = %v, want %q", expand["type"], "expand")
			}
			attrs := asMap(t, expand["attrs"])
			if attrs["title"] != tc.title {
				t.Errorf("expand attrs.title = %v, want %q", attrs["title"], tc.title)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ADFToMarkdown — expand → <details>
// ---------------------------------------------------------------------------

func TestADFToMarkdown_ExpandNodeEscapesTitle(t *testing.T) {
	// A title containing HTML special characters (especially
	// </summary>) must be escaped to prevent breaking the output.
	adf := map[string]any{
		"type": "doc",
		"content": []any{
			map[string]any{
				"type":  "expand",
				"attrs": map[string]any{"title": "a</summary>b"},
				"content": []any{
					map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": "body"}}},
				},
			},
		},
	}
	got := ADFToMarkdown(adf)
	want := "<details><summary>a&lt;/summary&gt;b</summary>\nbody\n</details>"
	if got != want {
		t.Errorf("ADFToMarkdown(expand with HTML title) = %q, want %q", got, want)
	}
}

func TestADFToMarkdown_ExpandNode(t *testing.T) {
	// An ADF expand node should render as <details><summary>…</summary>
	// with the body content inside, providing round-trip fidelity with
	// MarkdownToADF's <details> → expand conversion.
	adf := map[string]any{
		"type": "doc",
		"content": []any{
			map[string]any{
				"type":  "expand",
				"attrs": map[string]any{"title": "Click to expand"},
				"content": []any{
					map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": "hidden content"}}},
				},
			},
		},
	}
	got := ADFToMarkdown(adf)
	want := "<details><summary>Click to expand</summary>\nhidden content\n</details>"
	if got != want {
		t.Errorf("ADFToMarkdown(expand) = %q, want %q", got, want)
	}
}

func TestADFToMarkdown_ExpandNodeWithoutTitle(t *testing.T) {
	adf := map[string]any{
		"type": "doc",
		"content": []any{
			map[string]any{
				"type": "expand",
				"content": []any{
					map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": "content"}}},
				},
			},
		},
	}
	got := ADFToMarkdown(adf)
	want := "<details>\ncontent\n</details>"
	if got != want {
		t.Errorf("ADFToMarkdown(expand without title) = %q, want %q", got, want)
	}
}
