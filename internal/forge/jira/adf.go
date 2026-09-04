package jira

import (
	"fmt"
	"html"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// MarkdownToADF parses source as CommonMark and returns an Atlassian
// Document Format (ADF) "doc" node, since Jira's comment/description
// fields don't accept markdown directly. It supports the block/inline
// vocabulary ADFToPlainText (and the read-side walker it mirrors in
// internal/jirapoll) already recognizes: paragraphs, headings,
// bullet/ordered lists, blockquotes, fenced/indented code blocks,
// thematic breaks, and the strong/em/code/link/hardBreak inline marks.
// Block-level constructs outside that vocabulary (raw HTML, tables, ...)
// fall back to a plain-text paragraph of their raw source rather than
// vanishing outright — ADF has no equivalent node for most of them, but
// dropping the content entirely would silently lose whatever a caller
// wrote (e.g. this repo's own <details> convention for collapsing output).
//
// Returns an error, rather than a partial or empty result, if source is
// over maxMarkdownParseBytes or if it converts to no ADF content at all
// (e.g. markdown that's nothing but a chain of nested blockquotes deeper
// than maxADFWriteDepth): callers write the result straight to Jira, and
// silently posting a truncated or visibly empty comment is worse than
// failing the write.
func MarkdownToADF(source string) (map[string]any, error) {
	src := []byte(source)
	if len(src) > maxMarkdownParseBytes {
		return nil, fmt.Errorf("markdown body is %d bytes, over the %d byte limit", len(src), maxMarkdownParseBytes)
	}
	doc := goldmark.DefaultParser().Parse(text.NewReader(src))
	content := adfBlockContent(doc, src, 0, false)
	if len(content) == 0 {
		return nil, fmt.Errorf("markdown body converted to no ADF content")
	}
	return map[string]any{
		"version": 1,
		"type":    "doc",
		"content": content,
	}, nil
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

// maxMarkdownParseBytes caps the input size MarkdownToADF will parse.
// maxADFWriteDepth above only bounds the post-parse AST walk; Parse()
// itself is the actual bottleneck for adversarial input — benchmarking
// showed it costs ~O(N^2) on deeply nested blockquotes (~3.2s to parse
// 80,000 nesting levels, i.e. 160KB), independent of and unreached by the
// walk-depth cap. Rejecting oversized input outright keeps worst-case
// parse time to roughly a few hundred milliseconds even for
// pathologically nested input, while comfortably fitting any
// realistically hand-written comment or issue description; MarkdownToADF
// documents the limit for callers that may feed it larger,
// machine-generated bodies.
const maxMarkdownParseBytes = 32 * 1024

// MaxMarkdownBytes is the exported form of maxMarkdownParseBytes, for
// callers that assemble a markdown body destined for CreateComment/
// UpdateComment (or MarkdownToADF directly) and need to cap its size
// before hitting the limit at write time rather than after.
const MaxMarkdownBytes = maxMarkdownParseBytes

// adfBlockContent converts each block-level child of parent into zero or
// more ADF block nodes (a child can expand to several siblings, e.g. a
// flattened nested blockquote, or to none, e.g. a dropped thematic break).
// depth is the nesting depth of parent's children; past maxADFWriteDepth,
// remaining content is dropped rather than descended into. restricted
// indicates parent is itself a blockquote or listItem, whose ADF schema
// only accepts a narrower set of block types (paragraph, list, codeBlock)
// than convertBlockNode emits by default — see convertBlockNode.
func adfBlockContent(parent ast.Node, source []byte, depth int, restricted bool) []any {
	content := []any{}
	if depth > maxADFWriteDepth {
		return content
	}
	for c := parent.FirstChild(); c != nil; {
		// In non-restricted context, attempt to convert <details>
		// HTML blocks into ADF expand nodes. tryDetailsExpand
		// handles both single-block (no blank lines inside the
		// markup) and multi-block (blank lines split the <details>
		// opening, body, and closing across several AST siblings)
		// forms, since sticky.BuildUpdatedBody produces the latter
		// while hand-written Markdown typically produces the former.
		if !restricted {
			if expand, next := tryDetailsExpand(c, source, depth); expand != nil {
				content = append(content, expand)
				c = next
				continue
			}
		}
		content = append(content, convertBlockNode(c, source, depth, restricted)...)
		c = c.NextSibling()
	}
	return content
}

// summaryTagPattern matches <summary>...</summary> in HTML block content
// for extracting the expand title from a <details> block.
var summaryTagPattern = regexp.MustCompile(`(?is)<summary>(.*?)</summary>`)

// summaryInnerTagPattern matches HTML tags inside <summary> element content
// for stripping in extractSummary. Ensures the ADF expand title contains only
// plain text — nested HTML tags like <b>, <a>, <script>, or <img> inside
// the <summary> element are removed, leaving just the text content.
var summaryInnerTagPattern = regexp.MustCompile(`<[^>]*>`)

// stickyHistorySentinelPattern matches sticky history sentinel comments
// on their own line, for stripping from <details> body content before
// re-parsing as Markdown. These sentinels are metadata used by
// sticky.BuildUpdatedBody for history reconstruction and must not
// become visible text inside the rendered ADF expansion.
var stickyHistorySentinelPattern = regexp.MustCompile(`(?m)^\s*<!--\s*sticky:history-(?:start|end)\s*-->\s*$`)

// tryDetailsExpand checks whether c is an HTML block opening a
// <details> section and, if so, converts the entire section into an ADF
// expand node whose attrs.title carries the <summary> text and whose
// content holds the body re-parsed as Markdown.
//
// Two layouts are handled:
//
//   - Single-block: the entire <details>…</details> is one goldmark
//     HTMLBlock (no blank lines inside), common in hand-written Markdown.
//
//   - Multi-block: blank lines inside the markup (as produced by
//     sticky.BuildUpdatedBody) cause goldmark to split the opening tag,
//     body, and closing tag across separate AST siblings. The function
//     collects siblings until it finds the </details> closing block.
//
// Returns (expand, nextSibling) on success, or (nil, nil) if c is
// not a <details> opener — in which case adfBlockContent processes c
// normally through convertBlockNode.
func tryDetailsExpand(c ast.Node, source []byte, depth int) (map[string]any, ast.Node) {
	htmlBlock, ok := c.(*ast.HTMLBlock)
	if !ok {
		return nil, nil
	}
	raw := string(htmlBlock.Lines().Value(source))
	if !isDetailsOpen(raw) {
		return nil, nil
	}

	title := extractSummary(raw)

	// Single-block case: the entire <details>...</details> is one
	// HTMLBlock (no blank lines inside the markup).
	if hasDetailsClose(raw) {
		body := detailsInnerBody(raw)
		body = stickyHistorySentinelPattern.ReplaceAllString(body, "")
		body = strings.TrimSpace(body)
		var adfContent []any
		if body != "" {
			src := []byte(body)
			doc := goldmark.DefaultParser().Parse(text.NewReader(src))
			adfContent = adfBlockContent(doc, src, depth+1, false)
		}
		// When the body is empty (or becomes empty after sentinel
		// stripping), emit an expand with a single empty paragraph
		// rather than falling through to raw-text processing. This
		// mirrors the multi-block path and satisfies ADF's minItems: 1.
		if len(adfContent) == 0 {
			adfContent = []any{map[string]any{
				"type":    "paragraph",
				"content": []any{},
			}}
		}
		return expandNode(title, adfContent), c.NextSibling()
	}

	// Multi-block case: blank lines inside <details> caused goldmark to
	// split the block across multiple AST siblings. Collect content
	// nodes until we find the </details> closing block.
	var bodyContent []any
	next := c.NextSibling()
	foundClose := false
	for next != nil {
		if htmlBlock, ok := next.(*ast.HTMLBlock); ok {
			blockRaw := string(htmlBlock.Lines().Value(source))
			if isDetailsClose(blockRaw) {
				foundClose = true
				next = next.NextSibling()
				break
			}
			if isStickyHistorySentinel(blockRaw) {
				next = next.NextSibling()
				continue
			}
		}
		bodyContent = append(bodyContent, convertBlockNode(next, source, depth+1, false)...)
		next = next.NextSibling()
	}
	if !foundClose {
		return nil, nil
	}
	// When all siblings between <details> and </details> were sticky
	// sentinels (or otherwise produced no ADF content), emit an expand
	// with a single empty paragraph rather than falling through to
	// raw-text processing, which would render visible HTML in Jira.
	// ADF expand requires minItems: 1, so we supply one empty paragraph.
	if len(bodyContent) == 0 {
		bodyContent = []any{map[string]any{
			"type":    "paragraph",
			"content": []any{},
		}}
	}
	return expandNode(title, bodyContent), next
}

// expandNode builds an ADF expand node with the given title and block
// content. ADF's expand schema requires at least one content child
// (minItems: 1), so callers must ensure content is non-empty.
func expandNode(title string, content []any) map[string]any {
	node := map[string]any{
		"type":    "expand",
		"content": content,
	}
	if title != "" {
		node["attrs"] = map[string]any{"title": title}
	}
	return node
}

// isDetailsOpen reports whether raw starts with an HTML <details> tag.
func isDetailsOpen(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	lower := strings.ToLower(trimmed)
	return strings.HasPrefix(lower, "<details>") || strings.HasPrefix(lower, "<details ")
}

// hasDetailsClose reports whether raw contains a </details> closing tag.
func hasDetailsClose(raw string) bool {
	return strings.Contains(strings.ToLower(raw), "</details>")
}

// isDetailsClose reports whether raw is a </details> closing block.
func isDetailsClose(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	return strings.HasPrefix(strings.ToLower(trimmed), "</details")
}

// isStickyHistorySentinel reports whether raw is a sticky history
// sentinel HTML comment (<!-- sticky:history-start --> or
// <!-- sticky:history-end -->).
func isStickyHistorySentinel(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	return trimmed == "<!-- sticky:history-start -->" ||
		trimmed == "<!-- sticky:history-end -->"
}

// extractSummary extracts the text content of the first <summary> tag in
// raw, or "" if none is present. HTML entities are decoded and any nested
// HTML tags are stripped so the title stored in the ADF expand node is
// guaranteed plain text. The read-side (adfMarkdownBlock's expand case)
// re-encodes with html.EscapeString, keeping the round-trip correct
// without double-encoding pre-existing entities like "&amp;" → "&amp;amp;".
//
// Stripping tags prevents HTML injection: without it, a <summary> like
// <summary><img src=x onerror=alert(1)></summary> would store the raw
// <img> tag in the ADF title attribute, which could be interpreted as
// HTML by the consuming renderer.
func extractSummary(raw string) string {
	match := summaryTagPattern.FindStringSubmatch(raw)
	if match == nil {
		return ""
	}
	decoded := html.UnescapeString(strings.TrimSpace(match[1]))
	stripped := summaryInnerTagPattern.ReplaceAllString(decoded, "")
	return strings.TrimSpace(stripped)
}

// detailsInnerBody extracts the body content from a self-contained
// <details>...</details> HTML block: everything between </summary>
// (or the <details> opening tag if there's no summary) and </details>.
//
// Limitation: the no-summary fallback uses the first ">" to find the
// end of the opening tag, which would misparse a <details> tag with an
// attribute value containing ">". The codebase never produces
// attributed <details> tags, so this is acceptable.
func detailsInnerBody(raw string) string {
	body := raw
	lower := strings.ToLower(body)
	if idx := strings.Index(lower, "</summary>"); idx >= 0 {
		body = body[idx+len("</summary>"):]
	} else if idx := strings.Index(body, ">"); idx >= 0 {
		body = body[idx+1:]
	}
	lower = strings.ToLower(body)
	if idx := strings.LastIndex(lower, "</details>"); idx >= 0 {
		body = body[:idx]
	}
	return body
}

// convertBlockNode converts a single goldmark block node to zero or more
// ADF nodes: normally one, but zero for a dropped or empty node, or
// several when a nested blockquote is flattened into its parent's
// content. depth is n's own nesting depth. restricted indicates n is a
// direct child of a blockquote or listItem: ADF restricts both to
// paragraph/bulletList/orderedList/codeBlock/media content, so heading,
// thematic break, and nested blockquote — all valid at the top level or
// inside a list item's ordinary flow — must be degraded rather than
// emitted as-is, or Jira Cloud rejects the whole write with a 400.
func convertBlockNode(n ast.Node, source []byte, depth int, restricted bool) []any {
	switch v := n.(type) {
	case *ast.Paragraph:
		content := adfInlineContent(n, source, depth, nil)
		if len(content) == 0 {
			return nil
		}
		return oneNode(map[string]any{"type": "paragraph", "content": content})
	case *ast.TextBlock:
		// Tight list items wrap their text in a TextBlock rather than a
		// Paragraph; ADF's listItem schema still expects a paragraph child.
		content := adfInlineContent(n, source, depth, nil)
		if len(content) == 0 {
			return nil
		}
		return oneNode(map[string]any{"type": "paragraph", "content": content})
	case *ast.Heading:
		if restricted {
			// Degrade to a bold paragraph: ADF's blockquote/listItem
			// schema has no heading node.
			marks := []any{map[string]any{"type": "strong"}}
			return oneNode(map[string]any{"type": "paragraph", "content": adfInlineContent(n, source, depth, marks)})
		}
		return oneNode(map[string]any{
			"type":    "heading",
			"attrs":   map[string]any{"level": v.Level},
			"content": adfInlineContent(n, source, depth, nil),
		})
	case *ast.ThematicBreak:
		if restricted {
			// ADF's blockquote/listItem schema has no rule node; there's
			// no reasonable degradation, so it's dropped.
			return nil
		}
		return oneNode(map[string]any{"type": "rule"})
	case *ast.Blockquote:
		children := adfBlockContent(n, source, depth+1, true)
		if restricted {
			// ADF's blockquote schema doesn't allow a nested blockquote;
			// flatten by splicing this one's children directly into the
			// parent's content instead of wrapping them.
			return children
		}
		return containerNode("blockquote", children, nil)
	case *ast.CodeBlock:
		return oneNode(codeBlockNode(v.Lines().Value(source), ""))
	case *ast.FencedCodeBlock:
		return oneNode(codeBlockNode(v.Lines().Value(source), string(v.Language(source))))
	case *ast.List:
		listType := "bulletList"
		var attrs map[string]any
		if v.IsOrdered() {
			listType = "orderedList"
			if v.Start != 1 {
				attrs = map[string]any{"order": v.Start}
			}
		}
		// A List's own children are always ListItems, which are valid in
		// any context, so restricted doesn't propagate here — only into
		// each ListItem's own content below.
		return containerNode(listType, adfBlockContent(n, source, depth+1, false), attrs)
	case *ast.ListItem:
		return containerNode("listItem", adfBlockContent(n, source, depth+1, true), nil)
	default:
		// Node types without ADF-specific handling (e.g. HTMLBlock) fall
		// back to a plain-text paragraph of their raw source, mirroring
		// walkInline's own default case, so at least the readable content
		// isn't lost outright.
		if node := fallbackTextNode(n, source); node != nil {
			return oneNode(node)
		}
		return nil
	}
}

// oneNode wraps a single ADF node for convertBlockNode's []any return
// type.
func oneNode(node map[string]any) []any {
	return []any{node}
}

// containerNode builds an ADF container node (blockquote, bulletList,
// orderedList, listItem) of the given type, or returns no nodes at all if
// content is empty: ADF requires these types to have at least one child
// (minItems: 1), and an empty one — e.g. from a maxADFWriteDepth cutoff,
// or a listItem whose only child was dropped — would make Jira reject the
// entire write with a 400. Dropping the container is preferable to
// inserting a placeholder, since it doesn't fabricate visible content the
// original markdown never had.
func containerNode(nodeType string, content []any, attrs map[string]any) []any {
	if len(content) == 0 {
		return nil
	}
	node := map[string]any{"type": nodeType, "content": content}
	if attrs != nil {
		node["attrs"] = attrs
	}
	return oneNode(node)
}

// fallbackTextNode renders a block node's raw source lines as a plain-text
// paragraph, for block kinds convertBlockNode has no dedicated handling
// for. Returns nil if the node exposes no raw lines or they're blank.
func fallbackTextNode(n ast.Node, source []byte) map[string]any {
	lines, ok := n.(interface{ Lines() *text.Segments })
	if !ok {
		return nil
	}
	raw := strings.TrimSpace(string(lines.Lines().Value(source)))
	if raw == "" {
		return nil
	}
	return map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": raw}}}
}

// codeBlockNode builds an ADF codeBlock node. lang is set as the
// "language" attr only when non-empty (indented code blocks have none).
// The text child is omitted entirely for an empty block: ADF requires
// text nodes to be non-empty, but codeBlock content itself is optional, so
// a bare {"type":"codeBlock"} is the valid way to represent one.
func codeBlockNode(text []byte, lang string) map[string]any {
	node := map[string]any{"type": "codeBlock"}
	if trimmed := strings.TrimRight(string(text), "\n"); trimmed != "" {
		node["content"] = []any{map[string]any{"type": "text", "text": trimmed}}
	}
	if lang != "" {
		node["attrs"] = map[string]any{"language": lang}
	}
	return node
}

// adfInlineContent converts the inline children of a block node (paragraph
// or heading) into a flat sequence of ADF text/hardBreak nodes. depth is
// the block node's own nesting depth, threaded through to walkInline since
// inline marks (nested emphasis/links/code spans) recurse too. marks seeds
// the mark set every emitted text node starts with — nil normally, or e.g.
// a "strong" mark when a heading is being degraded to a bold paragraph
// inside a blockquote/listItem.
func adfInlineContent(parent ast.Node, source []byte, depth int, marks []any) []any {
	content := []any{}
	walkInline(parent, source, marks, &content, depth)
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
			appendADFText(out, textValue(v, source), marks)
			if v.HardLineBreak() {
				*out = append(*out, map[string]any{"type": "hardBreak"})
			} else if v.SoftLineBreak() {
				appendADFText(out, " ", marks)
			}
		case *ast.String:
			appendADFText(out, string(v.Value), marks)
		case *ast.CodeSpan:
			// ADF's code mark can only combine with link, per Atlassian's
			// mark-combination rules; drop any inherited strong/em (or
			// other) marks rather than emitting a schema-invalid
			// [strong, code] pair for input like "**`--flag`**".
			walkInline(v, source, withMark(onlyLinkMarks(marks), map[string]any{"type": "code"}), out, depth+1)
		case *ast.Emphasis:
			markType := "em"
			if v.Level >= 2 {
				markType = "strong"
			}
			walkInline(v, source, withMark(marks, map[string]any{"type": markType}), out, depth+1)
		case *ast.Link:
			dest := string(v.Destination)
			linkMarks := marks
			if isSafeHref(dest) {
				attrs := map[string]any{"href": dest}
				if len(v.Title) > 0 {
					attrs["title"] = string(v.Title)
				}
				linkMarks = withMark(marks, map[string]any{"type": "link", "attrs": attrs})
			}
			walkInline(v, source, linkMarks, out, depth+1)
		case *ast.AutoLink:
			dest := string(v.URL(source))
			linkMarks := marks
			if isSafeHref(dest) {
				linkMarks = withMark(marks, map[string]any{
					"type": "link", "attrs": map[string]any{"href": dest},
				})
			}
			appendADFText(out, string(v.Label(source)), linkMarks)
		case *ast.Image:
			// ADF has no plain image node outside "media", which needs
			// an uploaded attachment id rather than a bare URL — so the
			// destination, the load-bearing part of an image, is
			// preserved as a link on the alt text instead of being
			// dropped like the generic default case would (which walks
			// an Image's children as plain text and never looks at its
			// Destination).
			dest := string(v.Destination)
			linkMarks := marks
			if isSafeHref(dest) {
				linkMarks = withMark(marks, map[string]any{
					"type": "link", "attrs": map[string]any{"href": dest},
				})
			}
			walkInline(v, source, linkMarks, out, depth+1)
		default:
			// Node types without ADF-specific handling (e.g. Image,
			// RawHTML) fall through to walking their children as plain
			// text, so at least the readable content isn't lost.
			walkInline(c, source, marks, out, depth+1)
		}
	}
}

// isSafeHref reports whether dest is safe to emit as an ADF link's href.
// A missing scheme with no host (relative paths, "#fragment" anchors) is
// allowed, as are http/https/mailto; anything else — javascript:, data:,
// vbscript:, file:, protocol-relative "//host", ... — is rejected,
// mirroring the scheme allowlisting ValidateBaseURL already applies to
// Jira base URLs elsewhere in this package. dest that fails to parse as a
// URL at all is rejected too.
func isSafeHref(dest string) bool {
	if dest == "" {
		return true
	}
	u, err := url.Parse(dest)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "":
		return u.Host == ""
	case "http", "https", "mailto":
		return true
	default:
		return false
	}
}

// textValue returns the plain-text value of an *ast.Text node, resolving
// backslash escapes and HTML entity/numeric character references the same
// way goldmark's own HTML renderer does (via v.Value, which is raw source
// bytes and does neither). Raw text — inside a code span or code block —
// is returned unresolved, since goldmark's renderer leaves that content
// verbatim too: "\*not em\*" outside code becomes "*not em*", but
// "`\*x\*`" keeps its backslashes.
func textValue(v *ast.Text, source []byte) string {
	value := v.Value(source)
	if v.IsRaw() {
		return string(value)
	}
	return string(resolveTextEscapes(value))
}

// resolveTextEscapes resolves backslash-escaped punctuation and HTML
// entity/numeric character references in a single left-to-right pass, the
// same way goldmark's HTML renderer (defaultWriter.Write) does. A single
// pass is required: resolving punctuation escapes and entity references as
// two separate passes — the way util.UnescapePunctuations followed by
// util.ResolveEntityNames does — would unescape "\&copy;" to "&copy;"
// first and then misread that as the entity reference "&copy;", even
// though CommonMark's backslash-escape rule says the escaped "&" should
// stay literal and shield the following text from entity parsing.
func resolveTextEscapes(source []byte) []byte {
	var out []byte
	escaped := false
	limit := len(source)
	var ok bool
	n := 0
	for i := 0; i < limit; i++ {
		c := source[i]
		if escaped {
			if util.IsPunct(c) {
				out = append(out, source[n:i-1]...)
				n = i
				escaped = false
				continue
			}
			escaped = false
		}
		if c == '&' {
			pos := i
			next := i + 1
			if next < limit && source[next] == '#' {
				nnext := next + 1
				if nnext < limit {
					nc := source[nnext]
					if nc == 'x' || nc == 'X' {
						start := nnext + 1
						i, ok = util.ReadWhile(source, [2]int{start, limit}, util.IsHexDecimal)
						if ok && i < limit && source[i] == ';' {
							v, _ := strconv.ParseUint(util.BytesToReadOnlyString(source[start:i]), 16, 32)
							out = append(out, source[n:pos]...)
							n = i + 1
							out = utf8.AppendRune(out, util.ToValidRune(rune(v)))
							continue
						}
					} else if nc >= '0' && nc <= '9' {
						start := nnext
						i, ok = util.ReadWhile(source, [2]int{start, limit}, util.IsNumeric)
						if ok && i < limit && i-start < 8 && source[i] == ';' {
							v, _ := strconv.ParseUint(util.BytesToReadOnlyString(source[start:i]), 10, 32)
							out = append(out, source[n:pos]...)
							n = i + 1
							out = utf8.AppendRune(out, util.ToValidRune(rune(v)))
							continue
						}
					}
				}
			} else {
				start := next
				i, ok = util.ReadWhile(source, [2]int{start, limit}, util.IsAlphaNumeric)
				if ok && i < limit && source[i] == ';' {
					name := util.BytesToReadOnlyString(source[start:i])
					if entity, ok2 := util.LookUpHTML5EntityByName(name); ok2 {
						out = append(out, source[n:pos]...)
						n = i + 1
						out = append(out, entity.Characters...)
						continue
					}
				}
			}
			i = next - 1
		}
		if c == '\\' {
			escaped = true
		}
	}
	out = append(out, source[n:]...)
	return out
}

// withMark returns a new marks slice with mark appended, without mutating
// the caller's slice (siblings must not see marks added while walking a
// previous sibling's subtree). If marks already contains a mark of the
// same type — e.g. nested same-delimiter emphasis like
// "*outer _inner_ text*", which produces two nested *ast.Emphasis nodes
// with the same "em" markType — marks is returned unchanged rather than
// growing a duplicate: a text node with two identical marks is never
// meaningful, and ADF's validator behavior for it is unconfirmed.
//
// A same-type "link" mark is the one exception: unlike em/strong, two
// link marks can carry different hrefs — goldmark parses an autolink
// nested inside a Markdown link (e.g. "[a <inner-url> b](outer-url)") as
// a Link wrapping an AutoLink, even though CommonMark itself refuses
// nested links — so the existing link mark is replaced in place by the
// new (inner) one rather than treated as a redundant duplicate, matching
// CommonMark's inner-most-wins resolution for nested constructs.
func withMark(marks []any, mark map[string]any) []any {
	markType, _ := mark["type"].(string)
	for i, m := range marks {
		existing, ok := m.(map[string]any)
		if !ok || existing["type"] != markType {
			continue
		}
		if markType != "link" {
			return marks
		}
		next := make([]any, len(marks))
		copy(next, marks)
		next[i] = mark
		return next
	}
	next := make([]any, len(marks), len(marks)+1)
	copy(next, marks)
	return append(next, mark)
}

// onlyLinkMarks filters marks down to just the "link" mark (if present),
// for use before appending a "code" mark: ADF documents that code can only
// be combined with link, not with strong/em or other marks.
func onlyLinkMarks(marks []any) []any {
	var kept []any
	for _, m := range marks {
		if mark, ok := m.(map[string]any); ok && mark["type"] == "link" {
			kept = append(kept, m)
		}
	}
	return kept
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
		"bulletList", "orderedList", "listItem", "panel", "rule",
		"expand":
		return true
	default:
		return false
	}
}

// ADFToMarkdown converts a Jira issue or comment body to CommonMark
// Markdown, the reverse of MarkdownToADF. body is either a plain string or
// an ADF document (map[string]any), matching the two shapes Jira's REST
// API returns for description/body fields; any other type (including nil)
// yields "".
//
// Unlike ADFToPlainText, this preserves formatting (bold/italic/code/
// links, headings, lists, blockquotes, code blocks) rather than
// discarding it: tracker.Body is documented as Markdown-formatted text,
// so a Jira-backed tracker.Client returning plain text there would
// silently drop content GitHub- and GitLab-backed implementations
// preserve.
func ADFToMarkdown(body any) string {
	switch v := body.(type) {
	case string:
		return v
	case map[string]any:
		// A real Jira description/comment body is always a "doc" node
		// whose own content is a list of sibling blocks. Some callers
		// (and this package's own tests, mirroring ADFToPlainText's)
		// instead pass a single block node — a bare paragraph or list —
		// directly; render that as one block rather than misreading its
		// inline/item content as if it were a list of sibling blocks.
		if nodeType, _ := v["type"].(string); isBlockType(nodeType) && nodeType != "doc" {
			return adfMarkdownBlock(v, 0)
		}
		return strings.Join(adfMarkdownBlocks(v, 0), "\n\n")
	default:
		return ""
	}
}

// adfMarkdownBlocks renders each block-level child of node into its own
// Markdown string, mirroring walkADFNode's traversal but block-aware:
// callers join siblings with a blank line rather than a single newline,
// since that's what separates Markdown blocks (a single newline reads
// back as one paragraph). depth mirrors walkADFNode's cap against
// attacker-controlled ADF bodies.
func adfMarkdownBlocks(node map[string]any, depth int) []string {
	if depth > maxADFDepth {
		return nil
	}
	content, ok := node["content"].([]any)
	if !ok {
		return nil
	}
	var blocks []string
	for _, c := range content {
		childMap, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if block := adfMarkdownBlock(childMap, depth+1); block != "" {
			blocks = append(blocks, block)
		}
	}
	return blocks
}

// adfMarkdownBlock renders a single ADF block-level node as Markdown.
// Unrecognized types (e.g. "panel", "table", "taskList") fall back to
// recursing into their block-level children (e.g. a panel's paragraphs,
// a table's rows) so content isn't silently dropped; if that yields
// nothing, e.g. a taskItem whose own children are inline text nodes
// rather than blocks, it falls back further to rendering any direct
// inline text content flat, mirroring MarkdownToADF's own
// fallback-to-plain-text convention.
func adfMarkdownBlock(node map[string]any, depth int) string {
	nodeType, _ := node["type"].(string)
	switch nodeType {
	case "paragraph":
		return adfMarkdownInline(node)
	case "heading":
		level := 1
		if attrs, ok := node["attrs"].(map[string]any); ok {
			if l, ok := attrs["level"].(int); ok {
				level = l
			} else if l, ok := attrs["level"].(float64); ok {
				level = int(l)
			}
		}
		// The ADF schema restricts heading level to 1-6, but a
		// malformed or malicious body could carry anything;
		// strings.Repeat panics on a negative count, and 0 or >6
		// wouldn't round-trip as a heading anyway.
		level = clampInt(level, 1, 6)
		return strings.Repeat("#", level) + " " + adfMarkdownInline(node)
	case "codeBlock":
		lang := ""
		if attrs, ok := node["attrs"].(map[string]any); ok {
			if l, ok := attrs["language"].(string); ok {
				lang = sanitizeCodeLanguage(l)
			}
		}
		text := adfCodeBlockText(node)
		fence := strings.Repeat("`", codeFenceLength(text))
		if text == "" {
			return fence + lang + "\n" + fence
		}
		return fence + lang + "\n" + text + "\n" + fence
	case "rule":
		return "---"
	case "blockquote":
		lines := strings.Split(strings.Join(adfMarkdownBlocks(node, depth), "\n\n"), "\n")
		for i, line := range lines {
			lines[i] = "> " + line
		}
		return strings.Join(lines, "\n")
	case "bulletList":
		return adfMarkdownList(node, depth, func(int) string { return "- " })
	case "orderedList":
		start := 1
		if attrs, ok := node["attrs"].(map[string]any); ok {
			if o, ok := attrs["order"].(int); ok {
				start = o
			} else if o, ok := attrs["order"].(float64); ok {
				start = int(o)
			}
		}
		// CommonMark ordered-list start numbers are bounded to
		// [0, 999999999]; clamp so a malformed order attr can't
		// produce a marker a Markdown parser would reject.
		start = clampInt(start, 0, 999999999)
		return adfMarkdownList(node, depth, func(i int) string { return fmt.Sprintf("%d. ", start+i) })
	case "expand":
		title := ""
		if attrs, ok := node["attrs"].(map[string]any); ok {
			if t, ok := attrs["title"].(string); ok {
				title = t
			}
		}
		body := strings.Join(adfMarkdownBlocks(node, depth), "\n\n")
		var sb strings.Builder
		sb.WriteString("<details>")
		if title != "" {
			sb.WriteString("<summary>")
			sb.WriteString(html.EscapeString(title))
			sb.WriteString("</summary>")
		}
		sb.WriteString("\n")
		if body != "" {
			sb.WriteString(body)
			sb.WriteString("\n")
		}
		sb.WriteString("</details>")
		return sb.String()
	default:
		if blocks := adfMarkdownBlocks(node, depth); len(blocks) > 0 {
			return strings.Join(blocks, "\n\n")
		}
		return adfMarkdownInline(node)
	}
}

// codeFenceLength returns the number of backticks to use for a codeBlock's
// Markdown fence: at least 3, and always more than the longest run of
// consecutive backticks in content, so a quoted fenced code block inside
// content can't be mistaken for this block's own closing fence — per
// CommonMark, a closing fence needs only be *at least as many* backticks
// as the opening one.
func codeFenceLength(content string) int {
	longest, current := 0, 0
	for _, r := range content {
		if r == '`' {
			current++
			if current > longest {
				longest = current
			}
		} else {
			current = 0
		}
	}
	return max(longest+1, 3)
}

// codeLanguagePattern matches the conservative set of characters legal in
// Markdown fence-line language identifiers (word characters plus a few
// symbols common in real language names like "c++" and "c#").
var codeLanguagePattern = regexp.MustCompile(`^[A-Za-z0-9+#._-]+$`)

// sanitizeCodeLanguage returns lang unchanged if it matches
// codeLanguagePattern, or "" otherwise. ADF's codeBlock attrs.language is
// an unconstrained string spliced directly into the fence line; a value
// containing a newline and its own fence could inject arbitrary block
// structure into the rendered Markdown.
func sanitizeCodeLanguage(lang string) string {
	if codeLanguagePattern.MatchString(lang) {
		return lang
	}
	return ""
}

// adfCodeBlockText concatenates a codeBlock node's text children verbatim
// (no marks are valid inside an ADF codeBlock).
func adfCodeBlockText(node map[string]any) string {
	var sb strings.Builder
	for _, c := range asADFNodes(node["content"]) {
		if text, ok := c["text"].(string); ok {
			sb.WriteString(text)
		}
	}
	return sb.String()
}

// adfMarkdownList renders a bulletList or orderedList's items, each
// prefixed by marker(i) for its zero-based index. A listItem's own block
// content is rendered with adfMarkdownBlocks and indented so continuation
// lines (a second paragraph, or a nested list) stay part of the same item.
func adfMarkdownList(node map[string]any, depth int, marker func(i int) string) string {
	items := asADFNodes(node["content"])
	lines := make([]string, 0, len(items))
	for i, item := range items {
		body := strings.Join(adfMarkdownBlocks(item, depth+1), "\n\n")
		prefix := marker(i)
		indented := strings.ReplaceAll(body, "\n", "\n"+strings.Repeat(" ", len(prefix)))
		lines = append(lines, prefix+indented)
	}
	return strings.Join(lines, "\n")
}

// clampInt returns n bounded to [min, max].
func clampInt(n, min, max int) int {
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}

// asADFNodes type-asserts an ADF "content" field into a slice of child
// node maps, skipping (rather than failing on) any element that isn't a
// map[string]any.
func asADFNodes(content any) []map[string]any {
	items, ok := content.([]any)
	if !ok {
		return nil
	}
	nodes := make([]map[string]any, 0, len(items))
	for _, c := range items {
		if m, ok := c.(map[string]any); ok {
			nodes = append(nodes, m)
		}
	}
	return nodes
}

// adfMarkdownInline renders a block node's inline children (text/hardBreak/
// mention/emoji/status/date/inlineCard) as a single line of Markdown,
// applying each node's marks.
//
// Adjacent children carrying identical mark sets are coalesced into one
// run before their marks are applied, rather than wrapped independently.
// Real Jira-Cloud-authored ADF commonly splits a single bold run across
// several same-marked text nodes, and walkInline's own soft-break case
// (a " " text node inheriting its neighbors' marks) produces the same
// shape on the write side: wrapping "hello " and "world" independently
// as "**hello **" + "**world**" leaves "**hello ****world**", which
// MarkdownToADF re-parses with the middle "****" as literal asterisks
// rather than one continuous bold run.
func adfMarkdownInline(node map[string]any) string {
	var sb strings.Builder
	// atLineStart tracks whether the next emitted content starts a fresh
	// rendered Markdown line: true for the block's first child, and
	// again right after a hardBreak. Unmarked text landing here needs
	// escaping if it happens to begin with block-level syntax (#, -, >,
	// +, "1."), or MarkdownToADF would reparse it as a different node
	// type — see applyADFMarks/escapeMDLineStarts.
	atLineStart := true

	// pending buffers a run of consecutive children with identical marks
	// so their text can be escaped and wrapped once, rather than once
	// per ADF node.
	var pending struct {
		text        string
		marks       []map[string]any
		atLineStart bool
		open        bool
	}
	flush := func() {
		if !pending.open {
			return
		}
		sb.WriteString(applyADFMarks(pending.text, pending.marks, pending.atLineStart))
		pending.open = false
	}
	appendRun := func(text string, marks []map[string]any) {
		if pending.open && reflect.DeepEqual(pending.marks, marks) {
			pending.text += text
			return
		}
		flush()
		pending.text, pending.marks, pending.atLineStart, pending.open = text, marks, atLineStart, true
	}

	for _, c := range asADFNodes(node["content"]) {
		childType, _ := c["type"].(string)
		switch childType {
		case "hardBreak":
			// A backslash before the line ending, rather than the more
			// common trailing double-space: trailing whitespace is
			// silently stripped by enough editors, git diffs, and web
			// forms that a hard break built from it wouldn't reliably
			// survive a copy/paste round trip.
			flush()
			sb.WriteString("\\\n")
			atLineStart = true
			continue
		case "mention", "status":
			appendRun(atomAttrText(c, "text"), asADFNodes(c["marks"]))
			atLineStart = false
			continue
		case "emoji":
			text := atomAttrText(c, "text")
			if text == "" {
				text = atomAttrText(c, "shortName")
			}
			appendRun(text, asADFNodes(c["marks"]))
			atLineStart = false
			continue
		case "date":
			appendRun(atomAttrText(c, "timestamp"), asADFNodes(c["marks"]))
			atLineStart = false
			continue
		case "inlineCard":
			appendRun(atomAttrText(c, "url"), asADFNodes(c["marks"]))
			atLineStart = false
			continue
		}
		text, _ := c["text"].(string)
		appendRun(text, asADFNodes(c["marks"]))
		atLineStart = false
	}
	flush()
	return sb.String()
}

// atomAttrText reads a string-valued attrs field from an inline atom node
// (mention, emoji, status, date, inlineCard) — these carry their visible
// text in attrs rather than a top-level "text" field. Returns "" if attrs
// or the named field is missing or not a string.
func atomAttrText(node map[string]any, field string) string {
	attrs, ok := node["attrs"].(map[string]any)
	if !ok {
		return ""
	}
	s, _ := attrs[field].(string)
	return s
}

// applyADFMarks wraps text in the Markdown syntax for each of marks, in
// reverse order: MarkdownToADF's walkInline builds a text node's marks
// outer-to-inner (each enclosing mark is appended after the ones already
// inherited from its parent — see its Emphasis/Link cases), so marks[0] is
// the outermost mark and must be applied last to reproduce the original
// nesting. Text is escaped for Markdown-significant characters unless a
// code mark is present (code spans are verbatim in Markdown).
//
// atLineStart additionally escapes leading block-level syntax (#, -, >,
// +, "1.") when text is otherwise unmarked and lands at the start of a
// rendered line — marked text doesn't need this, since a Markdown parser
// sees the mark's own opening delimiter, not text's first character, at
// that position.
func applyADFMarks(text string, marks []map[string]any, atLineStart bool) string {
	if !hasCodeMark(marks) {
		text = escapeMDText(text)
		if len(marks) == 0 {
			text = escapeMDLineStarts(text, atLineStart)
		}
	}
	for i := len(marks) - 1; i >= 0; i-- {
		mark := marks[i]
		switch mark["type"] {
		case "strong":
			text = wrapFlanking(text, "**")
		case "em":
			text = wrapFlanking(text, "*")
		case "code":
			text = "`" + text + "`"
		case "link":
			href := ""
			if attrs, ok := mark["attrs"].(map[string]any); ok {
				href, _ = attrs["href"].(string)
			}
			text = "[" + text + "](" + href + ")"
		}
	}
	return text
}

// wrapFlanking wraps text in an emphasis delimiter (CommonMark's "**" or
// "*"), placing the delimiters around only text's non-whitespace core and
// leaving any leading or trailing whitespace outside them. CommonMark
// only lets a delimiter open or close emphasis when it isn't immediately
// followed (to open) or preceded (to close) by whitespace — wrapping
// "hello " directly as "**hello **" leaves the closing "**" unable to
// close, so MarkdownToADF would read it back as literal asterisks rather
// than bold. If text is entirely whitespace (or empty), there's no
// visible content to mark up, so it's returned unwrapped.
func wrapFlanking(text, delim string) string {
	leftTrimmed := strings.TrimLeftFunc(text, unicode.IsSpace)
	lead := text[:len(text)-len(leftTrimmed)]
	core := strings.TrimRightFunc(leftTrimmed, unicode.IsSpace)
	if core == "" {
		return text
	}
	trail := leftTrimmed[len(core):]
	return lead + delim + core + delim + trail
}

// mdEscaper escapes characters that have syntactic meaning in CommonMark
// so that literal content from Jira-native ADF (not from a MarkdownToADF
// round-trip) renders verbatim rather than triggering formatting.
// Backslash is escaped first to avoid double-escaping.
var mdEscaper = strings.NewReplacer(
	`\`, `\\`,
	`*`, `\*`,
	`_`, `\_`,
	"`", "\\`",
	`[`, `\[`,
	`]`, `\]`,
	`&`, `\&`,
)

// escapeMDText escapes markdown-significant characters in text.
func escapeMDText(text string) string {
	return mdEscaper.Replace(text)
}

// escapeMDBlockChars are the characters that only carry block-level
// meaning (ATX heading, blockquote, bullet list) when they're the first
// character on a Markdown source line. Unlike mdEscaper's characters,
// escaping these everywhere would be needlessly noisy for ordinary body
// text ("well-known", "C#"), so escapeMDLineStarts only escapes them at
// an actual line start.
const escapeMDBlockChars = "#->+"

// escapeMDLineStarts backslash-escapes a leading block-syntax character
// — ATX heading '#', blockquote '>', bullet list '-'/'+', or an ordered
// list marker like "1." or "1)" — wherever it falls at the start of a
// rendered Markdown line: at the very start of text if atLineStart, and
// after any newline embedded directly within text. Without this, e.g. a
// Jira-native paragraph whose text happens to start with "# " would
// reparse as a heading on a round trip through MarkdownToADF.
func escapeMDLineStarts(text string, atLineStart bool) string {
	var sb strings.Builder
	lineStart := atLineStart
	for i := 0; i < len(text); {
		c := text[i]
		if lineStart {
			if strings.IndexByte(escapeMDBlockChars, c) >= 0 {
				sb.WriteByte('\\')
				sb.WriteByte(c)
				i++
				lineStart = false
				continue
			}
			if c >= '0' && c <= '9' {
				j := i
				for j < len(text) && text[j] >= '0' && text[j] <= '9' {
					j++
				}
				if j < len(text) && (text[j] == '.' || text[j] == ')') {
					sb.WriteString(text[i:j])
					sb.WriteByte('\\')
					sb.WriteByte(text[j])
					i = j + 1
					lineStart = false
					continue
				}
			}
		}
		sb.WriteByte(c)
		lineStart = c == '\n'
		i++
	}
	return sb.String()
}

// hasCodeMark reports whether marks contains a "code" mark. Text inside a
// code span is rendered verbatim in Markdown, so escaping would insert
// visible backslashes.
func hasCodeMark(marks []map[string]any) bool {
	for _, m := range marks {
		if m["type"] == "code" {
			return true
		}
	}
	return false
}
