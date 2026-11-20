package security

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"unicode/utf8"
)

// JSON output that cannot be redacted without corrupting its structure is
// replaced with one of these valid JSON markers so downstream parsers (jq, the
// retro and eval tooling) keep working while no secret reaches the artifact.
// Each marker names the reason its content was dropped: the CLI turns that
// reason into an operator alert, because a replaced file loses whatever the
// agent wrote into it (#5705).
const (
	failClosedInvalidJSON      = `{"error":"output removed because redaction would invalidate JSON"}`
	failClosedCrossRecordJSON  = `{"error":"output removed because output contained a cross-record secret"}`
	failClosedSpansMembersJSON = `{"error":"output removed because a private key block spans JSON framing"}`

	// OutputRemovalTextJSONMarker replaces a whole file whose plain-text
	// redaction would leave the JSON it started as syntactically broken. The CLI
	// writes it on the fallback path for files the structural path cannot
	// handle.
	OutputRemovalTextJSONMarker = `{"error":"output removed because text redaction would invalidate JSON"}`
)

// privateKeyMask replaces each JSON string value that carries key material from a
// private-key block spanning JSON framing, and any key name that carries block
// text itself.
const privateKeyMask = "[REDACTED PRIVATE KEY]"

// outputRemovalMarkers lists every whole-file removal marker with the reason it
// states.
var outputRemovalMarkers = []struct {
	document, reason string
}{
	{failClosedInvalidJSON, "redaction would invalidate JSON"},
	{failClosedCrossRecordJSON, "a secret spanned multiple records"},
	{failClosedSpansMembersJSON, "a private key block spans JSON framing"},
	{OutputRemovalTextJSONMarker, "text redaction would invalidate JSON"},
}

// OutputRemoval reports whether sanitized is a whole-file removal marker and
// returns the reason it states. A replaced file loses the content the agent
// wrote into it, so callers alert operators rather than let that loss pass
// silently (#5705).
func OutputRemoval(sanitized []byte) (string, bool) {
	trimmed := string(bytes.TrimSpace(sanitized))
	for _, marker := range outputRemovalMarkers {
		if trimmed == marker.document {
			return marker.reason, true
		}
	}
	return "", false
}

// OutputRecordRemovals returns the reason stated by each fail-closed marker
// record in a sanitized JSONL stream, in order and without repeats. A JSONL file
// keeps its framing when records are replaced, so it is never a whole-file
// marker, yet the records it lost still have to reach an operator (#5705).
func OutputRecordRemovals(sanitized []byte) []string {
	var (
		reasons []string
		seen    = map[string]bool{}
	)
	for _, line := range bytes.Split(sanitized, []byte("\n")) {
		reason, removed := OutputRemoval(line)
		if !removed || seen[reason] {
			continue
		}
		seen[reason] = true
		reasons = append(reasons, reason)
	}
	return reasons
}

// sensitiveJSONKeyRe matches object keys whose values are masked even when the
// value itself does not match a secret pattern.
var sensitiveJSONKeyRe = regexp.MustCompile(`(?i)(?:key|token|secret|password|credential|auth)`)

// redactedJSONKey replaces an object key that sanitization removed entirely
// (a key made only of bidi overrides or other invisible characters, for
// example). Such a key must never be restored as-is.
const redactedJSONKey = "redacted"

// SanitizeJSONDocument structurally sanitizes a single JSON document so that
// redaction never changes its syntax. It returns the bytes to write and the
// findings that justified a rewrite; no findings means the document is clean
// and the caller must leave it untouched. A document that is not valid JSON is
// returned unchanged with no findings so the caller can fall back to a text
// scan.
//
// The document is rewritten by a token-preserving walk rather than decoded into
// a map and re-encoded: that keeps member order and duplicate keys, so a
// secret that spans JSON framing is still visible in the rewritten bytes.
func SanitizeJSONDocument(doc []byte, p *Pipeline) ([]byte, []Finding, error) {
	trimmed := bytes.TrimSpace(doc)
	if len(trimmed) == 0 || !json.Valid(trimmed) {
		return doc, nil, nil
	}
	// A block that spans members is invisible to both the leaf scan and the
	// written-bytes scan once a member is masked, and its markers may be
	// \u-escaped in the raw bytes. The decoded view is the only place it is
	// visible, so it is consulted before anything is rewritten.
	spans := collectJSONLeaves(trimmed).spanningPrivateKeys()
	if spans.unmappable > 0 {
		// The view is built from the leaves themselves, so localization should
		// never fail. If the walks ever disagree, fail closed rather than write
		// bytes that may still carry the block.
		return preserveTrailingNewline([]byte(failClosedSpansMembersJSON), doc),
			[]Finding{privateKeySpanFinding(spans.unmappable, false)}, nil
	}
	rebuilt, findings, err := rewriteJSONTokens(trimmed, p, spans)
	if err != nil {
		// json.Valid accepted the text and the walk uses the same scanner, so
		// this is a decoder limit rather than a syntax error. Return the error
		// so the caller fails closed instead of writing unscanned bytes.
		return nil, nil, fmt.Errorf("could not sanitize JSON document: %w", err)
	}
	out := doc
	if len(findings) > 0 || spans.localized > 0 {
		out = rebuilt
	}
	if spans.localized > 0 {
		findings = append(findings, privateKeySpanFinding(spans.localized, true))
	}
	// Scan the bytes that will actually be written, for the whole-value secrets
	// a leaf scan cannot see: a runtime value or a private-key block that spans
	// JSON framing (for example a PEM block split across members) and so cannot
	// be mapped back to a single leaf.
	sanitizedOut, outputFindings := redactJSONOutput(out, p)
	return preserveTrailingNewline(sanitizedOut, doc), append(findings, outputFindings...), nil
}

// privateKeySpanFinding reports the private-key blocks that span JSON framing.
// localized says whether they were masked in the members that carried them or
// the whole document had to be dropped.
func privateKeySpanFinding(blocks int, localized bool) Finding {
	detail := fmt.Sprintf("%d private key block(s) span JSON framing; masked the string values carrying them", blocks)
	if !localized {
		detail = "Private key block spans JSON framing and cannot be localized; output removed"
	}
	return Finding{
		Scanner:  "secret_redactor",
		Name:     "private_key",
		Severity: "critical",
		Detail:   detail,
	}
}

// privateKeyRe is the structural private-key pattern. A block that spans JSON
// framing has to be detected on the raw document: running the whole pipeline
// first lets a sensitive-named member mask the BEGIN marker and hide it.
var privateKeyRe = func() *regexp.Regexp {
	for _, pattern := range defaultStructuralPatterns() {
		if pattern.name == "private_key" {
			return pattern.regex
		}
	}
	panic("private_key structural pattern is missing")
}()

// privateKeyMarkerRe matches a private-key marker line, including the fragments a
// marker is reduced to when it is split across members. It marks the text a
// multi-member block carries into a key name or string value, so nothing that is
// block content is written back as structure. The quoted markers are the ones
// privateKeyRe delimits a block with.
var privateKeyMarkerRe = regexp.MustCompile(`-----BEGIN|-----END|PRIVATE KEY-----`)

// privateKeyMaterialRe matches a run of PEM body material: the base64 alphabet
// (which covers hex) at a length no ordinary prose word or schema enum reaches.
// A string value covered by a framing-spanning block is masked only when it
// holds a marker or matches this, because a lazy pattern that runs from one
// member's BEGIN to another member's END also covers the members between them.
// Masking those unconditionally replaced structural values — a finding's
// severity, category, action or repo, all of which the review schema constrains
// to enums and patterns — so the review failed validation and was not posted,
// which is the #5705 outcome. Real PEM bodies are 64-character base64 lines (or
// a partial final line), so a 16-character floor keeps every realistic split
// while leaving free text and enum values readable. valueCarriesKeyMaterial still
// has to reject a hex-only run, which is more likely a hash than a PEM line. A
// fragment shorter than the floor cannot be recognized as key material, which is
// the same limit that makes a quoted marker pair indistinguishable from a split
// block.
var privateKeyMaterialRe = regexp.MustCompile(`[A-Za-z0-9+/]{16,}=*`)

// jsonProvenance accumulates an order-preserving view of the decoded strings
// in a document so a private-key block that spans members can be detected after
// JSON decoding. The raw bytes are not usable for this: a member may encode a
// marker with \u escapes, and masking a sensitive member removes the BEGIN
// marker before the rewritten document is scanned.
type jsonProvenance struct {
	decoded bytes.Buffer
	ranges  []recordSpan
	// keys marks the entries in ranges that are object keys rather than string
	// values. Key names are the document's structure, so a block that spans
	// them is neutralized differently from a block that spans values.
	keys []bool
}

// decodedViewNormalizer normalizes decoded JSON text before it is matched for
// framing-spanning secrets, so a marker obfuscated with zero-width or bidi
// characters is normalized exactly as the per-leaf scan normalizes it.
var decodedViewNormalizer = NewUnicodeNormalizer()

// addString appends one decoded key or string value to the view and records its
// byte range so a match can be mapped back to the leaves that carry it.
func (v *jsonProvenance) addString(s string, isKey bool) {
	s = decodedViewNormalizer.Scan(s).Sanitized
	start := v.decoded.Len()
	v.decoded.WriteString(s)
	v.decoded.WriteByte('\n')
	v.ranges = append(v.ranges, recordSpan{start: start, end: v.decoded.Len() - 1})
	v.keys = append(v.keys, isKey)
}

// privateKeySpans is the result of looking for private-key blocks that span the
// keys and values of one document.
type privateKeySpans struct {
	// covered holds the indexes of the string values that carry part of a block
	// spanning more than one leaf. Every covered value is replaced whole.
	covered map[int]bool
	// maskedKeys holds the indexes of the object keys whose own name carries a
	// block marker and is therefore block content rather than structure.
	maskedKeys map[int]bool
	// localized counts the blocks that were mapped onto leaves.
	localized int
	// unmappable counts the blocks that no leaf range covers.
	unmappable int
}

// spanningPrivateKeys finds the private-key blocks in the decoded view that are
// not contained in a single key or value, and maps each one onto the leaves it
// covers. Containment is checked per match: a lazy pattern can start at one
// leaf's BEGIN and end at another leaf's END, and counting differences would let
// that match cancel out a complete leaf block.
//
// A block whose markers sit in different members is not proof that key material
// spans them — a review that quotes the header in one finding and the footer in
// another matches too — so only the members the match carries block content into
// are touched. The strings the match starts and ends in carry marker text and are
// masked unconditionally; a string value in between is masked when it holds a
// block marker or base64 body material (valueCarriesKeyMaterial). Those are the
// two shapes key material takes in a member, while the free text and enum values
// between a quoted marker pair stay readable, so a review result keeps its shape
// instead of failing schema validation and being dropped (#5705). An object key
// keeps its name unless the match starts or ends in it, or the name itself carries
// a block marker: key names are the document's structure, and masking them would
// drop a required field such as a finding's severity, category or file and make
// the document fail schema validation.
func (v *jsonProvenance) spanningPrivateKeys() privateKeySpans {
	spans := privateKeySpans{covered: map[int]bool{}, maskedKeys: map[int]bool{}}
	for _, match := range privateKeyRe.FindAllIndex(v.decoded.Bytes(), -1) {
		if v.leafContaining(match[0], match[1]) >= 0 {
			// A block inside one leaf is that leaf's own scan.
			continue
		}
		var hit []int
		for i, r := range v.ranges {
			if r.start < match[1] && match[0] < r.end {
				hit = append(hit, i)
			}
		}
		if len(hit) == 0 {
			spans.unmappable++
			continue
		}
		spans.localized++
		for pos, i := range hit {
			// The match begins with a BEGIN marker and ends with an END marker, so
			// the leaves it starts and ends in always carry marker text. They are
			// masked even when the marker itself is split across two members and
			// neither fragment matches privateKeyMarkerRe on its own.
			atBoundary := pos == 0 || pos == len(hit)-1
			if v.keys[i] {
				if atBoundary || privateKeyMarkerRe.Match(v.decoded.Bytes()[v.ranges[i].start:v.ranges[i].end]) {
					spans.maskedKeys[i] = true
				}
				continue
			}
			if atBoundary || v.valueCarriesKeyMaterial(i) {
				spans.covered[i] = true
			}
		}
	}
	return spans
}

// valueCarriesKeyMaterial reports whether the string value at index i has to be
// replaced whole: it carries a block marker, or it holds base64 body material. A
// value outside those two shapes is free text or a schema-constrained token that
// the match only covers because a lazy pattern ran from one member's marker to
// another's, so it keeps its content.
func (v *jsonProvenance) valueCarriesKeyMaterial(i int) bool {
	text := v.decoded.Bytes()[v.ranges[i].start:v.ranges[i].end]
	if privateKeyMarkerRe.Match(text) {
		return true
	}
	run := privateKeyMaterialRe.Find(text)
	if run == nil {
		return false
	}
	// A run of hex digits alone is as likely a content hash, a commit SHA or a
	// stable finding id as it is key material, and those are schema-constrained
	// values (head_sha, id, file). A PEM body is base64, so a real body run
	// almost always carries a base64-only character; treating a hex-only run as
	// key material would mask a required field and lose the review (#5705).
	for _, b := range run {
		switch {
		case b == '+' || b == '/' || b == '=':
			return true
		case b >= 'G' && b <= 'Z', b >= 'g' && b <= 'z':
			return true
		}
	}
	return false
}

// leafContaining returns the index of the leaf that fully contains [start, end),
// or -1 when no single leaf does.
func (v *jsonProvenance) leafContaining(start, end int) int {
	for i, r := range v.ranges {
		if r.start <= start && end <= r.end {
			return i
		}
	}
	return -1
}

// preserveTrailingNewline drops the newline json.Encoder always appends and
// restores the document's own trailing newline, so a rewrite never changes the
// file's line framing.
func preserveTrailingNewline(encoded, doc []byte) []byte {
	encoded = bytes.TrimRight(encoded, "\r\n")
	switch {
	case bytes.HasSuffix(doc, []byte("\r\n")):
		return append(encoded, '\r', '\n')
	case bytes.HasSuffix(doc, []byte("\n")):
		return append(encoded, '\n')
	default:
		return encoded
	}
}

// redactJSONOutput scans the bytes about to be written for secrets that only
// appear in the serialized form. Such a match does not map to a single leaf, so
// it is applied only when the redacted text is still valid JSON; otherwise the
// document fails closed with a valid marker. clean is returned unchanged when
// the scan finds nothing.
//
// Only whole-value patterns run here. The leaf walk has already applied the
// value patterns to the decoded document, so a value match on the re-encoded
// bytes is a redaction placeholder followed by JSON escape characters rather
// than a secret, and masking it again would drop both the placeholder's prefix
// and a non-secret backslash (#5705).
func redactJSONOutput(clean []byte, p *Pipeline) ([]byte, []Finding) {
	res := p.ScanWholeValueSecrets(string(clean))
	if len(res.Findings) == 0 {
		return clean, nil
	}
	for _, finding := range res.Findings {
		// A private-key block spanning values cannot be masked in place
		// without breaking the surrounding JSON.
		if finding.Name == "private_key" {
			return []byte(failClosedSpansMembersJSON), res.Findings
		}
	}
	if res.Sanitized == "" || !json.Valid([]byte(res.Sanitized)) {
		return []byte(failClosedInvalidJSON), res.Findings
	}
	return []byte(res.Sanitized), res.Findings
}

// SanitizeJSONLines sanitizes newline-delimited JSON one record at a time,
// preserving the record framing so every line stays independently parseable.
// A secret that spans a record boundary cannot be redacted without rewriting
// that framing, so the records it covers are replaced with valid fail-closed
// markers and every other record is left as sanitized.
func SanitizeJSONLines(content []byte, p *Pipeline) ([]byte, []Finding, error) {
	// A private-key block that spans records has to be detected on the original
	// framing: per-record masking of a sensitive member removes the BEGIN
	// marker, after which neither the record scan nor the whole-stream scan can
	// recognize the block.
	content, crossRecordFindings := failClosedCrossRecordPrivateKeys(content)
	var (
		out      bytes.Buffer
		findings []Finding
		spans    []recordSpan
	)
	findings = append(findings, crossRecordFindings...)
	for len(content) > 0 {
		line := content
		newline := bytes.IndexByte(content, '\n')
		hasNewline := newline >= 0
		if hasNewline {
			line = content[:newline]
			content = content[newline+1:]
		} else {
			content = nil
		}
		// Keep CRLF framing byte-for-byte for records that are not rewritten.
		carriageReturn := len(line) > 0 && line[len(line)-1] == '\r'
		if carriageReturn {
			line = line[:len(line)-1]
		}
		trimmed := bytes.TrimSpace(line)
		start := out.Len()
		switch {
		case len(trimmed) == 0:
			out.Write(line)
		case json.Valid(trimmed):
			record, recordFindings, err := SanitizeJSONDocument(trimmed, p)
			if err != nil {
				return nil, findings, fmt.Errorf("could not sanitize JSONL record: %w", err)
			}
			findings = append(findings, recordFindings...)
			if len(recordFindings) > 0 {
				out.Write(record)
			} else {
				out.Write(line)
			}
			spans = append(spans, recordSpan{start: start, end: out.Len()})
		default:
			res := p.Scan(string(line))
			if len(res.Findings) > 0 {
				findings = append(findings, res.Findings...)
				out.WriteString(res.Sanitized)
			} else {
				out.Write(line)
			}
			spans = append(spans, recordSpan{start: start, end: out.Len()})
		}
		if carriageReturn {
			out.WriteByte('\r')
		}
		if hasNewline {
			out.WriteByte('\n')
		}
	}

	rebuilt := out.Bytes()
	// Rescan the rebuilt stream only for the secrets no single record can show.
	// The per-record pass has already applied every value pattern to the decoded
	// record, so running them again on the encoded stream re-matches a redaction
	// placeholder followed by JSON escape characters: in encoded JSON a \n escape
	// is two non-space bytes, which lets env_assignment run past the end of the
	// decoded value and swallow the next word. Each such match replaced an
	// already-correct record — and a record whose only secret was an escaped
	// newline — with a cross-record marker, which is exactly the failure
	// redactJSONOutput rules out for single documents (#5705). Only whole-value
	// secrets (registered runtime values and private-key blocks) can still cross
	// a record boundary, so only those are scanned here.
	stream := p.ScanWholeValueSecrets(string(rebuilt))
	if len(stream.Findings) == 0 {
		return rebuilt, findings, nil
	}
	// Every finding a single record could account for was handled above: a
	// within-record whole-value secret is caught by SanitizeJSONDocument, and a
	// private-key block crossing records by failClosedCrossRecordPrivateKeys. What
	// remains spans a record boundary.
	findings = append(findings, stream.Findings...)
	return failClosedRecordSpans(rebuilt, spans, stream), findings, nil
}

// recordSpan is the byte range of one non-empty JSONL record in the rebuilt
// stream, excluding its trailing newline.
type recordSpan struct {
	start, end int
}

// failClosedCrossRecordPrivateKeys replaces every record covered by a
// private-key block that crosses record boundaries with a valid marker. Match
// offsets come straight from the original stream, so localization is exact.
func failClosedCrossRecordPrivateKeys(content []byte) ([]byte, []Finding) {
	spans := jsonLineSpans(content)
	if len(spans) == 0 {
		return content, nil
	}
	// Match in a decoded, order-preserving view so a marker written with
	// \u escapes is still recognized, and keep each record's range in that
	// view so a match maps back to the records it covers.
	var (
		view      bytes.Buffer
		viewSpans []recordSpan
	)
	for _, span := range spans {
		start := view.Len()
		line := content[span.start:span.end]
		if trimmed := bytes.TrimSpace(line); json.Valid(trimmed) {
			writeDecodedJSONView(&view, trimmed)
		} else {
			view.Write(line)
		}
		view.WriteByte('\n')
		viewSpans = append(viewSpans, recordSpan{start: start, end: view.Len() - 1})
	}
	matches := privateKeyRe.FindAllIndex(view.Bytes(), -1)
	covered := make([]bool, len(spans))
	var findings []Finding
	for _, match := range matches {
		var hit []int
		for i, span := range viewSpans {
			if span.end > match[0] && span.start < match[1] {
				hit = append(hit, i)
			}
		}
		// A block confined to one record is handled by that record's own
		// sanitization.
		if len(hit) < 2 {
			continue
		}
		findings = append(findings, Finding{
			Scanner:  "secret_redactor",
			Name:     "private_key",
			Severity: "critical",
			Detail:   "Private key block spans records and cannot be localized; output removed",
		})
		for _, i := range hit {
			covered[i] = true
		}
	}
	if len(findings) == 0 {
		return content, nil
	}
	var out bytes.Buffer
	cursor := 0
	for i, span := range spans {
		if !covered[i] {
			continue
		}
		out.Write(content[cursor:span.start])
		out.WriteString(failClosedCrossRecordJSON)
		cursor = span.end
	}
	out.Write(content[cursor:])
	return out.Bytes(), findings
}

// writeDecodedJSONView appends every decoded object key and string leaf of doc
// to buf in document order, one per line, so a value split across members is
// still visible as one contiguous run of text.
func writeDecodedJSONView(buf *bytes.Buffer, doc []byte) {
	buf.Write(collectJSONLeaves(doc).decoded.Bytes())
}

// collectJSONLeaves walks doc and records every decoded object key and string
// value, in document order, in a view where a value split across members reads as
// one contiguous run of text. Each leaf's range in that view is kept so a match
// can be mapped back to the members that carry it.
func collectJSONLeaves(doc []byte) *jsonProvenance {
	prov := &jsonProvenance{}
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	var walkValue func()
	walkValue = func() {
		tok, err := dec.Token()
		if err != nil {
			return
		}
		switch t := tok.(type) {
		case json.Delim:
			switch t {
			case '{':
				for dec.More() {
					keyTok, err := dec.Token()
					if err != nil {
						return
					}
					key, ok := keyTok.(string)
					if !ok {
						return
					}
					prov.addString(key, true)
					walkValue()
				}
				_, _ = dec.Token() // consume the closing delimiter
			case '[':
				for dec.More() {
					walkValue()
				}
				_, _ = dec.Token() // consume the closing delimiter
			}
		case string:
			prov.addString(t, false)
		}
	}
	walkValue()
	return prov
}

// jsonLineSpans returns the byte range of every non-empty line in content,
// excluding its trailing CR/LF framing.
func jsonLineSpans(content []byte) []recordSpan {
	var spans []recordSpan
	for start := 0; start < len(content); {
		end := bytes.IndexByte(content[start:], '\n')
		if end < 0 {
			end = len(content)
		} else {
			end += start
		}
		lineEnd := end
		if lineEnd > start && content[lineEnd-1] == '\r' {
			lineEnd--
		}
		if len(bytes.TrimSpace(content[start:lineEnd])) > 0 {
			spans = append(spans, recordSpan{start: start, end: lineEnd})
		}
		start = end + 1
	}
	return spans
}

// failClosedRecordSpans replaces the records a whole-stream redaction changed
// with a valid fail-closed marker. Records the redaction did not touch are
// copied through unchanged, so two disjoint cross-record matches never discard
// the clean records between them.
//
// The per-record pass has already handled every match a single record could
// account for, so a remaining match spans a record boundary. Localization is
// conservative but per record, not all-or-nothing: inside the changed region a
// record is kept only when its bytes are unique in the stream and reappear as a
// whole newline-delimited record in the redacted stream. Locating a covered
// record means finding its bytes again in the redacted stream, which is only
// trustworthy when the record is distinct: with a repeated record, a later
// untouched copy can stand in for the copy a match covered, and the original
// bytes — carrying part of the secret — would be written back. Such a record is
// replaced. Records outside the changed region are copied through, duplicates
// included.
func failClosedRecordSpans(stream []byte, spans []recordSpan, scan ScanResult) []byte {
	sanitized := []byte(scan.Sanitized)

	// Changes are confined to the region that differs between the stream and
	// the redacted stream, so records outside it were not touched.
	start, end := changedRegion(stream, sanitized)
	counts := make(map[string]int, len(spans))
	for _, span := range spans {
		counts[string(stream[span.start:span.end])]++
	}

	var out bytes.Buffer
	cursor := 0
	searchFrom := 0
	for _, span := range spans {
		record := stream[span.start:span.end]
		if span.end <= start || span.start >= end {
			// Outside the changed region: the bytes are untouched.
			out.Write(stream[cursor:span.end])
			cursor = span.end
			continue
		}
		idx := bytes.Index(sanitized[searchFrom:], record)
		// A surviving copy must be a whole record, and it must be the only copy:
		// otherwise a later untouched duplicate can stand in for a covered one.
		if counts[string(record)] == 1 && idx >= 0 && lineBounded(sanitized, searchFrom+idx, searchFrom+idx+len(record)) {
			searchFrom += idx + len(record)
			out.Write(stream[cursor:span.end])
			cursor = span.end
			continue
		}
		// The record is not present verbatim, so a match covered part of it (or
		// the scan reported a finding without changing bytes, which cannot be
		// localized at all). Either way it must not be written as-is.
		out.Write(stream[cursor:span.start])
		out.WriteString(failClosedCrossRecordJSON)
		cursor = span.end
	}
	out.Write(stream[cursor:])
	return out.Bytes()
}

// changedRegion returns the half-open byte range that differs between a and b.
// It is approximate for length-changing edits, which is enough: it is used as an
// upper bound on the bytes a redaction touched.
func changedRegion(a, b []byte) (int, int) {
	limit := min(len(a), len(b))
	start := 0
	for start < limit && a[start] == b[start] {
		start++
	}
	end := 0
	for end < limit-start && a[len(a)-1-end] == b[len(b)-1-end] {
		end++
	}
	return start, len(a) - end
}

// lineBounded reports whether data[start:end] is a whole newline-delimited
// line.
func lineBounded(data []byte, start, end int) bool {
	if start > 0 && data[start-1] != '\n' {
		return false
	}
	switch {
	case end == len(data), data[end] == '\n':
		return true
	case data[end] == '\r':
		return end+1 < len(data) && data[end+1] == '\n'
	default:
		return false
	}
}

// rewriteJSONTokens walks every token in doc, sanitizes each object key and
// scalar value, and renders a valid JSON document. Unlike map decoding it
// keeps member order and duplicate keys, so a secret in an earlier, overwritten
// value is still found, and a secret spanning JSON framing stays visible in the
// rewritten bytes. String values that a framing-spanning private key carries key
// material into, and key names whose own text carries a block marker, are
// replaced whole, which is how such a block is neutralized member by member.
func rewriteJSONTokens(doc []byte, p *Pipeline, spans privateKeySpans) ([]byte, []Finding, error) {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	var (
		buf bytes.Buffer
		w   = &jsonRewrite{pipeline: p, spans: spans}
	)
	if err := w.writeValue(dec, &buf, ""); err != nil {
		return nil, nil, fmt.Errorf("could not tokenize JSON: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, nil, fmt.Errorf("unexpected trailing JSON content")
	}
	return buf.Bytes(), w.findings, nil
}

// jsonRewrite is the state one token walk carries: the pipeline, the findings it
// has collected, and the keys and string values a framing-spanning private key
// covers.
type jsonRewrite struct {
	pipeline *Pipeline
	findings []Finding
	spans    privateKeySpans
	// index counts the keys and string values already walked, in document
	// order, so it lines up with the ranges in jsonProvenance.
	index int
}

// nextIndex advances the leaf cursor past the next key or string value and
// returns that leaf's index in the provenance view.
func (w *jsonRewrite) nextIndex() int {
	i := w.index
	w.index++
	return i
}

// valueMasked reports whether the string value at index i carries part of a
// framing-spanning private key and must be replaced whole.
func (w *jsonRewrite) valueMasked(i int) bool { return w.spans.covered[i] }

// keyMasked reports whether the object key at index i carries a block marker and
// must be replaced whole.
func (w *jsonRewrite) keyMasked(i int) bool { return w.spans.maskedKeys[i] }

// writeValue writes one JSON value to buf, sanitizing object keys and scalar
// leaves as it goes. memberKey is the enclosing object key when the value is an
// object member, so sensitive-key masking still applies; it is empty for array
// elements and the top-level value.
func (w *jsonRewrite) writeValue(dec *json.Decoder, buf *bytes.Buffer, memberKey string) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch t := tok.(type) {
	case json.Delim:
		return w.writeContainer(dec, buf, t)
	case string:
		if w.valueMasked(w.nextIndex()) {
			return writeJSONString(buf, privateKeyMask)
		}
		sanitized, stringFindings := sanitizeJSONString(t, memberKey, w.pipeline)
		w.findings = append(w.findings, stringFindings...)
		return writeJSONString(buf, sanitized)
	case json.Number:
		literal := t.String()
		res := w.pipeline.Scan(literal)
		if len(res.Findings) == 0 {
			buf.WriteString(literal)
			return nil
		}
		w.findings = append(w.findings, res.Findings...)
		// A redacted number is no longer a number literal, so write the
		// replacement as a JSON string to keep the document valid.
		return writeJSONString(buf, res.Sanitized)
	case bool:
		buf.WriteString(strconv.FormatBool(t))
		return nil
	case nil:
		buf.WriteString("null")
		return nil
	default:
		return fmt.Errorf("unexpected JSON token %T", tok)
	}
}

// jsonMember is one object member whose value has been rendered but whose name
// is not final yet. Names are resolved once the whole object has been read so a
// sanitized name can never collide with a clean key that appears later.
type jsonMember struct {
	originalKey  string
	sanitizedKey string
	rendered     []byte
}

// writeContainer writes the members of an object or array, preserving member
// order and duplicate keys.
func (w *jsonRewrite) writeContainer(dec *json.Decoder, buf *bytes.Buffer, delim json.Delim) error {
	switch delim {
	case '{':
		var members []jsonMember
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := keyTok.(string)
			if !ok {
				return fmt.Errorf("JSON object key is %T, want string", keyTok)
			}
			masked := w.keyMasked(w.nextIndex())
			sanitizedKey, keyFindings := sanitizeJSONKey(key, w.pipeline)
			if masked {
				// The key name carried a private-key block marker, so it is
				// block content rather than structure and cannot be kept.
				sanitizedKey, keyFindings = privateKeyMask, nil
			}
			w.findings = append(w.findings, keyFindings...)
			var rendered bytes.Buffer
			// The original key decides whether the value is sensitive: the mask
			// that replaced a key must not drag its value into json_field
			// masking.
			if err := w.writeValue(dec, &rendered, key); err != nil {
				return err
			}
			members = append(members, jsonMember{
				originalKey:  key,
				sanitizedKey: sanitizedKey,
				rendered:     rendered.Bytes(),
			})
		}
		if _, err := dec.Token(); err != nil { // consume '}'
			return err
		}
		assignJSONMemberNames(members)
		buf.WriteByte('{')
		for i, m := range members {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeJSONString(buf, m.sanitizedKey); err != nil {
				return err
			}
			buf.WriteByte(':')
			buf.Write(m.rendered)
		}
		buf.WriteByte('}')
		return nil
	case '[':
		buf.WriteByte('[')
		first := true
		for dec.More() {
			if !first {
				buf.WriteByte(',')
			}
			if err := w.writeValue(dec, buf, ""); err != nil {
				return err
			}
			first = false
		}
		if _, err := dec.Token(); err != nil { // consume ']'
			return err
		}
		buf.WriteByte(']')
		return nil
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delim.String())
	}
}

// assignJSONMemberNames resolves the member names of one object. Clean keys keep
// their names, and a sanitized name that would collide with a different key is
// disambiguated. Repeats of the same original duplicate key keep sharing one
// name, so intentional duplicate keys survive sanitization.
func assignJSONMemberNames(members []jsonMember) {
	claimed := make(map[string]string, len(members))
	for _, m := range members {
		if m.sanitizedKey == m.originalKey {
			claimed[m.sanitizedKey] = m.originalKey
		}
	}
	renamed := make(map[string]string, len(members))
	for i := range members {
		m := &members[i]
		if m.sanitizedKey == m.originalKey {
			continue
		}
		if target, ok := renamed[m.originalKey]; ok {
			m.sanitizedKey = target
			continue
		}
		target := m.sanitizedKey
		if owner, taken := claimed[target]; taken && owner != m.originalKey {
			for n := 1; ; n++ {
				cand := fmt.Sprintf("%s_%d", m.sanitizedKey, n)
				if _, exists := claimed[cand]; !exists {
					target = cand
					break
				}
			}
		}
		renamed[m.originalKey] = target
		claimed[target] = m.originalKey
		m.sanitizedKey = target
	}
}

// writeJSONString appends s to buf as a JSON string literal without escaping
// HTML characters, so legitimate markup survives sanitization.
func writeJSONString(buf *bytes.Buffer, s string) error {
	var encoded bytes.Buffer
	enc := json.NewEncoder(&encoded)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return err
	}
	buf.Write(bytes.TrimSuffix(encoded.Bytes(), []byte("\n")))
	return nil
}

// sanitizeJSONKey sanitizes one object key. A key whose sanitized value is
// empty was removed entirely, so it is replaced with a placeholder rather than
// kept as-is.
func sanitizeJSONKey(key string, p *Pipeline) (string, []Finding) {
	res := p.Scan(key)
	if len(res.Findings) == 0 {
		return key, nil
	}
	sanitized := res.Sanitized
	if sanitized == "" {
		sanitized = redactedJSONKey
	}
	return sanitized, res.Findings
}

// sanitizeJSONString sanitizes one string leaf. memberKey is the enclosing
// object key, used to mask values whose key name marks them as sensitive even
// when the value itself matches no secret pattern.
func sanitizeJSONString(value, memberKey string, p *Pipeline) (string, []Finding) {
	res := p.Scan(value)
	out := value
	var findings []Finding
	if len(res.Findings) > 0 {
		findings = append(findings, res.Findings...)
		out = res.Sanitized
	}
	if memberKey != "" && sensitiveJSONKeyRe.MatchString(memberKey) && !isAlreadyMasked(out) &&
		utf8.RuneCountInString(out) >= minJSONFieldLen {
		masked := Mask(out)
		findings = append(findings, Finding{
			Scanner:  "secret_redactor",
			Name:     "json_field",
			Severity: "high",
			Detail:   fmt.Sprintf("Redacted json_field (%d chars) -> %s", utf8.RuneCountInString(out), masked),
		})
		out = masked
	}
	return out, findings
}
