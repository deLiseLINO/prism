package server

import (
	"compress/gzip"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"prism/internal/account"
	"prism/internal/config"
	ingresschat "prism/internal/ingress/chat"
	ingressmessages "prism/internal/ingress/messages"
	"prism/internal/ingress/responses"
	"prism/internal/provider"
	"prism/internal/routing"
	"prism/internal/usage"
	"github.com/klauspost/compress/zstd"
)

type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

type Options struct {
	Planner         routing.Planner
	Registry        *provider.Registry
	Pool            account.Pool
	Config          configProvider
	Management      http.Handler
	ManagementToken string
	Clock           Clock
	QuotaGroup      account.QuotaGroup
	OnWarning       func(responses.Warning)
	Usage           usage.Store
}

type Server struct {
	router    routing.Router
	planner   routing.Planner
	registry  *provider.Registry
	pool      account.Pool
	cfg       configProvider
	mgmt      http.Handler
	mgmtToken string
	clock     Clock
	group     account.QuotaGroup
	onWarn    func(responses.Warning)
	usage     usage.Store

	ingressResponses *responses.Ingress
	ingressChat      ingresschat.Ingress
	ingressMessages  ingressmessages.Ingress
}

const defaultQuotaGroup = account.QuotaGroup("default")

func New(opts Options) *Server {
	clock := opts.Clock
	if clock == nil {
		clock = realClock{}
	}
	group := opts.QuotaGroup
	if group == "" {
		group = defaultQuotaGroup
	}
	onWarn := opts.OnWarning
	if onWarn == nil {
		onWarn = func(w responses.Warning) {
			log.Printf("server: ingress warning kind=%s detail=%s", w.Kind, w.Detail)
		}
	}
	s := &Server{
		planner:   opts.Planner,
		registry:  opts.Registry,
		pool:      opts.Pool,
		cfg:       opts.Config,
		mgmt:      opts.Management,
		mgmtToken: opts.ManagementToken,
		clock:     clock,
		group:     group,
		onWarn:    onWarn,
		usage:     opts.Usage,
	}
	s.router = routing.NewRouter(opts.Pool, opts.Registry, opts.Planner, group)
	s.ingressResponses = responses.New(func(warn responses.Warning) { s.onWarn(warn) })
	return s
}

var routeMethods = map[string]string{
	"/v1/responses":             http.MethodPost,
	"/v1/chat/completions":      http.MethodPost,
	"/v1/messages":              http.MethodPost,
	"/v1/messages/count_tokens": http.MethodPost,
	"/v1/responses/compact":     http.MethodPost,
	"/v1/models":                http.MethodGet,
}

const maxRequestBodyBytes = 64 << 20

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/responses", s.handleResponses)
	mux.HandleFunc("POST /v1/chat/completions", s.handleChat)
	mux.HandleFunc("POST /v1/messages", s.handleMessages)
	mux.HandleFunc("POST /v1/messages/count_tokens", s.handleCountTokens)
	mux.HandleFunc("POST /v1/responses/compact", s.handleCompact)
	mux.HandleFunc("GET /v1/models", s.handleModels)
	for path, method := range routeMethods {
		mux.HandleFunc(path, methodNotAllowed(method))
	}
	if s.mgmt != nil {
		mux.Handle("/api/v1/", s.mgmt)
	}
	mux.HandleFunc("/", notFound)
	return s.admit(mux)
}

func (s *Server) admit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !loopback(r.RemoteAddr) {
			token := bearer(r)
			if token == "" {
				writeJSON(w, http.StatusUnauthorized, errorEnvelope{
					Error: errorObject{Code: "unauthorized", Message: "missing bearer token"},
				})
				return
			}
			if strings.HasPrefix(r.URL.Path, "/api/v1/") && s.mgmtToken != "" && token != s.mgmtToken {
				writeJSON(w, http.StatusForbidden, errorEnvelope{
					Error: errorObject{Code: "forbidden", Message: "invalid bearer token"},
				})
				return
			}
		}
		if r.Body != nil {
			decoded, err := decodeBody(w, r)
			if err != nil {
				return
			}
			r.Body = decoded
		}
		next.ServeHTTP(w, r)
	})
}

// decodeBody unwraps Content-Encoding (codex CLI ships zstd-compressed
// request bodies) and applies the size cap to the decoded stream, so a
// compressed bomb cannot bypass the limit.
func decodeBody(w http.ResponseWriter, r *http.Request) (io.ReadCloser, error) {
	capped := http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding"))) {
	case "", "identity":
		return capped, nil
	case "zstd":
		decoder, err := zstd.NewReader(capped)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorEnvelope{
				Error: errorObject{Code: "invalid_json", Message: "zstd body: " + err.Error()},
			})
			return nil, err
		}
		return &decodedBody{Reader: http.MaxBytesReader(w, decoder.IOReadCloser(), maxRequestBodyBytes), closer: decoder.Close}, nil
	case "gzip":
		reader, err := gzip.NewReader(capped)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorEnvelope{
				Error: errorObject{Code: "invalid_json", Message: "gzip body: " + err.Error()},
			})
			return nil, err
		}
		return &decodedBody{Reader: http.MaxBytesReader(w, reader, maxRequestBodyBytes), closer: func() { _ = reader.Close() }}, nil
	default:
		writeJSON(w, http.StatusUnsupportedMediaType, errorEnvelope{
			Error: errorObject{Code: "unsupported_media_type", Message: "unsupported Content-Encoding: " + r.Header.Get("Content-Encoding")},
		})
		return nil, fmt.Errorf("unsupported content encoding")
	}
}

type decodedBody struct {
	io.Reader
	closer func()
}

func (b *decodedBody) Close() error {
	if b.closer != nil {
		b.closer()
	}
	return nil
}

func loopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func bearer(r *http.Request) string {
	const prefix = "Bearer "
	v := r.Header.Get("Authorization")
	if len(v) > len(prefix) && v[:len(prefix)] == prefix {
		return v[len(prefix):]
	}
	return ""
}

func methodNotAllowed(allow string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", allow)
		if strings.EqualFold(strings.TrimSpace(r.Header.Get("Upgrade")), "websocket") {
			// 426 rather than 405: codex-rs maps a connect-time
			// UPGRADE_REQUIRED to a clean session-scoped HTTP fallback
			// instead of treating the WebSocket transport as broken.
			writeJSON(w, http.StatusUpgradeRequired, errorEnvelope{
				Error: errorObject{Code: "upgrade_required", Message: "websocket upgrade is not supported; use HTTP"},
			})
			return
		}
		writeJSON(w, http.StatusMethodNotAllowed, errorEnvelope{
			Error: errorObject{Code: "method_not_allowed", Message: "method " + r.Method + " not allowed"},
		})
	}
}

func notFound(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusNotFound, errorEnvelope{
		Error: errorObject{Code: "not_found", Message: "unknown route"},
	})
}

func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("server: request id entropy: %v", err))
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

type errorObject struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Param   string `json:"param,omitempty"`
}

type errorEnvelope struct {
	Error errorObject `json:"error"`
}

type chatErrorBody struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
	Param   string `json:"param,omitempty"`
}

type chatErrorEnvelope struct {
	Error chatErrorBody `json:"error"`
}

type messagesErrorBody struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type messagesErrorEnvelope struct {
	Type  string            `json:"type"`
	Error messagesErrorBody `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("server: encode response: %v", err)
	}
}

type configProvider interface {
	Get() config.Snapshot
}
