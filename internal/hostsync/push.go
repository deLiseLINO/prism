package hostsync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/deLiseLINO/prism/internal/config"
)

// State is the last known sync outcome for one host.
type State string

const (
	StatePending State = "pending"
	StateSynced  State = "synced"
	StateFailed  State = "failed"
	StateOff     State = "off"
)

// Status is what the hosts list shows for one installed host. At is the
// RFC 3339 time of the last attempt.
type Status struct {
	State State  `json:"state"`
	At    string `json:"at,omitempty"`
	Error string `json:"error,omitempty"`
}

// Transport sends one request to the prism daemon of host.
type Transport func(ctx context.Context, host config.Host, req *http.Request) (*http.Response, error)

type ConfigSource interface {
	Get() config.Snapshot
}

type Options struct {
	Config    ConfigSource
	Secrets   SecretSource
	Transport Transport
	// Interval re-pushes to every host so a host that was offline, or was
	// edited locally, comes back to the main config.
	Interval time.Duration
	// Settle waits for a burst of config writes to end before pushing.
	Settle  time.Duration
	Timeout time.Duration
	Now     func() time.Time
}

// Pusher pushes the main config to every installed host with auto sync on.
type Pusher struct {
	opts   Options
	mu     sync.Mutex
	status map[string]Status
}

func NewPusher(opts Options) *Pusher {
	return &Pusher{opts: opts, status: map[string]Status{}}
}

// Status reports host's sync state; host is its current config entry.
func (p *Pusher) Status(id string, host config.Host) Status {
	if !host.SyncEnabled() {
		return Status{State: StateOff}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if st, ok := p.status[id]; ok {
		return st
	}
	return Status{State: StatePending}
}

// Run pushes at start, after config changes, and every Interval. Host
// mutations are config changes, so an installed or re-enabled host gets its
// first push right away.
func (p *Pusher) Run(ctx context.Context, changes <-chan struct{}) {
	ticker := time.NewTicker(p.opts.Interval)
	defer ticker.Stop()
	p.PushAll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-changes:
		case <-ticker.C:
		}
		if !p.settle(ctx, changes) {
			return
		}
		p.PushAll(ctx)
	}
}

func (p *Pusher) settle(ctx context.Context, changes <-chan struct{}) bool {
	timer := time.NewTimer(p.opts.Settle)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-changes:
			timer.Reset(p.opts.Settle)
		case <-timer.C:
			return true
		}
	}
}

// PushAll sends the current config to every installed host with auto sync
// on, in parallel, and records each outcome.
func (p *Pusher) PushAll(ctx context.Context) {
	snap := p.opts.Config.Get()
	targets := map[string]config.Host{}
	for id, host := range snap.Config.Hosts {
		if host.DaemonPort > 0 && host.SyncEnabled() {
			targets[id] = host
		}
	}
	p.mu.Lock()
	for id := range p.status {
		if _, ok := targets[id]; !ok {
			delete(p.status, id)
		}
	}
	p.mu.Unlock()
	if len(targets) == 0 {
		return
	}
	body, err := p.encode(ctx, snap.Config)
	var wg sync.WaitGroup
	for _, id := range slices.Sorted(maps.Keys(targets)) {
		if err != nil {
			p.record(id, err)
			continue
		}
		wg.Go(func() { p.record(id, p.push(ctx, targets[id], body)) })
	}
	wg.Wait()
}

func (p *Pusher) encode(ctx context.Context, doc config.Document) ([]byte, error) {
	bundle, err := Portable(ctx, doc, p.opts.Secrets)
	if err != nil {
		return nil, err
	}
	return json.Marshal(bundle)
}

func (p *Pusher) push(ctx context.Context, host config.Host, body []byte) error {
	ctx, cancel := context.WithTimeout(ctx, p.opts.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, "http://prism"+ConfigPath, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.opts.Transport(ctx, host, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	switch {
	case resp.StatusCode == http.StatusOK:
		return nil
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed:
		return fmt.Errorf("prism on the machine is too old for config sync; update it")
	default:
		return fmt.Errorf("prism on the machine refused the config: %s", errorMessage(resp.StatusCode, raw))
	}
}

func errorMessage(status int, raw []byte) string {
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &envelope) == nil && envelope.Error.Message != "" {
		return envelope.Error.Message
	}
	if text := strings.TrimSpace(string(raw)); text != "" {
		return text
	}
	return http.StatusText(status)
}

func (p *Pusher) record(id string, err error) {
	st := Status{State: StateSynced, At: p.opts.Now().UTC().Format(time.RFC3339)}
	if err != nil {
		st.State, st.Error = StateFailed, err.Error()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if prev := p.status[id]; prev.State != st.State || prev.Error != st.Error {
		if err != nil {
			log.Printf("prism: sync host %s: %v", id, err)
		} else {
			log.Printf("prism: sync host %s: up to date", id)
		}
	}
	p.status[id] = st
}
