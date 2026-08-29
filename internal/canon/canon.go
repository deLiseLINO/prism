package canon

type ModelID string
type ItemID string
type CallID string
type ToolName string

type OpaqueRef struct {
	Store string
	Key   string
}

func (r OpaqueRef) IsEmpty() bool { return r.Store == "" && r.Key == "" }

type Request struct {
	Model           ModelID
	Stream          bool
	Instructions    []Content
	Input           []Item
	Tools           []Tool
	ToolChoice      ToolChoice
	Reasoning       ReasoningConfig
	MaxOutputTokens int
	Sampling        Sampling
	Text            TextOutput
}

type ReasoningConfig struct {
	Effort  ReasoningEffort
	Summary ReasoningSummary
}

type Sampling struct {
	Temperature       *float64
	TopP              *float64
	Stop              []string
	ParallelToolCalls *bool
	ServiceTier       ServiceTier
}

type TextOutput struct{ Verbosity TextVerbosity }

type Content interface{ content() }

type TextContent struct{ Text string }

type ImageContent struct {
	MIMEType string
	Data     []byte
	Detail   string
}

func (TextContent) content()  {}
func (ImageContent) content() {}

type Item interface{ item() }

type Message struct {
	ID      ItemID
	Role    Role
	Content []Content
}

type ReasoningItem struct {
	ID      ItemID
	Content string
	Summary []TextContent
	State   OpaqueRef
}

type FunctionCall struct {
	ID        ItemID
	CallID    CallID
	Name      ToolName
	Arguments []byte
	State     OpaqueRef
}

type FunctionOutput struct {
	ID     ItemID
	CallID CallID
	Output []Content
}

type CustomToolCall struct {
	ID     ItemID
	CallID CallID
	Name   ToolName
	Input  string
	State  OpaqueRef
}

type CustomToolOutput struct {
	ID     ItemID
	CallID CallID
	Output string
}

type LocalShellCall struct {
	ID      ItemID
	CallID  CallID
	Command string
}

type LocalShellOutput struct {
	ID       ItemID
	CallID   CallID
	ExitCode int
	Output   string
}

type ToolSearchCall struct {
	ID     ItemID
	CallID CallID
	Query  string
}

type ToolSearchOutput struct {
	ID      ItemID
	CallID  CallID
	Results []ToolSearchResult
}

type ToolSearchResult struct {
	ToolName ToolName
	Summary  string
}

type CompactionMarker struct {
	ID   ItemID
	Kind CompactionKind
}

func (Message) item()          {}
func (ReasoningItem) item()    {}
func (FunctionCall) item()     {}
func (FunctionOutput) item()   {}
func (CustomToolCall) item()   {}
func (CustomToolOutput) item() {}
func (LocalShellCall) item()   {}
func (LocalShellOutput) item() {}
func (ToolSearchCall) item()   {}
func (ToolSearchOutput) item() {}
func (CompactionMarker) item() {}

type Tool interface{ tool() }

type FunctionTool struct {
	Name        ToolName
	Description string
	Parameters  []byte
	Strict      bool
}

type CustomToolDef struct {
	Name        ToolName
	Description string
	Format      CustomToolFormat
}

type LocalShellToolDef struct{}

type ToolSearchToolDef struct{ Limit int }

func (FunctionTool) tool()     {}
func (CustomToolDef) tool()    {}
func (LocalShellToolDef) tool() {}
func (ToolSearchToolDef) tool() {}

type ToolChoice interface{ toolChoice() }

type ToolAuto struct{}
type ToolNone struct{}
type ToolRequired struct{}
type ToolNamed struct{ Name ToolName }

func (ToolAuto) toolChoice()     {}
func (ToolNone) toolChoice()     {}
func (ToolRequired) toolChoice() {}
func (ToolNamed) toolChoice()    {}

type Role uint8

const (
	RoleUser Role = iota + 1
	RoleAssistant
	RoleSystem
)

type ReasoningEffort uint8

const (
	EffortMinimal ReasoningEffort = iota + 1
	EffortLow
	EffortMedium
	EffortHigh
	EffortXHigh
)

type ReasoningSummary uint8

const (
	SummaryNone ReasoningSummary = iota + 1
	SummaryAuto
	SummaryConcise
	SummaryDetailed
)

type ServiceTier uint8

const (
	TierDefault ServiceTier = iota + 1
	TierFlex
	TierPriority
)

type TextVerbosity uint8

const (
	VerbosityDefault TextVerbosity = iota + 1
	VerbosityLow
	VerbosityMedium
	VerbosityHigh
)

type CustomToolFormat uint8

const (
	FormatText CustomToolFormat = iota + 1
	FormatJSON
)

type CompactionKind uint8

const (
	CompactionAuto CompactionKind = iota + 1
	CompactionExplicit
)
