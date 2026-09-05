package integrations

import (
	"fmt"
	"regexp"
	"strings"
)

type OmpProviderSpec struct {
	BaseURL string
	API     string
	APIKey  string
	Models  []Model
}

type YamlLeafPatch struct {
	Kind    string
	Next    string
	Changed bool
	Reason  string
}

type sourceLine struct {
	text    string
	indent  int
	blank   bool
	comment bool
}

// PatchContext addresses one `providers.<id>` leaf in a plain block-map YAML
// document; -1 means "absent" for providersIndex, childIndent, leafStart, and
// leafEnd.
type patchContext struct {
	lines          []sourceLine
	providersIndex int
	containerEnd   int
	childIndent    int
	leafStart      int
	leafEnd        int
}

// UpsertProviderLeaf is the omp wrapper over the shared provider-leaf patcher.
func UpsertProviderLeaf(text string, providerID string, spec OmpProviderSpec) YamlLeafPatch {
	return UpsertProviderLeafBody(text, providerID, "models.yml", func(indent int) string {
		return RenderProviderLeaf(providerID, spec, indent)
	})
}

func RemoveProviderLeaf(text string, providerID string) YamlLeafPatch {
	return RemoveProviderLeafBody(text, providerID, "models.yml")
}

// UpsertProviderLeafBody is a fail-closed, source-preserving patch of one
// `providers.<id>` leaf: every other byte stays exactly as written. Any
// structure the line walker cannot address unambiguously — flow-style values
// on the patched path, tab indentation, duplicate keys — is a refusal, never a
// re-render of the document. The prism leaf itself is prism-owned: a content
// difference rewrites the leaf in place (new model set, format change) rather
// than refusing, matching the reference injector's rewrite-in-place semantics.
func UpsertProviderLeafBody(text, providerID, fileLabel string, renderBody func(indent int) string) YamlLeafPatch {
	scan, refused := scanProviderLeaf(text, providerID, fileLabel)
	if refused != "" {
		return YamlLeafPatch{Kind: "refused", Reason: refused}
	}
	if scan.leafStart == -1 {
		return insertLeaf(scan, providerID, fileLabel, renderBody)
	}
	canonical := renderBody(scan.childIndent)
	existing := leafText(scan.lines, scan.leafStart, scan.leafEnd)
	if existing == canonical {
		return YamlLeafPatch{Kind: "written", Next: joinLines(scan.lines), Changed: false}
	}
	replacement := sourceLines(canonical)
	next := append(append(append([]sourceLine{}, scan.lines[:scan.leafStart]...), replacement...), scan.lines[scan.leafEnd:]...)
	return YamlLeafPatch{Kind: "written", Next: joinLines(next), Changed: true}
}

func RemoveProviderLeafBody(text, providerID, fileLabel string) YamlLeafPatch {
	scan, refused := scanProviderLeaf(text, providerID, fileLabel)
	if refused != "" {
		return YamlLeafPatch{Kind: "refused", Reason: refused}
	}
	if scan.leafStart == -1 {
		return YamlLeafPatch{Kind: "written", Next: joinLines(scan.lines), Changed: false}
	}
	kept := append(append([]sourceLine{}, scan.lines[:scan.leafStart]...), scan.lines[scan.leafEnd:]...)
	if scan.providersIndex != -1 && containerEmpty(kept, scan.providersIndex, scan.childIndent) {
		kept = append(kept[:scan.providersIndex], kept[scan.providersIndex+1:]...)
	}
	return YamlLeafPatch{Kind: "written", Next: joinLines(kept), Changed: true}
}

const (
	LeafPresent = "present"
	LeafAbsent  = "absent"
	LeafRefused = "refused"
)

// ProviderLeafRead is a read-only view of one `providers.<id>` leaf: presence plus the baseUrl it declares.
type ProviderLeafRead struct {
	Kind    string
	BaseURL *string
	Reason  string
}

func ReadProviderLeaf(text string, providerID string) ProviderLeafRead {
	return ReadProviderLeafBody(text, providerID, "models.yml", "baseUrl")
}

func ReadProviderLeafBody(text, providerID, fileLabel, endpointKey string) ProviderLeafRead {
	scan, refused := scanProviderLeaf(text, providerID, fileLabel)
	if refused != "" {
		return ProviderLeafRead{Kind: LeafRefused, Reason: refused}
	}
	if scan.leafStart == -1 {
		return ProviderLeafRead{Kind: LeafAbsent}
	}
	leaf := leafText(scan.lines, scan.leafStart, scan.leafEnd)
	m := yamlEndpointRe(endpointKey).FindStringSubmatch(leaf)
	if m == nil {
		return ProviderLeafRead{Kind: LeafPresent, BaseURL: nil}
	}
	return ProviderLeafRead{Kind: LeafPresent, BaseURL: &m[1]}
}

var yamlEndpointPatterns = map[string]*regexp.Regexp{}

func yamlEndpointRe(key string) *regexp.Regexp {
	if re, ok := yamlEndpointPatterns[key]; ok {
		return re
	}
	re := regexp.MustCompile(`(?m)^[ \t]*` + regexp.QuoteMeta(key) + `:[ \t]*(\S.*?)[ \t]*$`)
	yamlEndpointPatterns[key] = re
	return re
}

func RenderProviderLeaf(providerID string, spec OmpProviderSpec, indent int) string {
	pad := strings.Repeat(" ", indent)
	body := pad + pad
	item := body + pad
	fields := item + pad
	lines := []string{
		pad + providerID + ":",
		body + "baseUrl: " + spec.BaseURL,
		body + "api: " + spec.API,
		body + "apiKey: " + spec.APIKey,
	}
	if len(spec.Models) == 0 {
		lines = append(lines, body+"models: []")
	} else {
		lines = append(lines, body+"models:")
		for _, model := range spec.Models {
			lines = append(lines, item+"- id: "+model.ID)
			lines = append(lines, fields+"name: "+model.Name)
			// omp validates model entries strictly and requires input modalities.
			lines = append(lines, fields+"input:")
			lines = append(lines, fields+pad+"- text")
			if model.ContextWindow > 0 {
				lines = append(lines, fields+"contextWindow: "+fmt.Sprintf("%d", model.ContextWindow))
			}
		}
	}
	return strings.Join(lines, "\n")
}

func scanProviderLeaf(text string, providerID string, fileLabel string) (patchContext, string) {
	lines := sourceLines(text)
	for _, line := range lines {
		if strings.HasPrefix(line.text, "\t") {
			return patchContext{}, "prism: " + providerID + " patch refused — tab indentation in " + fileLabel + " is unsupported"
		}
	}
	providersIndex, refused := findTopLevelKey(lines, "providers")
	if refused != "" {
		return patchContext{}, "prism: " + providerID + " patch refused — " + refused
	}
	if providersIndex == -1 {
		return patchContext{lines: lines, providersIndex: -1, containerEnd: len(lines), childIndent: -1, leafStart: -1, leafEnd: -1}, ""
	}
	containerEnd := blockEnd(lines, providersIndex)
	childIndent := immediateIndent(lines, providersIndex+1, containerEnd)
	leafStart, leafEnd := -1, -1
	if childIndent != -1 {
		start, end, flow, duplicate := findLeaf(lines, providersIndex, containerEnd, childIndent, providerID)
		if flow {
			return patchContext{}, "prism: " + providerID + " patch refused — " + providerID + " exists as a flow-style value"
		}
		if duplicate {
			return patchContext{}, "prism: " + providerID + " patch refused — duplicate " + providerID + " keys under providers"
		}
		leafStart, leafEnd = start, end
	}
	return patchContext{lines: lines, providersIndex: providersIndex, containerEnd: containerEnd, childIndent: childIndent, leafStart: leafStart, leafEnd: leafEnd}, ""
}

func sourceLines(text string) []sourceLine {
	parts := strings.Split(text, "\n")
	lines := make([]sourceLine, len(parts))
	for i, line := range parts {
		trimmed := strings.TrimSpace(line)
		lines[i] = sourceLine{
			text:    line,
			indent:  leadingSpaces(line),
			blank:   trimmed == "",
			comment: strings.HasPrefix(strings.TrimLeft(line, " \t\n\r\v\f"), "#"),
		}
	}
	return lines
}

func leadingSpaces(line string) int {
	n := 0
	for n < len(line) && line[n] == ' ' {
		n++
	}
	return n
}

func joinLines(lines []sourceLine) string {
	texts := make([]string, len(lines))
	for i, line := range lines {
		texts[i] = line.text
	}
	return strings.Join(texts, "\n")
}

// findTopLevelKey returns the index of the single top-level `key:` block-map
// line, or -1; a refused string names duplicates and flow-style values.
func findTopLevelKey(lines []sourceLine, key string) (int, string) {
	pattern := topLevelKeyRe(key)
	index := -1
	for position, line := range lines {
		if line.indent != 0 || line.blank || line.comment || !pattern.MatchString(line.text) {
			continue
		}
		if index != -1 {
			return -1, "duplicate top-level " + key + " keys"
		}
		inline := strings.TrimSpace(line.text[len(key)+1:])
		if inline != "" && !strings.HasPrefix(inline, "#") {
			return -1, key + " is a flow-style value, not a block map"
		}
		index = position
	}
	return index, ""
}

func topLevelKeyRe(key string) *regexp.Regexp {
	return regexp.MustCompile("^" + key + ":(?:\\s.*)?$")
}

// blockEnd returns the first line at or after `start` that a sibling or parent of the block would own.
func blockEnd(lines []sourceLine, start int) int {
	indent := lines[start].indent
	for k := start + 1; k < len(lines); k++ {
		line := lines[k]
		if !line.blank && !line.comment && line.indent <= indent {
			return k
		}
	}
	return len(lines)
}

func immediateIndent(lines []sourceLine, start int, end int) int {
	for k := start; k < end; k++ {
		line := lines[k]
		if !line.blank && !line.comment {
			return line.indent
		}
	}
	return -1
}

func createProvidersBlock(lines []sourceLine, renderBody func(indent int) string) YamlLeafPatch {
	block := append([]string{"providers:"}, strings.Split(renderBody(2), "\n")...)
	allBlank := true
	for _, line := range lines {
		if !line.blank {
			allBlank = false
			break
		}
	}
	if allBlank {
		return YamlLeafPatch{Kind: "written", Next: strings.Join(block, "\n") + "\n", Changed: true}
	}
	textLines := make([]string, len(lines))
	for i, line := range lines {
		textLines[i] = line.text
	}
	at := appendIndex(textLines)
	textLines = append(textLines[:at], append(append([]string{}, block...), textLines[at:]...)...)
	return YamlLeafPatch{Kind: "written", Next: strings.Join(textLines, "\n"), Changed: true}
}

// findLeaf locates the single `providerID:` leaf at childIndent inside the
// providers container, reporting flow-style and duplicate leaves as refusals.
func findLeaf(lines []sourceLine, providersIndex int, containerEnd int, childIndent int, providerID string) (start, end int, flow, duplicate bool) {
	pattern := topLevelKeyRe(providerID)
	found := -1
	for k := providersIndex + 1; k < containerEnd; k++ {
		line := lines[k]
		if line.blank || line.comment || line.indent != childIndent {
			continue
		}
		keyText := line.text[line.indent:]
		if !pattern.MatchString(keyText) {
			continue
		}
		if found != -1 {
			return -1, -1, false, true
		}
		found = k
		inline := strings.TrimSpace(keyText[len(providerID)+1:])
		if inline != "" && !strings.HasPrefix(inline, "#") {
			return -1, -1, true, false
		}
	}
	if found == -1 {
		return -1, -1, false, false
	}
	end = blockEnd(lines, found)
	if end > containerEnd {
		end = containerEnd
	}
	for end > found+1 && (lines[end-1].blank || lines[end-1].comment) {
		end--
	}
	return found, end, false, false
}

func leafText(lines []sourceLine, start int, end int) string {
	return joinLines(lines[start:end])
}

func insertLeaf(context patchContext, providerID, fileLabel string, renderBody func(indent int) string) YamlLeafPatch {
	if context.providersIndex == -1 {
		return createProvidersBlock(context.lines, renderBody)
	}
	childIndent := context.childIndent
	if childIndent == -1 {
		childIndent = context.lines[context.providersIndex].indent + 2
	}
	rendered := strings.Split(renderBody(childIndent), "\n")
	textLines := make([]string, len(context.lines))
	for i, line := range context.lines {
		textLines[i] = line.text
	}
	insertAt := containerAppendIndex(context.lines, context.providersIndex, context.containerEnd)
	textLines = append(textLines[:insertAt], append(append([]string{}, rendered...), textLines[insertAt:]...)...)
	return YamlLeafPatch{Kind: "written", Next: strings.Join(textLines, "\n"), Changed: true}
}
func containerAppendIndex(lines []sourceLine, providersIndex int, containerEnd int) int {
	index := containerEnd
	for index > providersIndex+1 && (lines[index-1].blank || lines[index-1].comment) {
		index--
	}
	return index
}

func appendIndex(textLines []string) int {
	index := len(textLines)
	for index > 0 && strings.TrimSpace(textLines[index-1]) == "" {
		index--
	}
	return index
}

func containerEmpty(lines []sourceLine, providersIndex int, childIndent int) bool {
	for k := providersIndex + 1; k < len(lines); k++ {
		line := lines[k]
		if line.blank || line.comment {
			continue
		}
		if line.indent < childIndent {
			break
		}
		return false
	}
	return true
}

func editedRefusal(providerID string) string {
	return "prism: " + providerID + " leaf in models.yml was edited outside prism; apply left it untouched, rollback removes our leaf only"
}
