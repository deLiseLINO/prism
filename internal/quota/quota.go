package quota

import "time"

type Snapshot struct {
	Used      int64
	Limit     *int64
	WindowEnd time.Time
	Source    Source
}

type Source uint8

const (
	SourceHeader Source = iota + 1
	SourceEndpoint
	SourceReport
	SourceProbe
)
