package server

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"prism/internal/canon"
	"prism/internal/egress"
	egresschat "prism/internal/egress/chat"
	egressmessages "prism/internal/egress/messages"
	egressresponses "prism/internal/egress/responses"
	"prism/internal/execution"
	ingresschat "prism/internal/ingress/chat"
	ingressmessages "prism/internal/ingress/messages"
	ingressresponses "prism/internal/ingress/responses"
	"prism/internal/routing"
	"prism/internal/stream"
)

type protocol uint8

const (
	protocolResponses protocol = iota + 1
	protocolChat
	protocolMessages
)

const stallPollInterval = 5 * time.Second

var errTerminalWritten = errors.New("server: terminal already emitted")

type streamSink interface {
	Begin() error
	Frame(canon.Event) error
	Flush() error
	Lifecycle() routing.ResponseLifecycle
	Close()
}

type responsesSink struct {
	e      *egressresponses.Egress
	header egress.ResponseHeader
}

func (s *responsesSink) Begin() error                         { return s.e.Begin(s.header) }
func (s *responsesSink) Frame(ev canon.Event) error           { return s.e.Frame(ev) }
func (s *responsesSink) Flush() error                         { return s.e.Flush() }
func (s *responsesSink) Lifecycle() routing.ResponseLifecycle { return s.e.Lifecycle() }
func (s *responsesSink) Close()                               { s.e.Close() }

type chatSink struct {
	c      *egresschat.Chat
	header egresschat.ResponseHeader
}

func (s *chatSink) Begin() error                         { return s.c.Begin(s.header) }
func (s *chatSink) Frame(ev canon.Event) error           { return s.c.Frame(ev) }
func (s *chatSink) Flush() error                         { return s.c.Flush() }
func (s *chatSink) Lifecycle() routing.ResponseLifecycle { return s.c.Lifecycle() }
func (s *chatSink) Close()                               {}

type messagesSink struct {
	e      egressmessages.Egress
	header egressmessages.ResponseHeader
}

func (s *messagesSink) Begin() error                         { return s.e.Begin(s.header) }
func (s *messagesSink) Frame(ev canon.Event) error           { return s.e.Frame(ev) }
func (s *messagesSink) Flush() error                         { return s.e.Flush() }
func (s *messagesSink) Lifecycle() routing.ResponseLifecycle { return s.e.Lifecycle() }
func (s *messagesSink) Close()                               {}

type pipeline struct {
	sink            streamSink
	tracker         *stream.Tracker
	clock           Clock
	mu              sync.Mutex
	flusher         http.Flusher
	terminalWritten bool
}

func (s *Server) handleResponses(w http.ResponseWriter, r *http.Request) {
	s.turn(w, r, protocolResponses)
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	s.turn(w, r, protocolChat)
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	s.turn(w, r, protocolMessages)
}

func (s *Server) parse(proto protocol, r *http.Request) (canon.Request, execution.Facts, error) {
	switch proto {
	case protocolResponses:
		return s.ingressResponses.Parse(r.Context(), r)
	case protocolChat:
		return s.ingressChat.Parse(r.Context(), r)
	default:
		return s.ingressMessages.Parse(r.Context(), r)
	}
}

func (s *Server) turn(w http.ResponseWriter, r *http.Request, proto protocol) {
	req, facts, err := s.parse(proto, r)
	if err != nil {
		s.writeParseError(w, proto, err)
		return
	}
	sink := s.newSink(w, proto, req, facts)
	p := &pipeline{sink: sink, tracker: stream.NewTrackerWithClock(s.clock), clock: s.clock, flusher: w.(http.Flusher)}
	if err := sink.Begin(); err != nil {
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	go p.watchStall(ctx, cancel, done)
	res := s.router.Turn(ctx, req, facts, sink.Lifecycle(), p)
	p.finish(res)
	sink.Close()
}

func (s *Server) newSink(w http.ResponseWriter, proto protocol, req canon.Request, facts execution.Facts) streamSink {
	id := newRequestID()
	now := s.clock.Now()
	switch proto {
	case protocolResponses:
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		e := egressresponses.NewWithClock(w, facts, s.clock)
		return &responsesSink{e: e, header: egress.ResponseHeader{ID: id, Model: req.Model, CreatedAt: now}}
	case protocolChat:
		if req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
		} else {
			w.Header().Set("Content-Type", "application/json")
		}
		c := egresschat.New(w, req.Stream)
		return &chatSink{c: c, header: egresschat.ResponseHeader{ID: id, Model: req.Model, CreatedAt: now}}
	default:
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		m := egressmessages.New(w)
		return &messagesSink{e: m, header: egressmessages.ResponseHeader{ID: id, Model: req.Model, CreatedAt: now}}
	}
}

func (p *pipeline) flush() {
	p.flusher.Flush()
}
func (p *pipeline) Emit(ev canon.Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.terminalWritten {
		return errTerminalWritten
	}
	if err := p.tracker.Apply(ev); err != nil {
		return err
	}
	if err := p.sink.Frame(ev); err != nil {
		return err
	}
	p.flush()
	return nil
}
func (p *pipeline) finish(res routing.TurnResult) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.terminalWritten {
		return
	}
	p.terminalWritten = true
	switch t := res.Terminal.(type) {
	case routing.Failed:
		_ = p.sink.Frame(t.Event)
	case routing.Finished:
		_ = p.sink.Frame(t.Event)
	}
	_ = p.sink.Flush()
	p.flush()
}

func (p *pipeline) watchStall(ctx context.Context, cancel context.CancelFunc, done <-chan struct{}) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-p.clock.After(stallPollInterval):
		}
		p.mu.Lock()
		written := p.terminalWritten
		p.mu.Unlock()
		if written {
			return
		}
		ev, ok := p.tracker.OnStall()
		if !ok {
			continue
		}
		p.mu.Lock()
		if !p.terminalWritten {
			p.terminalWritten = true
			_ = p.sink.Frame(ev)
			_ = p.sink.Flush()
			p.flush()
		}
		p.mu.Unlock()
		cancel()
		return
	}
}

func (s *Server) writeParseError(w http.ResponseWriter, proto protocol, err error) {
	switch proto {
	case protocolResponses:
		var pe *ingressresponses.ParseError
		if errors.As(err, &pe) {
			status := pe.Status
			if status == 0 {
				status = http.StatusBadRequest
			}
			writeJSON(w, status, errorEnvelope{Error: errorObject{
				Code:    pe.Reason.String(),
				Message: pe.Error(),
				Param:   pe.Field,
			}})
			return
		}
		writeJSON(w, http.StatusBadRequest, errorEnvelope{Error: errorObject{
			Code:    "invalid_request",
			Message: err.Error(),
		}})
	case protocolChat:
		var pe *ingresschat.ParseError
		if errors.As(err, &pe) {
			writeJSON(w, http.StatusBadRequest, chatErrorEnvelope{Error: chatErrorBody{
				Message: pe.Error(),
				Type:    "invalid_request_error",
				Code:    pe.Reason.String(),
				Param:   pe.Field,
			}})
			return
		}
		writeJSON(w, http.StatusBadRequest, chatErrorEnvelope{Error: chatErrorBody{
			Message: err.Error(),
			Type:    "invalid_request_error",
			Code:    "invalid_request",
		}})
	default:
		var pe *ingressmessages.ParseError
		if errors.As(err, &pe) {
			writeJSON(w, http.StatusBadRequest, messagesErrorEnvelope{Type: "error", Error: messagesErrorBody{
				Type:    "invalid_request_error",
				Message: pe.Error(),
			}})
			return
		}
		writeJSON(w, http.StatusBadRequest, messagesErrorEnvelope{Type: "error", Error: messagesErrorBody{
			Type:    "invalid_request_error",
			Message: err.Error(),
		}})
	}
}
