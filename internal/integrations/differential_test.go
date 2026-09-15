package integrations

import (
	"strings"
	"testing"
)

// The staged expectations are the byte-identical outputs of the landed TS
// implementation on the same seeds (the landed TS implementation), plus
// the codex root-routing pair; any drift here means the Go port changed
// user-visible bytes.
func TestDifferentialByteIdenticalOutputs(t *testing.T) {
	codexSeed := "# user configuration\n" +
		"top_setting = \"keep\"\n" +
		"\n" +
		"[profile.default]\n" +
		"model = \"gpt-5.2\"\n" +
		"\n"

	t.Run("codex apply and rollback byte-identical", func(t *testing.T) {
		dir := tempDir(t)
		configPath := tempFile(t, dir, "config.toml", codexSeed)
		reg := NewRegistry()
		if err := reg.Register(NewCodex(CodexOptions{Port: testPort, ConfigPath: configPath})); err != nil {
			t.Fatal(err)
		}
		expectedManaged := strings.Join([]string{
			"# user configuration",
			`top_setting = "keep"`,
			"",
			prismRoutingMarker,
			`openai_base_url = "http://127.0.0.1:8787/v1"`,
			"[profile.default]",
			`model = "gpt-5.2"`,
			"",
			"",
			CodexFence.Begin,
			"[model_providers.prism]",
			`name = "prism"`,
			`base_url = "http://127.0.0.1:8787/v1"`,
			`wire_api = "responses"`,
			routingJournalHeader,
			"# written: http://127.0.0.1:8787/v1",
			CodexFence.End,
			"",
		}, "\n")
		if res := reg.Apply(Codex); !res.OK {
			t.Fatalf("apply failed: %+v", res)
		}
		if got := readFile(t, configPath); got != expectedManaged {
			t.Fatalf("apply mismatch:\nGOT:\n%s\nWANT:\n%s", got, expectedManaged)
		}
		if res := reg.Rollback(Codex); !res.OK {
			t.Fatalf("rollback failed: %+v", res)
		}
		if got := readFile(t, configPath); got != codexSeed {
			t.Fatalf("rollback mismatch:\nGOT:\n%s\nWANT:\n%s", got, codexSeed)
		}
	})

	grokSeed := "[cli]\n" +
		"installer = \"internal\"\n" +
		"\n" +
		"[models]\n" +
		"default = \"grok-4\"\n" +
		"\n" +
		"[user.custom]\n" +
		"note = \"keep me\"\n"

	t.Run("grok apply and rollback byte-identical", func(t *testing.T) {
		dir := tempDir(t)
		configPath := tempFile(t, dir, "config.toml", grokSeed)
		reg := NewRegistry()
		models := DefaultPrismModels
		if err := reg.Register(NewGrok(GrokOptions{Port: testPort, Models: models, ConfigPath: configPath})); err != nil {
			t.Fatal(err)
		}
		body := GrokManagedBlock(testPort, models)
		expectedManaged := grokSeed + "\n" + GrokFence.Begin + "\n" + body + "\n" + GrokFence.End + "\n"
		if res := reg.Apply(Grok); !res.OK {
			t.Fatalf("apply failed: %+v", res)
		}
		if got := readFile(t, configPath); got != expectedManaged {
			t.Fatalf("apply mismatch:\nGOT:\n%s\nWANT:\n%s", got, expectedManaged)
		}
		if res := reg.Rollback(Grok); !res.OK {
			t.Fatalf("rollback failed: %+v", res)
		}
		if got := readFile(t, configPath); got != grokSeed {
			t.Fatalf("rollback mismatch:\nGOT:\n%s\nWANT:\n%s", got, grokSeed)
		}
	})

	ompSeed := "theme: dark\n" +
		"providers:\n" +
		"  openai:\n" +
		"    baseUrl: https://api.openai.com/v1\n" +
		"    api: openai-completions\n" +
		"    apiKey: sk-user\n" +
		"    models: []\n" +
		"\n"

	t.Run("omp apply and rollback byte-identical", func(t *testing.T) {
		dir := tempDir(t)
		modelsPath := tempFile(t, dir, "models.yml", ompSeed)
		reg := NewRegistry()
		models := DefaultPrismModels
		if err := reg.Register(NewOmp(OmpOptions{Port: testPort, Models: models, ModelsPath: modelsPath})); err != nil {
			t.Fatal(err)
		}
		lines := []string{
			"theme: dark",
			"providers:",
			"  openai:",
			"    baseUrl: https://api.openai.com/v1",
			"    api: openai-completions",
			"    apiKey: sk-user",
			"    models: []",
			"  prism:",
			"    baseUrl: http://127.0.0.1:8787/v1",
			"    api: openai-completions",
			"    apiKey: prism-loopback",
			"    models:",
		}
		for _, m := range models {
			lines = append(lines,
				"      - id: "+m.ID,
				"        name: "+m.Name,
				"        input:",
				"          - text",
				"        reasoning: true",
				"        thinking:",
				"          mode: effort",
				"          efforts:",
				"            - minimal",
				"            - low",
				"            - medium",
				"            - high",
				"            - xhigh",
				"          defaultLevel: medium",
			)
		}
		lines = append(lines, "", "")
		expectedManaged := strings.Join(lines, "\n")
		if res := reg.Apply(Omp); !res.OK {
			t.Fatalf("apply failed: %+v", res)
		}
		if got := readFile(t, modelsPath); got != expectedManaged {
			t.Fatalf("apply mismatch:\nGOT:\n%s\nWANT:\n%s", got, expectedManaged)
		}
		if res := reg.Rollback(Omp); !res.OK {
			t.Fatalf("rollback failed: %+v", res)
		}
		if got := readFile(t, modelsPath); got != ompSeed {
			t.Fatalf("rollback mismatch:\nGOT:\n%s\nWANT:\n%s", got, ompSeed)
		}
	})
}
