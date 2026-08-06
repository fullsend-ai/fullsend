package jira

import (
	"fmt"
	"strings"
	"testing"
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

// ---------------------------------------------------------------------------
// MarkdownToADF
// ---------------------------------------------------------------------------

func TestMarkdownToADF_PlainParagraph(t *testing.T) {
	doc := MarkdownToADF("hello world")

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

func TestMarkdownToADF_MultipleParagraphs(t *testing.T) {
	doc := MarkdownToADF("first\n\nsecond")

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
		doc := MarkdownToADF(src)
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
	doc := MarkdownToADF("**bold** and *italic* and `code`")

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

func TestMarkdownToADF_FencedCodeBlockWithLanguage(t *testing.T) {
	doc := MarkdownToADF("```go\nfmt.Println(\"hi\")\n```")

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

func TestMarkdownToADF_BulletList(t *testing.T) {
	doc := MarkdownToADF("- one\n- two\n")

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
	doc := MarkdownToADF("5. five\n6. six\n")

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
	doc := MarkdownToADF("> quoted text")

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
	doc := MarkdownToADF("see [the docs](https://example.com/docs)")

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

func TestMarkdownToADF_ThematicBreak(t *testing.T) {
	doc := MarkdownToADF("above\n\n---\n\nbelow")

	content := asSlice(t, doc["content"])
	if len(content) != 3 {
		t.Fatalf("doc content len = %d, want 3", len(content))
	}
	rule := asMap(t, content[1])
	if rule["type"] != "rule" {
		t.Errorf("block type = %v, want %q", rule["type"], "rule")
	}
}

func TestMarkdownToADF_HardBreak(t *testing.T) {
	doc := MarkdownToADF("line one  \nline two")

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
