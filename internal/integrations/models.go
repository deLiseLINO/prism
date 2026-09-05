package integrations

type ID string

const (
	Codex     ID = "codex"
	Grok      ID = "grok"
	Omp       ID = "omp"
	Claude    ID = "claude"
	Pi        ID = "pi"
	Opencode  ID = "opencode"
	Opencode2 ID = "opencode2"
	Hermes    ID = "hermes"
)

var IDs = []ID{Codex, Grok, Omp, Claude, Pi, Opencode, Opencode2, Hermes}

func ValidID(s string) (ID, bool) {
	for _, id := range IDs {
		if ID(s) == id {
			return id, true
		}
	}
	return "", false
}

type Model struct {
	ID            string
	Name          string
	ContextWindow int
}

// DefaultPrismModels mirrors the daemon's default combo targets
// (internal/config: codex-main/gpt-5.2-codex, gpt-5.2, and ag/gemini-3-pro).
var DefaultPrismModels = []Model{
	{ID: "gpt-5.2-codex", Name: "GPT-5.2 Codex"},
	{ID: "gpt-5.2", Name: "GPT-5.2"},
	{ID: "gemini-3-pro", Name: "Gemini 3 Pro"},
}

type ApplyResult struct {
	OK     bool   `json:"ok"`
	ID     ID     `json:"id"`
	Reason string `json:"reason,omitempty"`
}

type Status struct {
	ID         ID      `json:"id"`
	Installed  bool    `json:"installed"`
	Managed    bool    `json:"managed"`
	TargetPath *string `json:"targetPath"`
	Endpoint   *string `json:"endpoint"`
	Drift      bool    `json:"drift"`
	Detail     string  `json:"detail"`
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
