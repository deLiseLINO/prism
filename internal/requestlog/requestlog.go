package requestlog

import (
	"sync"
	"time"

	"prism/internal/account"
	"prism/internal/canon"
	"prism/internal/execution"
)

const maxAttemptsPerTurn = 32
const maxErrorBytes = 512

type Status uint8

const (
	StatusOpen Status = iota + 1
	StatusCompleted
	StatusIncomplete
	StatusFailed
)

type Outcome uint8

const (
	AttemptSucceeded Outcome = iota + 1
	AttemptUnauthorized
	AttemptForbidden
	AttemptRateLimited
	AttemptQuotaExhausted
	AttemptNotFound
	AttemptTimeout
	AttemptServer
	AttemptTransport
	AttemptInvalidRequest
	AttemptContextLength
	AttemptClientClosed
	AttemptNoTerminal
	AttemptRejected
)

func (o Outcome) String() string {
	switch o {
	case AttemptSucceeded:
		return "succeeded"
	case AttemptUnauthorized:
		return "unauthorized"
	case AttemptForbidden:
		return "forbidden"
	case AttemptRateLimited:
		return "rate_limited"
	case AttemptQuotaExhausted:
		return "quota_exhausted"
	case AttemptNotFound:
		return "not_found"
	case AttemptTimeout:
		return "timeout"
	case AttemptServer:
		return "server"
	case AttemptTransport:
		return "transport"
	case AttemptInvalidRequest:
		return "invalid_request"
	case AttemptContextLength:
		return "context_length"
	case AttemptClientClosed:
		return "client_closed"
	case AttemptNoTerminal:
		return "no_terminal"
	case AttemptRejected:
		return "rejected"
	default:
		return "unknown"
	}
}

type Terminal struct {
	Status     Status
	Failed     bool
	Reason     canon.FailureReason
	Incomplete canon.IncompleteReason
	Usage      canon.Usage
}

type AttemptInfo struct {
	Provider  account.ProviderID
	AccountID account.AccountID
	Model     canon.ModelID
	StartedAt time.Time
	Outcome   Outcome
	Error     string
}

type Attempt struct {
	Provider  account.ProviderID
	AccountID account.AccountID
	Model     canon.ModelID
	StartedAt time.Time
	Duration  time.Duration
	Outcome   Outcome
	Error     string
}

type Entry struct {
	Seq             uint64
	RequestID       execution.RequestID
	Client          execution.Client
	Session         execution.SessionKey
	Model           canon.ModelID
	StartedAt       time.Time
	Duration        time.Duration
	Status          Status
	Terminal        Terminal
	Attempts        []Attempt
	AttemptsDropped int
}

type Journal struct {
	mu      sync.Mutex
	ring    []*Entry
	head    int
	count   int
	seq     uint64
	dropped uint64
	now     func() time.Time
}

func New(capacity int, now func() time.Time) *Journal {
	if capacity <= 0 {
		return &Journal{now: now}
	}
	return &Journal{ring: make([]*Entry, capacity), now: now}
}

func (j *Journal) Open(f execution.Facts, model canon.ModelID) *Turn {
	e := &Entry{
		Client:    f.Client,
		RequestID: f.RequestID,
		Session:   f.Session,
		Model:     model,
		StartedAt: j.nowTime(),
		Status:    StatusOpen,
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.ring == nil {
		return &Turn{j: j, entry: e}
	}
	j.seq++
	e.Seq = j.seq
	if j.count == len(j.ring) {
		j.ring[j.head] = e
		j.head = (j.head + 1) % len(j.ring)
		j.dropped++
		return &Turn{j: j, entry: e}
	}
	j.ring[(j.head+j.count)%len(j.ring)] = e
	j.count++
	return &Turn{j: j, entry: e}
}

func (j *Journal) Snapshot() []Entry {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]Entry, 0, j.count)
	for i := range j.count {
		e := j.ring[(j.head+i)%len(j.ring)]
		cp := *e
		if e.Attempts != nil {
			cp.Attempts = append([]Attempt(nil), e.Attempts...)
		}
		out = append(out, cp)
	}
	return out
}

func (j *Journal) Dropped() uint64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.dropped
}

func (j *Journal) nowTime() time.Time {
	if j.now != nil {
		return j.now()
	}
	return time.Now()
}

type Turn struct {
	j      *Journal
	entry  *Entry
	now    func() time.Time
	closed bool
}

func (t *Turn) Now() time.Time {
	if t.now != nil {
		return t.now()
	}
	return t.j.nowTime()
}

func (t *Turn) Attempt(a AttemptInfo) {
	t.j.mu.Lock()
	defer t.j.mu.Unlock()
	if t.closed {
		return
	}
	e := t.entry
	if len(e.Attempts) >= maxAttemptsPerTurn {
		e.AttemptsDropped++
		return
	}
	err := a.Error
	if len(err) > maxErrorBytes {
		err = err[:maxErrorBytes]
	}
	end := t.j.nowTime()
	e.Attempts = append(e.Attempts, Attempt{
		Provider:  a.Provider,
		AccountID: a.AccountID,
		Model:     a.Model,
		StartedAt: a.StartedAt,
		Duration:  end.Sub(a.StartedAt),
		Outcome:   a.Outcome,
		Error:     err,
	})
}

func (t *Turn) Close(term Terminal) {
	t.j.mu.Lock()
	defer t.j.mu.Unlock()
	if t.closed {
		return
	}
	t.closed = true
	e := t.entry
	e.Duration = t.j.nowTime().Sub(e.StartedAt)
	e.Status = term.Status
	e.Terminal = term
}
