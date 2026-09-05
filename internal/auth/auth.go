package auth

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"prism/internal/account"
)

type AuthSessionID string

type AuthStart struct {
	Session AuthSessionID
	URL     string
}

type AuthCallback struct {
	Session AuthSessionID
	Code    string
	State   string
}

type AuthStatus struct {
	State string
}

const (
	StatusPending      = "pending"
	StatusComplete     = "complete"
	StatusFailed       = "failed"
	StatusAuthorized   = "authorized"
	StatusUnauthorized = "unauthorized"
)

const (
	statusExchanging = "exchanging"
	defaultTTL       = 15 * time.Minute
)

var (
	ErrUnknownProvider = errors.New("auth: unknown provider")
	ErrUnknownSession  = errors.New("auth: unknown session")
	ErrStateMismatch   = errors.New("auth: state mismatch")
	ErrSessionExpired  = errors.New("auth: session expired")
	ErrInvalidCallback = errors.New("auth: invalid callback")
	ErrAuthFailed      = errors.New("auth: authentication failed")
	ErrLoopbackBind    = errors.New("auth: loopback bind failed")
)

// Sink persists the exchanged credential and owns runtime pool registration.
type Sink interface {
	Persist(ctx context.Context, provider account.ProviderID, cred account.Credential) (account.Account, error)
	Register(a account.Account)
	Registered(provider account.ProviderID) bool
}

// Flow is the provider-specific OAuth behavior behind a Service.
type Flow interface {
	Config() ProviderConfig
	AuthURL(state, verifier, redirectURI string) string
	Exchange(ctx context.Context, code, verifier, redirectURI string) (account.Credential, error)
}

type Options struct {
	HTTP        *http.Client
	Now         func() time.Time
	Random      io.Reader
	SessionTTL  time.Duration
	OnboardPoll time.Duration
}

func (o Options) withDefaults() Options {
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Random == nil {
		o.Random = rand.Reader
	}
	if o.SessionTTL <= 0 {
		o.SessionTTL = defaultTTL
	}
	return o
}

type sessionRecord struct {
	provider    account.ProviderID
	verifier    string
	state       string
	redirectURI string
	expires     time.Time
	status      string
}

type loopback struct {
	server      *http.Server
	redirectURI string
}

type Service struct {
	sink  Sink
	flows map[account.ProviderID]Flow
	opts  Options

	mu        sync.Mutex
	pending   map[AuthSessionID]*sessionRecord
	byState   map[string]AuthSessionID
	loopbacks map[account.ProviderID]*loopback
}

func New(sink Sink, flows map[account.ProviderID]Flow, opts Options) (*Service, error) {
	if sink == nil {
		return nil, fmt.Errorf("auth: nil sink")
	}
	if len(flows) == 0 {
		return nil, fmt.Errorf("auth: no providers configured")
	}
	return &Service{
		sink:      sink,
		flows:     flows,
		opts:      opts.withDefaults(),
		pending:   map[AuthSessionID]*sessionRecord{},
		byState:   map[string]AuthSessionID{},
		loopbacks: map[account.ProviderID]*loopback{},
	}, nil
}

func (s *Service) Start(ctx context.Context, provider account.ProviderID) (AuthStart, error) {
	flow, ok := s.flows[provider]
	if !ok {
		return AuthStart{}, ErrUnknownProvider
	}
	redirectURI, err := s.listenerURI(provider, flow.Config())
	if err != nil {
		return AuthStart{}, err
	}
	verifier, err := randomToken(s.opts.Random, 32)
	if err != nil {
		return AuthStart{}, fmt.Errorf("auth: pkce verifier: %w", err)
	}
	state, err := randomToken(s.opts.Random, 16)
	if err != nil {
		return AuthStart{}, fmt.Errorf("auth: state nonce: %w", err)
	}
	rawSession, err := randomToken(s.opts.Random, 16)
	if err != nil {
		return AuthStart{}, fmt.Errorf("auth: session id: %w", err)
	}
	sid := AuthSessionID(rawSession)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(s.opts.Now())
	s.pending[sid] = &sessionRecord{
		provider:    provider,
		verifier:    verifier,
		state:       state,
		redirectURI: redirectURI,
		expires:     s.opts.Now().Add(s.opts.SessionTTL),
		status:      StatusPending,
	}
	s.byState[state] = sid
	return AuthStart{Session: sid, URL: flow.AuthURL(state, verifier, redirectURI)}, nil
}

// Complete validates and finishes a callback from the management route or the
// loopback listener; both entry points converge here.
func (s *Service) Complete(ctx context.Context, provider account.ProviderID, cb AuthCallback) error {
	s.mu.Lock()
	rec, ok := s.pending[cb.Session]
	if !ok || rec.provider != provider {
		s.mu.Unlock()
		return ErrUnknownSession
	}
	if cb.Code == "" {
		s.mu.Unlock()
		return ErrInvalidCallback
	}
	if cb.State == "" || cb.State != rec.state {
		s.mu.Unlock()
		return ErrStateMismatch
	}
	sid := cb.Session
	s.mu.Unlock()
	return s.finish(ctx, sid, rec, cb.Code, cb.State)
}

func (s *Service) Status(ctx context.Context, provider account.ProviderID, session AuthSessionID) (AuthStatus, error) {
	if _, ok := s.flows[provider]; !ok {
		return AuthStatus{}, ErrUnknownProvider
	}
	if session == "" {
		if s.sink.Registered(provider) {
			return AuthStatus{State: StatusAuthorized}, nil
		}
		return AuthStatus{State: StatusUnauthorized}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(s.opts.Now())
	rec, ok := s.pending[session]
	if !ok || rec.provider != provider {
		return AuthStatus{}, ErrUnknownSession
	}
	state := rec.status
	if state == statusExchanging {
		state = StatusPending
	}
	return AuthStatus{State: state}, nil
}

func (s *Service) Close(ctx context.Context) error {
	s.mu.Lock()
	closing := s.loopbacks
	s.loopbacks = map[account.ProviderID]*loopback{}
	s.mu.Unlock()
	var errs []error
	for _, lb := range closing {
		errs = append(errs, lb.server.Shutdown(ctx))
	}
	return errors.Join(errs...)
}

func (s *Service) finish(ctx context.Context, sid AuthSessionID, rec *sessionRecord, code, state string) error {
	s.mu.Lock()
	if rec.status != StatusPending {
		s.mu.Unlock()
		return ErrUnknownSession
	}
	if rec.state != state {
		s.mu.Unlock()
		return ErrStateMismatch
	}
	if s.opts.Now().After(rec.expires) {
		delete(s.pending, sid)
		delete(s.byState, rec.state)
		s.mu.Unlock()
		return ErrSessionExpired
	}
	flow := s.flows[rec.provider]
	verifier, redirectURI, provider := rec.verifier, rec.redirectURI, rec.provider
	rec.status = statusExchanging
	delete(s.byState, state)
	s.mu.Unlock()

	cred, err := flow.Exchange(ctx, code, verifier, redirectURI)
	if err != nil {
		s.mu.Lock()
		rec.status = StatusFailed
		s.mu.Unlock()
		return ErrAuthFailed
	}
	acct, err := s.sink.Persist(ctx, provider, cred)
	if err != nil {
		s.mu.Lock()
		rec.status = StatusFailed
		s.mu.Unlock()
		return err
	}
	s.sink.Register(acct)
	s.mu.Lock()
	rec.status = StatusComplete
	s.mu.Unlock()
	return nil
}

func (s *Service) completeByState(ctx context.Context, provider account.ProviderID, state, code string) error {
	s.mu.Lock()
	sid, ok := s.byState[state]
	if !ok {
		s.mu.Unlock()
		return ErrUnknownSession
	}
	rec, ok := s.pending[sid]
	if !ok || rec.provider != provider {
		s.mu.Unlock()
		return ErrUnknownSession
	}
	s.mu.Unlock()
	return s.finish(ctx, sid, rec, code, state)
}

func (s *Service) sweepLocked(now time.Time) {
	for sid, rec := range s.pending {
		if now.After(rec.expires) {
			delete(s.pending, sid)
			delete(s.byState, rec.state)
		}
	}
}
