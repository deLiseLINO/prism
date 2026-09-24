package integrations

type ID string

const (
	Codex    ID = "codex"
	Grok     ID = "grok"
	Omp      ID = "omp"
	Claude   ID = "claude"
	Pi       ID = "pi"
	Opencode ID = "opencode"
	Hermes   ID = "hermes"
)

var IDs = []ID{Codex, Grok, Omp, Claude, Pi, Opencode, Hermes}

func ValidID(s string) (ID, bool) {
	for _, id := range IDs {
		if ID(s) == id {
			return id, true
		}
	}
	return "", false
}

type Model struct {
	ID                     string
	Name                   string
	ContextWindow          int
	ImageInput             bool
	ReasoningEfforts       []string
	DefaultReasoningEffort string
}

// DefaultPrismModels mirrors the daemon's default combo targets
// (internal/config: codex-main/gpt-5.2-codex, gpt-5.2, and ag/gemini-3-pro),
// namespaced in the provider/model form the chat ingress resolves; the bare
// spellings are rejected there with 400.
var DefaultPrismModels = []Model{
	{ID: "codex-main/gpt-5.2-codex", Name: "GPT-5.2 Codex"},
	{ID: "codex-main/gpt-5.2", Name: "GPT-5.2"},
	{ID: "ag/gemini-3-pro", Name: "Gemini 3 Pro"},
}

// chatEffortVocabulary is the reasoning-effort set the daemon's chat
// completions ingress honors; any other value silently no-ops there, so it is
// never advertised to a chat-wire client.
var chatEffortVocabulary = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

// responsesEffortVocabulary is the set the responses ingress honors, which
// grok's api_backend = "responses" tables ride.
var responsesEffortVocabulary = []string{"off", "low", "medium", "high", "xhigh", "max"}

// ompThinkingEfforts is the effort ladder omp's thinking block accepts: the
// chat-honored rungs minus off (a thinking level, not an effort).
var ompThinkingEfforts = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

// piThinkingLevels are the picker levels pi's thinkingLevelMap keys; each
// maps to the wire effort it names, or null when the wire does not honor it.
var piThinkingLevels = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

const maxOutputTokenBudget = 32000

// maxTokensFor is the output budget written beside a known context window;
// client schemas require a value, so it clamps to the window.
func maxTokensFor(contextWindow int) int {
	if contextWindow < maxOutputTokenBudget {
		return contextWindow
	}
	return maxOutputTokenBudget
}

// effortsFor projects a model's declared reasoning ladder onto the vocabulary
// the receiving wire honors: declared rungs survive in order minus unknowns
// and duplicates, and an undeclared ladder means the daemon's full steering
// range, which every ingress accepts.
func effortsFor(model Model, vocabulary []string) []string {
	if len(model.ReasoningEfforts) == 0 {
		return append([]string(nil), vocabulary...)
	}
	out := make([]string, 0, len(model.ReasoningEfforts))
	for _, effort := range model.ReasoningEfforts {
		if !containsString(vocabulary, effort) || containsString(out, effort) {
			continue
		}
		out = append(out, effort)
	}
	return out
}

// defaultEffort resolves the rung a client config pins as its default: the
// model's configured default when it survived projection, then medium, then
// high, then the first rung.
func defaultEffort(efforts []string, configured string) string {
	if configured != "" && containsString(efforts, configured) {
		return configured
	}
	if containsString(efforts, "medium") {
		return "medium"
	}
	if containsString(efforts, "high") {
		return "high"
	}
	if len(efforts) > 0 {
		return efforts[0]
	}
	return ""
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// resolveModels picks the model list a client config carries. A configured
// live source that advertises nothing is a named refusal: hardcoded fallback
// ids are rejected by the daemon's ingress, so writing them would point the
// client at models it can never call.
func resolveModels(fallback []Model, src func() []Model, id ID) ([]Model, string) {
	if src == nil {
		return fallback, ""
	}
	if models := src(); len(models) > 0 {
		return models, ""
	}
	return nil, "prism: " + string(id) + " apply refused — live models source is empty; refusing rather than writing fallback model ids the daemon rejects"
}

type ApplyResult struct {
	OK        bool   `json:"ok"`
	ID        ID     `json:"id"`
	Reason    string `json:"reason,omitempty"`
	Retryable bool   `json:"retryable,omitempty"`
}

type Status struct {
	ID         ID      `json:"id"`
	Installed  bool    `json:"installed"`
	Managed    bool    `json:"managed"`
	Enabled    bool    `json:"enabled"`
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
