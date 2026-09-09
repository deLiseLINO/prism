package usage

import "time"

type Usage struct {
	InputTokens       int64
	OutputTokens      int64
	CachedInputTokens int64
	ReasoningTokens   int64
	TotalTokens       int64
}

type Status uint8

const (
	StatusReported Status = iota + 1
	StatusZero
)

func (s Status) String() string {
	switch s {
	case StatusReported:
		return "reported"
	case StatusZero:
		return "zero"
	default:
		return "unknown"
	}
}

type Attempt struct {
	Provider string
	Model    string
	Outcome  string
	Usage    Usage
}

type Record struct {
	RequestID string
	Timestamp time.Time
	Model     string
	Protocol  string
	Status    string
	Reason    string
	Usage     Usage
	UsageKind Status
	Attempts  []Attempt
	Duration  time.Duration
}

type Aggregate struct {
	Requests        int64
	Completed       int64
	Failed          int64
	InputTokens     int64
	OutputTokens    int64
	CachedTokens    int64
	ReasoningTokens int64
	TotalTokens     int64
	Measured        int64
}

type ModelAggregate struct {
	Model    string
	Provider string
	Aggregate
}

type ProviderAggregate struct {
	Provider string
	Aggregate
}
