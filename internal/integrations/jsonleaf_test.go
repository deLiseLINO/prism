package integrations

import (
	"encoding/json"
	"strings"
	"testing"
)

func testJSONLeafBody(leafIndent int) string {
	pad := strings.Repeat(" ", leafIndent)
	inner := strings.Repeat(" ", leafIndent+2)
	return strings.Join([]string{
		pad + `"prism": {`,
		inner + `"baseUrl": "http://127.0.0.1:8787/v1",`,
		inner + `"api": "openai-completions",`,
		inner + `"apiKey": "prism-loopback"`,
		pad + `}`,
	}, "\n")
}

func upsertTestLeaf(text string) JSONPatch {
	return UpsertJSONBlockLeaf(text, "providers", "prism", "models.json", testJSONLeafBody)
}

func removeTestLeaf(text string) JSONPatch {
	return RemoveJSONBlockLeaf(text, "providers", "prism", "models.json")
}

func readTestLeaf(text string) JSONLeafRead {
	return ReadJSONBlockLeaf(text, "providers", "prism", "models.json", "baseUrl")
}

func assertValidJSON(t *testing.T, text string) {
	t.Helper()
	if !json.Valid([]byte(text)) {
		t.Fatalf("output is not valid JSON:\n%s", text)
	}
}

func TestJSONLeafApplyCreatesLeafInFreshDocument(t *testing.T) {
	patch := upsertTestLeaf("")
	if patch.Kind != "written" || !patch.Changed {
		t.Fatalf("expected written change, got %+v", patch)
	}
	assertValidJSON(t, patch.Next)
	if !strings.Contains(patch.Next, `"prism": {`) || !strings.Contains(patch.Next, `"baseUrl": "http://127.0.0.1:8787/v1"`) {
		t.Fatalf("leaf missing from fresh document:\n%s", patch.Next)
	}
	again := upsertTestLeaf(patch.Next)
	if again.Kind != "written" || again.Changed {
		t.Fatalf("second apply is not a no-op: %+v", again)
	}
}

func TestJSONLeafApplyPatchesOnlyPrismLeaf(t *testing.T) {
	seed := "{\n  \"theme\": \"dark\",\n  \"providers\": {\n    \"openai\": {\n      \"baseUrl\": \"https://api.openai.com/v1\"\n    }\n  }\n}\n"
	patch := upsertTestLeaf(seed)
	if patch.Kind != "written" || !patch.Changed {
		t.Fatalf("expected written change, got %+v", patch)
	}
	assertValidJSON(t, patch.Next)
	if !strings.Contains(patch.Next, `"theme": "dark",`) || !strings.Contains(patch.Next, `"openai": {`) {
		t.Fatalf("user members lost:\n%s", patch.Next)
	}
	if !strings.Contains(patch.Next, `"prism": {`) {
		t.Fatalf("prism leaf missing:\n%s", patch.Next)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(patch.Next), &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc["providers"].(map[string]any)["prism"].(map[string]any); !ok {
		t.Fatalf("prism leaf not a JSON object member:\n%s", patch.Next)
	}
}

func TestJSONLeafApplyRewritesOwnLeafInPlace(t *testing.T) {
	seed := "{\n  \"providers\": {\n    \"prism\": {\n      \"baseUrl\": \"http://127.0.0.1:9999/v1\"\n    }\n  }\n}\n"
	patch := upsertTestLeaf(seed)
	if patch.Kind != "written" || !patch.Changed {
		t.Fatalf("expected rewrite, got %+v", patch)
	}
	assertValidJSON(t, patch.Next)
	if !strings.Contains(patch.Next, `"baseUrl": "http://127.0.0.1:8787/v1"`) {
		t.Fatalf("stale endpoint survived:\n%s", patch.Next)
	}
}

func TestJSONLeafExpandsOneLineAndEmptyLeaves(t *testing.T) {
	seeds := []string{
		"{\n  \"providers\": {\n    \"prism\": { \"baseUrl\": \"http://old/v1\" }\n  }\n}\n",
		"{\n  \"providers\": {\n    \"prism\": {}\n  }\n}\n",
		"{\n  \"providers\": {}\n}\n",
		"{\n  \"providers\": {},\n  \"other\": 1\n}\n",
	}
	for _, seed := range seeds {
		patch := upsertTestLeaf(seed)
		if patch.Kind != "written" || !patch.Changed {
			t.Fatalf("seed %q: expected expansion, got %+v", seed, patch)
		}
		assertValidJSON(t, patch.Next)
		if !strings.Contains(patch.Next, `"baseUrl": "http://127.0.0.1:8787/v1"`) {
			t.Fatalf("seed %q: canonical leaf missing:\n%s", seed, patch.Next)
		}
	}
}

func TestJSONLeafRemoveStripsLeafAndPrunesEmptyContainer(t *testing.T) {
	seed := "{\n  \"providers\": {\n    \"prism\": {\n      \"baseUrl\": \"http://127.0.0.1:8787/v1\"\n    }\n  }\n}\n"
	patch := removeTestLeaf(seed)
	if patch.Kind != "written" || !patch.Changed {
		t.Fatalf("expected removal, got %+v", patch)
	}
	if strings.Contains(patch.Next, "prism") || strings.Contains(patch.Next, "providers") {
		t.Fatalf("prism leaf or empty container survived:\n%s", patch.Next)
	}
	if patch.Next != "{\n}\n" {
		t.Fatalf("rollback bytes mismatch:\n%s", patch.Next)
	}
}

func TestJSONLeafRemovePreservesSiblingsAndCommas(t *testing.T) {
	seed := "{\n  \"providers\": {\n    \"openai\": {\n      \"baseUrl\": \"https://api.openai.com/v1\"\n    },\n    \"prism\": {\n      \"baseUrl\": \"http://127.0.0.1:8787/v1\"\n    },\n    \"anthropic\": {\n      \"baseUrl\": \"https://api.anthropic.com\"\n    }\n  }\n}\n"
	patch := removeTestLeaf(seed)
	if patch.Kind != "written" || !patch.Changed {
		t.Fatalf("expected removal, got %+v", patch)
	}
	assertValidJSON(t, patch.Next)
	if !strings.Contains(patch.Next, `"openai"`) || !strings.Contains(patch.Next, `"anthropic"`) {
		t.Fatalf("siblings lost:\n%s", patch.Next)
	}
	if strings.Contains(patch.Next, "prism") {
		t.Fatalf("prism leaf survived:\n%s", patch.Next)
	}
}

func TestJSONLeafRemoveStripsDanglingCommaWhenLastMemberRemoved(t *testing.T) {
	seed := "{\n  \"providers\": {\n    \"openai\": {\n      \"baseUrl\": \"https://api.openai.com/v1\"\n    },\n    \"prism\": {\n      \"baseUrl\": \"http://127.0.0.1:8787/v1\"\n    }\n  }\n}\n"
	patch := removeTestLeaf(seed)
	if patch.Kind != "written" || !patch.Changed {
		t.Fatalf("expected removal, got %+v", patch)
	}
	assertValidJSON(t, patch.Next)
}

func TestJSONLeafRemoveIsHonestNoOpWithoutLeaf(t *testing.T) {
	seed := "{\n  \"providers\": {\n    \"openai\": {\n      \"baseUrl\": \"https://api.openai.com/v1\"\n    }\n  }\n}\n"
	patch := removeTestLeaf(seed)
	if patch.Kind != "written" || patch.Changed {
		t.Fatalf("expected no-change removal, got %+v", patch)
	}
	if patch.Next != seed {
		t.Fatalf("bytes changed on no-op:\n%s", patch.Next)
	}
}

func TestJSONLeafRoundTripRestoresUserBytesVerbatim(t *testing.T) {
	seed := "{\n  \"theme\": \"dark\",\n  \"providers\": {\n    \"openai\": {\n      \"baseUrl\": \"https://api.openai.com/v1\"\n    }\n  }\n}\n"
	applied := upsertTestLeaf(seed)
	removed := removeTestLeaf(applied.Next)
	if removed.Kind != "written" || !removed.Changed {
		t.Fatalf("expected removal, got %+v", removed)
	}
	if removed.Next != seed {
		t.Fatalf("round trip did not restore user bytes verbatim:\nGOT:\n%s\nWANT:\n%s", removed.Next, seed)
	}
}

func TestJSONLeafRefusesAmbiguousDocuments(t *testing.T) {
	cases := map[string]string{
		"tabs":              "{\n\t\"providers\": {}\n}\n",
		"jsonc comments":    "{\n  // comment\n  \"providers\": {}\n}\n",
		"flow root":         `{"providers": {}}` + "\n",
		"non-object root":   "[\n  1\n]\n",
		"duplicate keys":    "{\n  \"providers\": {},\n  \"providers\": {}\n}\n",
		"scalar container":  "{\n  \"providers\": 5\n}\n",
		"array container":   "{\n  \"providers\": []\n}\n",
		"scalar leaf":       "{\n  \"providers\": {\n    \"prism\": 5\n  }\n}\n",
		"duplicate leaves":  "{\n  \"providers\": {\n    \"prism\": {},\n    \"prism\": {}\n  }\n}\n",
		"missing closer":    "{\n  \"providers\": {\n    \"prism\": {}\n",
		"trailing content":  "{\n  \"providers\": {}\n}\nextra\n",
		"unclosed leaf obj": "{\n  \"providers\": {\n    \"prism\": {\n      \"baseUrl\": \"x\"\n  }\n}\n",
	}
	for name, seed := range cases {
		if patch := upsertTestLeaf(seed); patch.Kind != "refused" {
			t.Fatalf("%s: expected refusal, got %+v", name, patch)
		}
		if name != "scalar leaf" {
			if patch := removeTestLeaf(seed); patch.Kind != "refused" {
				t.Fatalf("%s: remove expected refusal, got %+v", name, patch)
			}
		}
	}
}

func TestJSONLeafRemoveRemovesScalarAtOwnedKey(t *testing.T) {
	seed := "{\n  \"providers\": {\n    \"prism\": 5\n  },\n  \"other\": true\n}\n"
	patch := removeTestLeaf(seed)
	if patch.Kind != "written" || !patch.Changed {
		t.Fatalf("expected removal, got %+v", patch)
	}
	assertValidJSON(t, patch.Next)
	if strings.Contains(patch.Next, "prism") {
		t.Fatalf("prism member survived:\n%s", patch.Next)
	}
	if !strings.Contains(patch.Next, `"other": true`) {
		t.Fatalf("user member lost:\n%s", patch.Next)
	}
}

func TestJSONLeafReadDerivesPresenceAndEndpoint(t *testing.T) {
	if read := readTestLeaf("{\n}\n"); read.Kind != jsonLeafAbsent {
		t.Fatalf("expected absent, got %+v", read)
	}
	if read := readTestLeaf("{\n  \"providers\": {}\n}\n"); read.Kind != jsonLeafAbsent {
		t.Fatalf("expected absent, got %+v", read)
	}
	read := readTestLeaf(upsertTestLeaf("").Next)
	if read.Kind != jsonLeafPresent || read.Endpoint == nil || *read.Endpoint != "http://127.0.0.1:8787/v1" {
		t.Fatalf("expected present endpoint, got %+v", read)
	}
	drift := readTestLeaf("{\n  \"providers\": {\n    \"prism\": {\n      \"baseUrl\": \"http://elsewhere/v1\"\n    }\n  }\n}\n")
	if drift.Kind != jsonLeafPresent || drift.Endpoint == nil || *drift.Endpoint != "http://elsewhere/v1" {
		t.Fatalf("expected drift endpoint, got %+v", drift)
	}
}

func jsonEnvEntries() []JSONScalarEntry {
	return []JSONScalarEntry{
		{Key: "ANTHROPIC_BASE_URL", Value: "http://127.0.0.1:8787"},
		{Key: "ANTHROPIC_AUTH_TOKEN", Value: "prism-loopback"},
		{Key: "ANTHROPIC_MODEL", Value: "claude-codex--gpt-5.2-codex"},
	}
}

func upsertTestEnvKeys(text string) JSONPatch {
	return UpsertJSONScalarKeys(text, "env", "settings.json", jsonEnvEntries())
}

func removeTestEnvKeys(text string) JSONPatch {
	keys := make([]string, 0, len(jsonEnvEntries()))
	for _, entry := range jsonEnvEntries() {
		keys = append(keys, entry.Key)
	}
	return RemoveJSONScalarKeys(text, "env", "settings.json", keys)
}

func TestJSONScalarKeysInsertIntoFreshAndExistingDocuments(t *testing.T) {
	fresh := upsertTestEnvKeys("")
	if fresh.Kind != "written" || !fresh.Changed {
		t.Fatalf("expected written change, got %+v", fresh)
	}
	assertValidJSON(t, fresh.Next)
	if !strings.Contains(fresh.Next, `"ANTHROPIC_BASE_URL": "http://127.0.0.1:8787"`) {
		t.Fatalf("env key missing:\n%s", fresh.Next)
	}

	seed := "{\n  \"permissions\": {\n    \"allow\": [\n      \"Bash\"\n    ]\n  }\n}\n"
	patch := upsertTestEnvKeys(seed)
	if patch.Kind != "written" || !patch.Changed {
		t.Fatalf("expected written change, got %+v", patch)
	}
	assertValidJSON(t, patch.Next)
	if !strings.Contains(patch.Next, `"permissions"`) {
		t.Fatalf("user member lost:\n%s", patch.Next)
	}
	if read := ReadJSONScalarKeys(patch.Next, "env", "ANTHROPIC_BASE_URL", "settings.json", envKeyNames()); read.Kind != jsonLeafPresent || read.Endpoint == nil || *read.Endpoint != "http://127.0.0.1:8787" {
		t.Fatalf("read after insert mismatch: %+v", read)
	}
}

func envKeyNames() []string {
	return []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_MODEL"}
}

func TestJSONScalarKeysPreserveSiblingEnvEntries(t *testing.T) {
	seed := "{\n  \"env\": {\n    \"CUSTOM_TOOL\": \"keep\"\n  }\n}\n"
	patch := upsertTestEnvKeys(seed)
	if patch.Kind != "written" || !patch.Changed {
		t.Fatalf("expected written change, got %+v", patch)
	}
	assertValidJSON(t, patch.Next)
	if !strings.Contains(patch.Next, `"CUSTOM_TOOL": "keep",`) {
		t.Fatalf("user env entry lost or reordered:\n%s", patch.Next)
	}
	removed := removeTestEnvKeys(patch.Next)
	if removed.Kind != "written" || !removed.Changed {
		t.Fatalf("expected removal, got %+v", removed)
	}
	if removed.Next != seed {
		t.Fatalf("round trip did not restore user bytes verbatim:\nGOT:\n%s\nWANT:\n%s", removed.Next, seed)
	}
}

func TestJSONScalarKeysRewritePrismOwnedValuesInPlace(t *testing.T) {
	seed := "{\n  \"env\": {\n    \"ANTHROPIC_BASE_URL\": \"http://127.0.0.1:8787\",\n    \"ANTHROPIC_MODEL\": \"claude-codex--stale\"\n  }\n}\n"
	patch := upsertTestEnvKeys(seed)
	if patch.Kind != "written" || !patch.Changed {
		t.Fatalf("expected healing rewrite, got %+v", patch)
	}
	assertValidJSON(t, patch.Next)
	if !strings.Contains(patch.Next, `"ANTHROPIC_MODEL": "claude-codex--gpt-5.2-codex"`) {
		t.Fatalf("stale model survived:\n%s", patch.Next)
	}
	if !strings.Contains(patch.Next, `"ANTHROPIC_AUTH_TOKEN": "prism-loopback"`) {
		t.Fatalf("missing key not inserted:\n%s", patch.Next)
	}
}

func TestJSONScalarKeysRefuseUserOwnedValues(t *testing.T) {
	seed := "{\n  \"env\": {\n    \"ANTHROPIC_BASE_URL\": \"https://my-gateway.example\"\n  }\n}\n"
	patch := upsertTestEnvKeys(seed)
	if patch.Kind != "refused" {
		t.Fatalf("expected user-owned refusal, got %+v", patch)
	}
	if !strings.Contains(patch.Reason, "ANTHROPIC_BASE_URL") {
		t.Fatalf("refusal does not name the key: %s", patch.Reason)
	}
	if patch.Next != "" {
		t.Fatalf("refusal produced bytes: %s", patch.Next)
	}
}

func TestJSONScalarKeysRefuseNonStringUserValue(t *testing.T) {
	seed := "{\n  \"env\": {\n    \"ANTHROPIC_BASE_URL\": 42\n  }\n}\n"
	if patch := upsertTestEnvKeys(seed); patch.Kind != "refused" {
		t.Fatalf("expected refusal, got %+v", patch)
	}
}

func TestJSONScalarKeysRemovePrunesEmptyEnv(t *testing.T) {
	seed := "{\n  \"env\": {\n    \"ANTHROPIC_BASE_URL\": \"http://127.0.0.1:8787\"\n  }\n}\n"
	patch := removeTestEnvKeys(seed)
	if patch.Kind != "written" || !patch.Changed {
		t.Fatalf("expected removal, got %+v", patch)
	}
	if strings.Contains(patch.Next, "env") {
		t.Fatalf("empty env container survived:\n%s", patch.Next)
	}
	if patch.Next != "{\n}\n" {
		t.Fatalf("unexpected bytes:\n%s", patch.Next)
	}
}

func TestJSONScalarKeysIdempotent(t *testing.T) {
	first := upsertTestEnvKeys("{\n}\n")
	second := upsertTestEnvKeys(first.Next)
	if second.Kind != "written" || second.Changed {
		t.Fatalf("second apply is not a no-op: %+v", second)
	}
}

func TestJSONScalarKeysRemoveStripsPrecedingComma(t *testing.T) {
	seed := "{\n  \"permissions\": {\n    \"allow\": [\n      \"Bash\"\n    ]\n  },\n  \"env\": {\n    \"ANTHROPIC_BASE_URL\": \"http://127.0.0.1:8787\",\n    \"ANTHROPIC_AUTH_TOKEN\": \"prism-loopback\"\n  }\n}\n"
	patch := removeTestEnvKeys(seed)
	if patch.Kind != "written" || !patch.Changed {
		t.Fatalf("expected removal, got %+v", patch)
	}
	if patch.Next != "{\n  \"permissions\": {\n    \"allow\": [\n      \"Bash\"\n    ]\n  }\n}\n" {
		t.Fatalf("preceding comma survived prune:\n%s", patch.Next)
	}
}

func TestJSONBlockLeafRemoveStripsPrecedingComma(t *testing.T) {
	seed := "{\n  \"theme\": \"dark\",\n  \"providers\": {\n    \"prism\": {\n      \"baseUrl\": \"http://localhost:8080/v1\"\n    }\n  }\n}\n"
	applied := UpsertJSONBlockLeaf(seed, "providers", "prism", "models.json", func(leafIndent int) string {
		pad := strings.Repeat(" ", leafIndent)
		return pad + `"prism": {` + "\n" +
			pad + `  "baseUrl": "http://127.0.0.1:8787/v1",` + "\n" +
			pad + `  "apiKey": "prism-loopback"` + "\n" +
			pad + `}`
	})
	patch := RemoveJSONBlockLeaf(applied.Next, "providers", "prism", "models.json")
	if patch.Kind != "written" || !patch.Changed {
		t.Fatalf("expected removal, got %+v", patch)
	}
	if patch.Next != seed {
		t.Fatalf("block prune left stray bytes:\nGOT:\n%s\nWANT:\n%s", patch.Next, seed)
	}
}
