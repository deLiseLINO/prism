package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"prism/internal/account"
)

func randomToken(r io.Reader, n int) (string, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func s256Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// listenerURI binds the provider loopback listener once and returns the
// redirect URI derived from the bound port.
func (s *Service) listenerURI(provider account.ProviderID, cfg ProviderConfig) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if lb, ok := s.loopbacks[provider]; ok {
		return lb.redirectURI, nil
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(cfg.BindHost, strconv.Itoa(cfg.CallbackPort)))
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrLoopbackBind, err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	mux := http.NewServeMux()
	mux.HandleFunc(cfg.CallbackPath, s.loopbackHandler(provider))
	lb := &loopback{
		server:      &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second},
		redirectURI: fmt.Sprintf("http://%s:%d%s", cfg.CallbackHost, port, cfg.CallbackPath),
	}
	go func() { _ = lb.server.Serve(ln) }()
	s.loopbacks[provider] = lb
	return lb.redirectURI, nil
}

func (s *Service) loopbackHandler(provider account.ProviderID) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		state := r.URL.Query().Get("state")
		err := s.completeByState(r.Context(), provider, state, code)
		if err != nil {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, "Login failed. Return to the app and try again.")
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, "Login complete. You can close this window.")
	}
}
