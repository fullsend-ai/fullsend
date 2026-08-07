package jira

import (
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// MarkdownToADF parses source as CommonMark and returns an Atlassian
// Document Format (ADF) "doc" node, since Jira's comment/description
// fields don't accept markdown directly. It supports the block/inline
// vocabulary ADFToPlainText (and the read-side walker it mirrors in
// internal/jirapoll) already recognizes: paragraphs, headings,
// bullet/ordered lists, blockquotes, fenced/indented code blocks,
// thematic breaks, and the strong/em/code/link/hardBreak inline marks.
// Markdown constructs outside that vocabulary (tables, images, raw HTML,
// footnotes, ...) are silently dropped rather than rendered as something
// ADF doesn't understand.
func MarkdownToADF(source string) map[string]any {
	src := []byte(source)
	doc := goldmark.DefaultParser().Parse(text.NewReader(src))
	return map[string]any{
		"version": 1,
		"type":    "doc",
		"content": adfBlockContent(doc, src, 0),
	}
}

// maxADFWriteDepth caps how deep adfBlockContent/convertBlockNode/
// walkInline will recurse into parsed markdown, mirroring maxADFDepth's
// rationale on the read side below. MarkdownToADF's callers don't feed it
// attacker-controlled input today, but it ships as a public API
// (jira.MarkdownToADF, LiveClient.CreateComment/UpdateComment) that a
// future caller could feed external content into (e.g. quoting an issue
// or comment body) — the same unbounded-recursion risk applies once that
// happens.
const maxADFWriteDepth = 50

// adfBlockContent converts each block-level child of parent into an ADF
// block node, dropping any child type convertBlockNode doesn't recognize.
// depth is the nesting depth of parent's children; past maxADFWriteDepth,
// remaining content is dropped rather than descended into, consistent with
// how unsupported node types are already handled.
func adfBlockContent(parent ast.Node, source []byte, depth int) []any {
	content := []any{}
	if depth > maxADFWriteDepth {
		return content
	}
	for c := parent.FirstChild(); c != nil; c = c.NextSibling() {
		if node := convertBlockNode(c, source, depth); node != nil {
			content = append(content, node)
		}
	}
	return content
}

// convertBlockNode converts a single goldmark block node to its ADF
// equivalent, or returns nil for node kinds outside MarkdownToADF's
// supported vocabulary. depth is n's own nesting depth.
func convertBlockNode(n ast.Node, source []byte, depth int) map[string]any {
	switch v := n.(type) {
	case *ast.Paragraph:
		return map[string]any{"type": "paragraph", "content": adfInlineContent(n, source, depth)}
	case *ast.TextBlock:
		// Tight list items wrap their text in a TextBlock rather than a
		// Paragraph; ADF's listItem schema still expects a paragraph child.
		return map[string]any{"type": "paragraph", "content": adfInlineContent(n, source, depth)}
	case *ast.Heading:
		return map[string]any{
			"type":    "heading",
			"attrs":   map[string]any{"level": v.Level},
			"content": adfInlineContent(n, source, depth),
		}
	case *ast.ThematicBreak:
		return map[string]any{"type": "rule"}
	case *ast.Blockquote:
		return map[string]any{"type": "blockquote", "content": adfBlockContent(n, source, depth+1)}
	case *ast.CodeBlock:
		return codeBlockNode(v.Lines().Value(source), "")
	case *ast.FencedCodeBlock:
		return codeBlockNode(v.Lines().Value(source), string(v.Language(source)))
	case *ast.List:
		listType := "bulletList"
		var attrs map[string]any
		if v.IsOrdered() {
			listType = "orderedList"
			if v.Start != 1 {
				attrs = map[string]any{"order": v.Start}
			}
		}
		node := map[string]any{"type": listType, "content": adfBlockContent(n, source, depth+1)}
		if attrs != nil {
			node["attrs"] = attrs
		}
		return node
	case *ast.ListItem:
		return map[string]any{"type": "listItem", "content": adfBlockContent(n, source, depth+1)}
	default:
		return nil
	}
}

// codeBlockNode builds an ADF codeBlock node. lang is set as the
// "language" attr only when non-empty (indented code blocks have none).
func codeBlockNode(text []byte, lang string) map[string]any {
	node := map[string]any{
		"type":    "codeBlock",
		"content": []any{map[string]any{"type": "text", "text": strings.TrimRight(string(text), "\n")}},
	}
	if lang != "" {
		node["attrs"] = map[string]any{"language": lang}
	}
	return node
}

// adfInlineContent converts the inline children of a block node (paragraph
// or heading) into a flat sequence of ADF text/hardBreak nodes. depth is
// the block node's own nesting depth, threaded through to walkInline since
// inline marks (nested emphasis/links/code spans) recurse too.
func adfInlineContent(parent ast.Node, source []byte, depth int) []any {
	content := []any{}
	walkInline(parent, source, nil, &content, depth)
	return content
}

// walkInline recursively walks inline nodes, accumulating the marks
// (strong/em/code/link) implied by enclosing nodes and emitting a text (or
// hardBreak) ADF node for each leaf. Past maxADFWriteDepth, remaining
// content is dropped rather than descended into.
func walkInline(parent ast.Node, source []byte, marks []any, out *[]any, depth int) {
	if depth > maxADFWriteDepth {
		return
	}
	for c := parent.FirstChild(); c != nil; c = c.NextSibling() {
		switch v := c.(type) {
		case *ast.Text:
			appendADFText(out, string(v.Value(source)), marks)
			if v.HardLineBreak() {
				*out = append(*out, map[string]any{"type": "hardBreak"})
			} else if v.SoftLineBreak() {
				appendADFText(out, " ", marks)
			}
		case *ast.String:
			appendADFText(out, string(v.Value), marks)
		case *ast.CodeSpan:
			walkInline(v, source, withMark(marks, map[string]any{"type": "code"}), out, depth+1)
		case *ast.Emphasis:
			markType := "em"
			if v.Level >= 2 {
				markType = "strong"
			}
			walkInline(v, source, withMark(marks, map[string]any{"type": markType}), out, depth+1)
		case *ast.Link:
			attrs := map[string]any{"href": string(v.Destination)}
			if len(v.Title) > 0 {
				attrs["title"] = string(v.Title)
			}
			walkInline(v, source, withMark(marks, map[string]any{"type": "link", "attrs": attrs}), out, depth+1)
		case *ast.AutoLink:
			appendADFText(out, string(v.Label(source)), withMark(marks, map[string]any{
				"type": "link", "attrs": map[string]any{"href": string(v.URL(source))},
			}))
		default:
			// Node types without ADF-specific handling (e.g. Image,
			// RawHTML) fall through to walking their children as plain
			// text, so at least the readable content isn't lost.
			walkInline(c, source, marks, out, depth+1)
		}
	}
}

// withMark returns a new marks slice with mark appended, without mutating
// the caller's slice (siblings must not see marks added while walking a
// previous sibling's subtree).
func withMark(marks []any, mark map[string]any) []any {
	next := make([]any, len(marks), len(marks)+1)
	copy(next, marks)
	return append(next, mark)
}

// appendADFText appends a "text" ADF node, or does nothing for empty text
// (e.g. the zero-length segment goldmark can produce around a hard break).
func appendADFText(out *[]any, value string, marks []any) {
	if value == "" {
		return
	}
	node := map[string]any{"type": "text", "text": value}
	if len(marks) > 0 {
		node["marks"] = marks
	}
	*out = append(*out, node)
}

// maxADFDepth caps how deep walkADFNode will recurse into an issue or
// comment body. Real Jira-UI-authored ADF documents are shallow (a
// handful of levels for nested lists at most); a body is
// attacker-controlled by any Jira user who can comment on or edit an
// issue tracker.Client reads, so without a cap, deeply nested JSON could
// exhaust the goroutine stack. Mirrors jirapoll's identical cap and logic
// (see extractADFText/walkADFNode in internal/jirapoll/discover.go) — the
// duplication is intentional for now, to avoid refactoring jirapoll's
// private helpers as part of this package's feature work; consolidating
// onto this exported ADFToPlainText is a reasonable follow-up once the
// Jira tracker integration stabilizes.
const maxADFDepth = 50

// ADFToPlainText extracts plain text from a Jira issue or comment body.
// body is either a plain string or an ADF document (map[string]any),
// matching the two shapes Jira's REST API returns for description/body
// fields; any other type (including nil) yields "".
func ADFToPlainText(body any) string {
	switch v := body.(type) {
	case string:
		return v
	case map[string]any:
		var sb strings.Builder
		walkADFNode(v, &sb, 0)
		return sb.String()
	default:
		return ""
	}
}

// walkADFNode recursively walks ADF nodes, extracting text, up to
// maxADFDepth levels deep.
func walkADFNode(node map[string]any, sb *strings.Builder, depth int) {
	if depth > maxADFDepth {
		return
	}

	// A hardBreak (Shift+Enter in the Jira editor) carries no text or
	// content; without emitting a newline here, words the author placed on
	// separate visual lines within one paragraph would fuse together.
	if nodeType, _ := node["type"].(string); nodeType == "hardBreak" {
		sb.WriteString("\n")
		return
	}

	if text, ok := node["text"].(string); ok {
		sb.WriteString(text)
	}

	nodeType, _ := node["type"].(string)

	content, ok := node["content"].([]any)
	if !ok {
		return
	}

	for i, child := range content {
		childMap, ok := child.(map[string]any)
		if !ok {
			continue
		}
		walkADFNode(childMap, sb, depth+1)

		// Add newline after paragraph/heading blocks (except the last one).
		childType, _ := childMap["type"].(string)
		if i < len(content)-1 && isBlockType(childType) && isBlockType(nodeType) {
			sb.WriteString("\n")
		}
	}
}

// isBlockType returns true for ADF block-level types that should be
// separated by newlines.
func isBlockType(nodeType string) bool {
	switch nodeType {
	case "doc", "paragraph", "heading", "blockquote", "codeBlock",
		"bulletList", "orderedList", "listItem", "panel", "rule":
		return true
	default:
		return false
	}
}
