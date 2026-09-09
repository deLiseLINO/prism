package integrations

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// JSONPatch mirrors YamlLeafPatch: the next bytes and whether they changed, or
// a named refusal (fail-closed). A refusal with Retryable set is a conflict
// with user-owned bytes that a confirmed (forced) apply may take over;
// structural refusals stay final.
type JSONPatch struct {
	Kind      string
	Next      string
	Changed   bool
	Reason    string
	Retryable bool
}

// JSONScalarEntry is one `"key": "value"` member prism owns inside a container.
type JSONScalarEntry struct {
	Key   string
	Value string
}

// JSONLeafRead mirrors ProviderLeafRead for JSON targets.
type JSONLeafRead struct {
	Kind     string
	Endpoint *string
	Reason   string
}

const (
	jsonLeafPresent = "present"
	jsonLeafAbsent  = "absent"
	jsonLeafRefused = "refused"
)

// jsonString renders a JSON string literal with escaping identical to
// encoding/json, so canonical bytes are deterministic.
func jsonString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `"` + s + `"`
	}
	return string(b)
}

// jsonUnquote resolves a captured raw string body (between the quotes) back to
// its value; on any malformed escape the raw body is returned as-is.
func jsonUnquote(raw string) string {
	var out string
	if err := json.Unmarshal([]byte(`"`+raw+`"`), &out); err != nil {
		return raw
	}
	return out
}

func jsonMemberRe(key string) *regexp.Regexp {
	return regexp.MustCompile(`^"` + regexp.QuoteMeta(key) + `"[ \t]*:`)
}

var jsonScalarValueRe = regexp.MustCompile(`^"((?:[^"\\]|\\.)*)"[ \t]*,?[ \t]*$`)

// jsonDocument is a walked JSON object document: the root object's opener and
// closer line indexes and the member indent. Only pretty-printed block-style
// documents are addressable; everything else refuses.
type jsonDocument struct {
	lines        []sourceLine
	opener       int
	closer       int
	memberIndent int
}

// scanJSONDocument validates the document shape and locates the root object.
// Refusals: tab indentation, JSONC comments, a non-object or non-block-style
// root, an unclosed root object, or trailing content after it.
func scanJSONDocument(text string, fileLabel string) (jsonDocument, string) {
	lines := sourceLines(text)
	for _, line := range lines {
		if strings.HasPrefix(line.text, "\t") {
			return jsonDocument{}, "prism: " + fileLabel + " patch refused — tab indentation is unsupported"
		}
		trimmedLeft := strings.TrimLeft(line.text, " \t\n\r\v\f")
		if strings.HasPrefix(trimmedLeft, "//") || strings.HasPrefix(trimmedLeft, "/*") || strings.HasPrefix(trimmedLeft, "*") || strings.HasPrefix(trimmedLeft, "*/") {
			return jsonDocument{}, "prism: " + fileLabel + " patch refused — comments (JSONC) are unsupported"
		}
	}
	opener := -1
	for i, line := range lines {
		if line.blank {
			continue
		}
		if strings.TrimSpace(line.text) != "{" {
			return jsonDocument{}, "prism: " + fileLabel + " patch refused — root is not a block-style JSON object"
		}
		opener = i
		break
	}
	if opener == -1 {
		return jsonDocument{lines: lines, opener: -1, closer: -1, memberIndent: -1}, ""
	}
	closer := -1
	for i := opener + 1; i < len(lines); i++ {
		if lines[i].blank {
			continue
		}
		if lines[i].indent <= lines[opener].indent {
			if strings.TrimSpace(lines[i].text) == "}" {
				closer = i
			}
			break
		}
	}
	if closer == -1 {
		return jsonDocument{}, "prism: " + fileLabel + " patch refused — root object is missing its closing brace"
	}
	if !allBlankAfter(lines, closer+1) {
		return jsonDocument{}, "prism: " + fileLabel + " patch refused — trailing content after the root object"
	}
	return jsonDocument{lines: lines, opener: opener, closer: closer, memberIndent: immediateIndent(lines, opener+1, closer)}, ""
}

func allBlankAfter(lines []sourceLine, start int) bool {
	for i := start; i < len(lines); i++ {
		if !lines[i].blank {
			return false
		}
	}
	return true
}

func memberInline(text, key string) (string, bool) {
	rest := strings.TrimLeft(text, " ")
	loc := jsonMemberRe(key).FindStringIndex(rest)
	if loc == nil {
		return "", false
	}
	return strings.TrimSpace(rest[loc[1]:]), true
}

// findJSONMember locates the single `key:` member line of the container span
// [containerStart, containerEnd) at the given indent. Duplicate keys refuse.
func findJSONMember(lines []sourceLine, containerStart, containerEnd, indent int, key, fileLabel string) (int, string, string) {
	found := -1
	inline := ""
	for i := containerStart + 1; i < containerEnd; i++ {
		line := lines[i]
		if line.blank || line.indent != indent {
			continue
		}
		rest, ok := memberInline(line.text, key)
		if !ok {
			continue
		}
		if found != -1 {
			return -1, "", "prism: " + fileLabel + " patch refused — duplicate \"" + key + "\" keys"
		}
		found = i
		inline = rest
	}
	return found, inline, ""
}

// jsonBlockEnd finds the closer line of the object whose member line is start
// (inline remainder `{`), or refuses when no closer arrives in span.
func jsonBlockEnd(lines []sourceLine, start, containerEnd int, fileLabel string) (int, string) {
	indent := lines[start].indent
	for i := start + 1; i < containerEnd; i++ {
		line := lines[i]
		if line.blank {
			continue
		}
		if line.indent <= indent {
			trimmed := strings.TrimSpace(line.text)
			if trimmed == "}" || trimmed == "}," {
				return i, ""
			}
			break
		}
	}
	return -1, "prism: " + fileLabel + " patch refused — object opened at line " + strconv.Itoa(start+1) + " is not closed before its parent"
}

// locateJSONContainer finds the container member of the root object: member
// line index, closer line index (equal to the member line for an inline `{}`
// container), and the child indent. Absent container returns start -1.
func locateJSONContainer(doc jsonDocument, containerKey, fileLabel string) (start, closer, childIndent int, refusal string) {
	if doc.opener == -1 {
		return -1, -1, -1, ""
	}
	found, inline, refused := findJSONMember(doc.lines, doc.opener, doc.closer, doc.memberIndent, containerKey, fileLabel)
	if refused != "" {
		return -1, -1, -1, refused
	}
	if found == -1 {
		return -1, -1, -1, ""
	}
	if inline == "{" {
		end, refused := jsonBlockEnd(doc.lines, found, doc.closer, fileLabel)
		if refused != "" {
			return -1, -1, -1, refused
		}
		return found, end, immediateIndent(doc.lines, found+1, end), ""
	}
	if inline == "{}" || inline == "{}," {
		return found, found, -1, ""
	}
	return -1, -1, -1, "prism: " + fileLabel + " patch refused — \"" + containerKey + "\" is a flow-style or non-object value"
}

func endsWithComma(text string) bool {
	return strings.HasSuffix(strings.TrimRight(text, " \t\r"), ",")
}

func stripTrailingComma(text string) string {
	trimmed := strings.TrimRight(text, " \t\r")
	if strings.HasSuffix(trimmed, ",") {
		return trimmed[:len(trimmed)-1]
	}
	return text
}

func appendComma(text string) string {
	return strings.TrimRight(text, " \t\r") + ","
}

// lastContentLine returns the index of the last non-blank line in [from, to).
func lastContentLine(lines []sourceLine, from, to int) int {
	for i := to - 1; i >= from; i-- {
		if !lines[i].blank {
			return i
		}
	}
	return -1
}

func jsonLines(parts ...string) []sourceLine {
	out := make([]sourceLine, len(parts))
	for i, part := range parts {
		out[i] = sourceLine{text: part, indent: leadingSpaces(part), blank: strings.TrimSpace(part) == ""}
	}
	return out
}

func spliceLines(lines []sourceLine, cutFrom, cutTo int, replacement []sourceLine) []sourceLine {
	next := make([]sourceLine, 0, len(lines)+len(replacement))
	next = append(next, lines[:cutFrom]...)
	next = append(next, replacement...)
	next = append(next, lines[cutTo:]...)
	return next
}

func jsonPatchWritten(lines []sourceLine, changed bool) JSONPatch {
	return JSONPatch{Kind: "written", Next: joinLines(lines), Changed: changed}
}

func jsonPatchRefused(reason string) JSONPatch {
	return JSONPatch{Kind: "refused", Reason: reason}
}

func jsonPatchForceable(reason string) JSONPatch {
	return JSONPatch{Kind: "refused", Reason: reason, Retryable: true}
}

// UpsertJSONBlockLeaf patches one `container.leaf` object member into a
// pretty-printed JSON document. renderBody receives the leaf key line's indent
// and returns the leaf's full lines (opening `"leaf": {` through closing `}`,
// no trailing commas — the patcher adds them). Only the leaf's own lines
// change; every other byte stays exactly as written. A content difference
// rewrites the leaf in place; a one-line or `{}` leaf is expanded.
func UpsertJSONBlockLeaf(text, containerKey, leafKey, fileLabel string, renderBody func(leafIndent int) string) JSONPatch {
	doc, refused := scanJSONDocument(text, fileLabel)
	if refused != "" {
		return jsonPatchRefused(refused)
	}
	if doc.opener == -1 {
		return insertRootContainerWithLeaf(doc, containerKey, renderBody)
	}
	containerStart, containerEnd, childIndent, refused := locateJSONContainer(doc, containerKey, fileLabel)
	if refused != "" {
		return jsonPatchRefused(refused)
	}
	if containerStart == -1 {
		return insertContainerMemberWithLeaf(doc, containerKey, renderBody)
	}
	if childIndent == -1 {
		childIndent = doc.memberIndent + 2
	}
	leafLine, inline, refused := findJSONMember(doc.lines, containerStart, containerEnd, childIndent, leafKey, fileLabel)
	if refused != "" {
		return jsonPatchRefused(refused)
	}
	body := jsonLines(strings.Split(renderBody(childIndent), "\n")...)
	if leafLine == -1 {
		return insertLeafIntoContainer(doc, containerStart, containerEnd, body)
	}
	if inline == "{" {
		leafEnd, endRefused := jsonBlockEnd(doc.lines, leafLine, containerEnd, fileLabel)
		if endRefused != "" {
			return jsonPatchRefused(endRefused)
		}
		if endsWithComma(doc.lines[leafEnd].text) {
			body[len(body)-1].text = appendComma(body[len(body)-1].text)
		} else {
			body[len(body)-1].text = stripTrailingComma(body[len(body)-1].text)
		}
		if leafText(doc.lines, leafLine, leafEnd+1) == joinLines(body) {
			return jsonPatchWritten(doc.lines, false)
		}
		return replaceLeafLines(doc, leafLine, leafEnd, body)
	}
	if strings.HasPrefix(inline, "{") {
		return replaceLeafLines(doc, leafLine, leafLine, body)
	}
	return jsonPatchRefused("prism: " + fileLabel + " patch refused — \"" + leafKey + "\" under \"" + containerKey + "\" is a non-object value")
}

func replaceLeafLines(doc jsonDocument, leafStart, leafEnd int, body []sourceLine) JSONPatch {
	lines := doc.lines
	hadComma := endsWithComma(lines[leafEnd].text)
	if hadComma && !endsWithComma(body[len(body)-1].text) {
		body[len(body)-1].text = appendComma(body[len(body)-1].text)
	}
	if !hadComma {
		body[len(body)-1].text = stripTrailingComma(body[len(body)-1].text)
	}
	body[0].indent = lines[leafStart].indent
	next := spliceLines(lines, leafStart, leafEnd+1, body)
	return jsonPatchWritten(next, true)
}

func insertLeafIntoContainer(doc jsonDocument, containerStart, containerEnd int, body []sourceLine) JSONPatch {
	lines := doc.lines
	if containerStart == containerEnd {
		openerText := strings.TrimSuffix(strings.TrimSuffix(strings.TrimRight(lines[containerStart].text, " \t\r"), ","), "{}") + "{"
		expanded := []sourceLine{{text: openerText, indent: lines[containerStart].indent}}
		expanded = append(expanded, body...)
		closerText := strings.Repeat(" ", lines[containerStart].indent) + "}"
		if endsWithComma(lines[containerStart].text) {
			closerText += ","
		}
		expanded = append(expanded, sourceLine{text: closerText, indent: lines[containerStart].indent})
		next := spliceLines(lines, containerStart, containerStart+1, expanded)
		return jsonPatchWritten(next, true)
	}
	if prev := lastContentLine(lines, containerStart+1, containerEnd); prev != -1 && !endsWithComma(lines[prev].text) {
		lines[prev].text = appendComma(lines[prev].text)
	}
	body[len(body)-1].text = stripTrailingComma(body[len(body)-1].text)
	next := spliceLines(lines, containerEnd, containerEnd, body)
	return jsonPatchWritten(next, true)
}

func insertContainerMemberWithLeaf(doc jsonDocument, containerKey string, renderBody func(leafIndent int) string) JSONPatch {
	lines := doc.lines
	memberIndent := doc.memberIndent
	if memberIndent == -1 {
		memberIndent = 2
	}
	body := jsonLines(strings.Split(renderBody(memberIndent+2), "\n")...)
	block := []sourceLine{{text: strings.Repeat(" ", memberIndent) + jsonString(containerKey) + ": {", indent: memberIndent}}
	block = append(block, body...)
	block = append(block, sourceLine{text: strings.Repeat(" ", memberIndent) + "}", indent: memberIndent})
	if prev := lastContentLine(lines, doc.opener+1, doc.closer); prev != -1 && !endsWithComma(lines[prev].text) {
		lines[prev].text = appendComma(lines[prev].text)
	}
	next := spliceLines(lines, doc.closer, doc.closer, block)
	return jsonPatchWritten(next, true)
}

func insertRootContainerWithLeaf(doc jsonDocument, containerKey string, renderBody func(leafIndent int) string) JSONPatch {
	body := jsonLines(strings.Split(renderBody(4), "\n")...)
	lines := jsonLines("{", "  "+jsonString(containerKey)+": {")
	lines = append(lines, body...)
	lines = append(lines, sourceLine{text: "  }", indent: 2}, sourceLine{text: "}", indent: 0}, sourceLine{text: "", indent: 0})
	return jsonPatchWritten(lines, true)
}

// RemoveJSONBlockLeaf removes exactly the `container.leaf` member (and the
// container member line itself when the container becomes empty). User bytes
// elsewhere are preserved verbatim.
func RemoveJSONBlockLeaf(text, containerKey, leafKey, fileLabel string) JSONPatch {
	doc, refused := scanJSONDocument(text, fileLabel)
	if refused != "" {
		return jsonPatchRefused(refused)
	}
	if doc.opener == -1 {
		return jsonPatchWritten(doc.lines, false)
	}
	containerStart, containerEnd, childIndent, refused := locateJSONContainer(doc, containerKey, fileLabel)
	if refused != "" {
		return jsonPatchRefused(refused)
	}
	if containerStart == -1 || containerStart == containerEnd {
		return jsonPatchWritten(doc.lines, false)
	}
	leafLine, inline, leafRefused := findJSONMember(doc.lines, containerStart, containerEnd, childIndent, leafKey, fileLabel)
	if leafRefused != "" {
		return jsonPatchRefused(leafRefused)
	}
	if leafLine == -1 {
		return jsonPatchWritten(doc.lines, false)
	}
	leafEnd := leafLine
	if inline == "{" {
		end, refused := jsonBlockEnd(doc.lines, leafLine, containerEnd, fileLabel)
		if refused != "" {
			return jsonPatchRefused(refused)
		}
		leafEnd = end
	}
	lines := doc.lines
	next := spliceLines(lines, leafLine, leafEnd+1, nil)
	newContainerEnd := containerEnd - (leafEnd + 1 - leafLine)
	if containerMembersAllBlank(next, containerStart, newContainerEnd) {
		next = pruneJSONContainer(next, doc.opener, containerStart, newContainerEnd)
		return jsonPatchWritten(next, true)
	}
	if idx := lastContentLine(next, containerStart+1, newContainerEnd); idx != -1 && endsWithComma(next[idx].text) {
		next[idx].text = stripTrailingComma(next[idx].text)
	}
	return jsonPatchWritten(next, true)
}

func pruneJSONContainer(lines []sourceLine, parentOpener, containerStart, containerEnd int) []sourceLine {
	lines = spliceLines(lines, containerStart, containerEnd+1, nil)
	if idx := lastContentLine(lines, parentOpener+1, containerStart); idx != -1 && endsWithComma(lines[idx].text) {
		lines[idx].text = stripTrailingComma(lines[idx].text)
	}
	return lines
}

func containerMembersAllBlank(lines []sourceLine, containerStart, containerEnd int) bool {
	for i := containerStart + 1; i < containerEnd; i++ {
		if !lines[i].blank {
			return false
		}
	}
	return true
}

// ReadJSONBlockLeaf derives the leaf's presence and endpoint from the bytes.
func ReadJSONBlockLeaf(text, containerKey, leafKey, fileLabel, endpointKey string) JSONLeafRead {
	doc, refused := scanJSONDocument(text, fileLabel)
	if refused != "" {
		return JSONLeafRead{Kind: jsonLeafRefused, Reason: refused}
	}
	if doc.opener == -1 {
		return JSONLeafRead{Kind: jsonLeafAbsent}
	}
	containerStart, containerEnd, childIndent, refused := locateJSONContainer(doc, containerKey, fileLabel)
	if refused != "" {
		return JSONLeafRead{Kind: jsonLeafRefused, Reason: refused}
	}
	if containerStart == -1 {
		return JSONLeafRead{Kind: jsonLeafAbsent}
	}
	leafLine, inline, leafRefused := findJSONMember(doc.lines, containerStart, containerEnd, childIndent, leafKey, fileLabel)
	if leafRefused != "" {
		return JSONLeafRead{Kind: jsonLeafRefused, Reason: leafRefused}
	}
	if leafLine == -1 {
		return JSONLeafRead{Kind: jsonLeafAbsent}
	}
	leafEnd := leafLine
	if inline == "{" {
		if end, ok := jsonBlockEndSafe(doc.lines, leafLine, containerEnd); ok {
			leafEnd = end
		}
	}
	leaf := leafText(doc.lines, leafLine, leafEnd+1)
	if m := jsonEndpointRe(endpointKey).FindStringSubmatch(leaf); m != nil {
		endpoint := jsonUnquote(m[1])
		return JSONLeafRead{Kind: jsonLeafPresent, Endpoint: &endpoint}
	}
	return JSONLeafRead{Kind: jsonLeafPresent, Endpoint: nil}
}

func jsonBlockEndSafe(lines []sourceLine, start, containerEnd int) (int, bool) {
	indent := lines[start].indent
	for i := start + 1; i < containerEnd; i++ {
		if lines[i].blank {
			continue
		}
		if lines[i].indent <= indent {
			trimmed := strings.TrimSpace(lines[i].text)
			return i, trimmed == "}" || trimmed == "},"
		}
	}
	return start, false
}

var jsonEndpointPatterns = map[string]*regexp.Regexp{}

func jsonEndpointRe(key string) *regexp.Regexp {
	if re, ok := jsonEndpointPatterns[key]; ok {
		return re
	}
	re := regexp.MustCompile(`(?m)^[ \t]*"` + regexp.QuoteMeta(key) + `"[ \t]*:[ \t]*"((?:[^"\\]|\\.)*)"[ \t]*,?[ \t]*$`)
	jsonEndpointPatterns[key] = re
	return re
}

// UpsertJSONScalarKeys patches a fixed set of `"key": "value"` members inside
// one container. Ownership is derived from the bytes: when any target key
// already carries its canonical value the set is prism-owned and is rewritten
// in place (healing partial states); when target keys hold other values and no
// canonical value is present they are user-owned and the patch refuses before
// any write.
func UpsertJSONScalarKeys(text, containerKey, fileLabel string, entries []JSONScalarEntry) JSONPatch {
	return UpsertJSONScalarKeysForced(text, containerKey, fileLabel, entries, false)
}

// UpsertJSONScalarKeysForced takes over user-owned scalar keys after an
// explicit confirmation: the displaced key lines are journaled in the fence
// comment of the sibling leaf so rollback restores them verbatim.
func UpsertJSONScalarKeysForced(text, containerKey, fileLabel string, entries []JSONScalarEntry, force bool) JSONPatch {
	doc, refused := scanJSONDocument(text, fileLabel)
	if refused != "" {
		return jsonPatchRefused(refused)
	}
	if doc.opener == -1 {
		return insertRootContainerWithKeys(containerKey, entries)
	}
	containerStart, containerEnd, childIndent, refused := locateJSONContainer(doc, containerKey, fileLabel)
	if refused != "" {
		return jsonPatchRefused(refused)
	}
	if containerStart == -1 {
		return insertContainerMemberWithKeys(doc, containerKey, entries)
	}
	if containerStart == containerEnd {
		return expandInlineContainerWithKeys(doc, containerStart, doc.memberIndent+2, entries)
	}
	if childIndent == -1 {
		childIndent = doc.memberIndent + 2
	}
	prismOwned := false
	var userOwned []string
	for _, entry := range entries {
		line, value, entryRefused := findJSONScalarMember(doc.lines, containerStart, containerEnd, childIndent, entry.Key, fileLabel)
		if entryRefused != "" {
			return jsonPatchRefused(entryRefused)
		}
		if line == -1 {
			continue
		}
		if value == entry.Value {
			prismOwned = true
		} else {
			userOwned = append(userOwned, entry.Key)
		}
	}
	if len(userOwned) > 0 && !prismOwned {
		if !force {
			return jsonPatchForceable("prism: " + fileLabel + " patch refused — " + strings.Join(userOwned, ", ") + " under \"" + containerKey + "\" is user-owned; remove or rename it before applying")
		}
	}
	lines := doc.lines
	changed := false
	for _, entry := range entries {
		line, value, _ := findJSONScalarMember(lines, containerStart, containerEnd, childIndent, entry.Key, fileLabel)
		rendered := strings.Repeat(" ", childIndent) + jsonString(entry.Key) + ": " + jsonString(entry.Value)
		if line == -1 {
			if prev := lastContentLine(lines, containerStart+1, containerEnd); prev != -1 && !endsWithComma(lines[prev].text) {
				lines[prev].text = appendComma(lines[prev].text)
			}
			lines = spliceLines(lines, containerEnd, containerEnd, jsonLines(rendered))
			containerEnd++
			changed = true
			continue
		}
		if value == entry.Value {
			continue
		}
		hadComma := endsWithComma(lines[line].text)
		lines[line].text = rendered
		if hadComma {
			lines[line].text = appendComma(lines[line].text)
		}
		changed = true
	}
	return jsonPatchWritten(lines, changed)
}

func expandInlineContainerWithKeys(doc jsonDocument, containerStart, childIndent int, entries []JSONScalarEntry) JSONPatch {
	openerText := strings.TrimSuffix(strings.TrimSuffix(strings.TrimRight(doc.lines[containerStart].text, " \t\r"), ","), "{}") + "{"
	expanded := []sourceLine{{text: openerText, indent: doc.lines[containerStart].indent}}
	for i, entry := range entries {
		line := strings.Repeat(" ", childIndent) + jsonString(entry.Key) + ": " + jsonString(entry.Value)
		if i < len(entries)-1 {
			line += ","
		}
		expanded = append(expanded, sourceLine{text: line, indent: childIndent})
	}
	closerText := strings.Repeat(" ", doc.lines[containerStart].indent) + "}"
	if endsWithComma(doc.lines[containerStart].text) {
		closerText += ","
	}
	expanded = append(expanded, sourceLine{text: closerText, indent: doc.lines[containerStart].indent})
	next := spliceLines(doc.lines, containerStart, containerStart+1, expanded)
	return jsonPatchWritten(next, true)
}

func insertContainerMemberWithKeys(doc jsonDocument, containerKey string, entries []JSONScalarEntry) JSONPatch {
	lines := doc.lines
	memberIndent := doc.memberIndent
	if memberIndent == -1 {
		memberIndent = 2
	}
	block := []sourceLine{{text: strings.Repeat(" ", memberIndent) + jsonString(containerKey) + ": {", indent: memberIndent}}
	for i, entry := range entries {
		line := strings.Repeat(" ", memberIndent+2) + jsonString(entry.Key) + ": " + jsonString(entry.Value)
		if i < len(entries)-1 {
			line += ","
		}
		block = append(block, sourceLine{text: line, indent: memberIndent + 2})
	}
	block = append(block, sourceLine{text: strings.Repeat(" ", memberIndent) + "}", indent: memberIndent})
	if prev := lastContentLine(lines, doc.opener+1, doc.closer); prev != -1 && !endsWithComma(lines[prev].text) {
		lines[prev].text = appendComma(lines[prev].text)
	}
	next := spliceLines(lines, doc.closer, doc.closer, block)
	return jsonPatchWritten(next, true)
}

func insertRootContainerWithKeys(containerKey string, entries []JSONScalarEntry) JSONPatch {
	lines := jsonLines("{")
	lines = append(lines, sourceLine{text: "  " + jsonString(containerKey) + ": {", indent: 2})
	for i, entry := range entries {
		line := "    " + jsonString(entry.Key) + ": " + jsonString(entry.Value)
		if i < len(entries)-1 {
			line += ","
		}
		lines = append(lines, sourceLine{text: line, indent: 4})
	}
	lines = append(lines, sourceLine{text: "  }", indent: 2}, sourceLine{text: "}", indent: 0}, sourceLine{text: "", indent: 0})
	return jsonPatchWritten(lines, true)
}

// findJSONScalarMember locates one string-valued member line of a container.
// A present-but-non-string value refuses.
func findJSONScalarMember(lines []sourceLine, containerStart, containerEnd, indent int, key, fileLabel string) (int, string, string) {
	line, inline, refused := findJSONMember(lines, containerStart, containerEnd, indent, key, fileLabel)
	if refused != "" || line == -1 {
		return -1, "", refused
	}
	m := jsonScalarValueRe.FindStringSubmatch(inline)
	if m == nil {
		return -1, "", "prism: " + fileLabel + " patch refused — \"" + key + "\" under its container is not a JSON string value"
	}
	return line, jsonUnquote(m[1]), ""
}

// RemoveJSONScalarKeys removes exactly the named members from the container
// (and the container member line when the container becomes empty), whatever
// value they now carry.
func RemoveJSONScalarKeys(text, containerKey, fileLabel string, keys []string) JSONPatch {
	doc, refused := scanJSONDocument(text, fileLabel)
	if refused != "" {
		return jsonPatchRefused(refused)
	}
	if doc.opener == -1 {
		return jsonPatchWritten(doc.lines, false)
	}
	containerStart, containerEnd, childIndent, refused := locateJSONContainer(doc, containerKey, fileLabel)
	if refused != "" {
		return jsonPatchRefused(refused)
	}
	if containerStart == -1 || containerStart == containerEnd {
		return jsonPatchWritten(doc.lines, false)
	}
	lines := doc.lines
	removedAny := false
	for _, key := range keys {
		line, inline, keyRefused := findJSONMember(lines, containerStart, containerEnd, childIndent, key, fileLabel)
		if keyRefused != "" {
			return jsonPatchRefused(keyRefused)
		}
		if line == -1 {
			continue
		}
		end := line
		if inline == "{" {
			blockEnd, refused := jsonBlockEnd(lines, line, containerEnd, fileLabel)
			if refused != "" {
				return jsonPatchRefused(refused)
			}
			end = blockEnd
		} else if strings.HasPrefix(inline, "{") || strings.HasPrefix(inline, "[") {
			return jsonPatchRefused("prism: " + fileLabel + " patch refused — \"" + key + "\" under its container is a flow-style value")
		}
		lines = spliceLines(lines, line, end+1, nil)
		containerEnd -= end + 1 - line
		removedAny = true
	}
	if !removedAny {
		return jsonPatchWritten(lines, false)
	}
	if containerMembersAllBlank(lines, containerStart, containerEnd) {
		lines = pruneJSONContainer(lines, doc.opener, containerStart, containerEnd)
		return jsonPatchWritten(lines, true)
	}
	if idx := lastContentLine(lines, containerStart+1, containerEnd); idx != -1 && endsWithComma(lines[idx].text) {
		lines[idx].text = stripTrailingComma(lines[idx].text)
	}
	return jsonPatchWritten(lines, true)
}

// ReadJSONScalarKeys derives presence from any target key and the endpoint
// from the endpoint key's value.
func ReadJSONScalarKeys(text, containerKey, endpointKey, fileLabel string, keys []string) JSONLeafRead {
	doc, refused := scanJSONDocument(text, fileLabel)
	if refused != "" {
		return JSONLeafRead{Kind: jsonLeafRefused, Reason: refused}
	}
	if doc.opener == -1 {
		return JSONLeafRead{Kind: jsonLeafAbsent}
	}
	containerStart, containerEnd, childIndent, refused := locateJSONContainer(doc, containerKey, fileLabel)
	if refused != "" {
		return JSONLeafRead{Kind: jsonLeafRefused, Reason: refused}
	}
	if containerStart == -1 {
		return JSONLeafRead{Kind: jsonLeafAbsent}
	}
	anyPresent := false
	for _, key := range keys {
		line, _, keyRefused := findJSONMember(doc.lines, containerStart, containerEnd, childIndent, key, fileLabel)
		if keyRefused != "" {
			return JSONLeafRead{Kind: jsonLeafRefused, Reason: keyRefused}
		}
		if line != -1 {
			anyPresent = true
			break
		}
	}
	if !anyPresent {
		return JSONLeafRead{Kind: jsonLeafAbsent}
	}
	if line, value, malformed := findJSONScalarMember(doc.lines, containerStart, containerEnd, childIndent, endpointKey, fileLabel); malformed == "" && line != -1 {
		return JSONLeafRead{Kind: jsonLeafPresent, Endpoint: &value}
	}
	return JSONLeafRead{Kind: jsonLeafPresent, Endpoint: nil}
}
