package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const remoteAddr = "10.0.0.7:41000"

func remoteRequest(method, path, body string) *http.Request {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	r.RemoteAddr = remoteAddr
	return r
}

func TestAdmitRemoteRequiresBearerAllFamilies(t *testing.T) {
	h := newTestServer(t, nil, nil, nil)
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/v1/responses"},
		{http.MethodPost, "/v1/chat/completions"},
		{http.MethodPost, "/v1/messages"},
		{http.MethodPost, "/v1/messages/count_tokens"},
		{http.MethodPost, "/v1/responses/compact"},
		{http.MethodGet, "/v1/models"},
		{http.MethodGet, "/api/v1/health"},
		{http.MethodGet, "/api/v1/providers"},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, remoteRequest(tc.method, tc.path, ""))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s remote without bearer: status = %d, want 401", tc.method, tc.path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `"unauthorized"`) {
			t.Fatalf("%s %s: body = %s", tc.method, tc.path, rec.Body.String())
		}
	}
}

func TestAdmitRemoteArbitraryBearerRejectedOnManagement(t *testing.T) {
	h := newTestServer(t, nil, nil, nil)
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/health", ""},
		{http.MethodGet, "/api/v1/providers", ""},
		{http.MethodPost, "/api/v1/providers", `{"id":"evil","wire":"codex"}`},
		{http.MethodDelete, "/api/v1/providers/p1?expectedGeneration=1", ""},
		{http.MethodDelete, "/api/v1/accounts/a1", ""},
	} {
		req := remoteRequest(tc.method, tc.path, tc.body)
		req.Header.Set("Authorization", "Bearer attacker-chosen-garbage")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s remote with arbitrary bearer: status = %d, want 403", tc.method, tc.path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `"forbidden"`) {
			t.Fatalf("%s %s: body = %s", tc.method, tc.path, rec.Body.String())
		}
	}
}

func TestAdmitRemoteManagementBearerAcceptedWhenValid(t *testing.T) {
	h := newTestServer(t, nil, nil, nil)
	req := remoteRequest(http.MethodGet, "/api/v1/health", "")
	req.Header.Set("Authorization", "Bearer "+h.mgmtToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("remote management with valid bearer: status = %d, want 200", rec.Code)
	}
}

func TestAdmitLoopbackManagementAndInferencePreserved(t *testing.T) {
	h := newTestServer(t, nil, nil, nil)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/health"},
		{http.MethodGet, "/api/v1/providers"},
		{http.MethodGet, "/v1/models"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.RemoteAddr = "127.0.0.1:1234"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s loopback: status = %d, want 200", tc.method, tc.path, rec.Code)
		}
	}
}

func TestAdmitRemoteInferenceStillAcceptsAnyBearer(t *testing.T) {
	h := newTestServer(t, nil, nil, nil)
	req := remoteRequest(http.MethodGet, "/v1/models", "")
	req.Header.Set("Authorization", "Bearer anything")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden {
		t.Fatalf("remote inference with bearer: status = %d", rec.Code)
	}
}

func oversizedBody(t *testing.T, n int64) *bytes.Reader {
	t.Helper()
	b := bytes.Repeat([]byte("a"), int(n))
	return bytes.NewReader(b)
}

func TestBodyLimitExactBoundaryAllowed(t *testing.T) {
	h := newTestServer(t, nil, nil, nil)
	b := oversizedBody(t, maxRequestBodyBytes)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", b)
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusRequestEntityTooLarge {
		t.Fatalf("exactly 64 MiB body rejected with 413")
	}
}

func TestBodyLimitOversizedRejectedEveryIngressFamily(t *testing.T) {
	h := newTestServer(t, nil, nil, nil)
	for _, tc := range []struct {
		name string
		path string
	}{
		{"responses", "/v1/responses"},
		{"chat", "/v1/chat/completions"},
		{"messages", "/v1/messages"},
		{"count_tokens", "/v1/messages/count_tokens"},
		{"compact", "/v1/responses/compact"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := oversizedBody(t, maxRequestBodyBytes+1)
			req := httptest.NewRequest(http.MethodPost, tc.path, b)
			req.RemoteAddr = "127.0.0.1:1234"
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("%s: status = %d, want 413", tc.path, rec.Code)
			}
			var env struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
				t.Fatalf("%s: decode body %q: %v", tc.path, rec.Body.String(), err)
			}
			if env.Error.Code != "request_too_large" {
				t.Fatalf("%s: error.code = %q, want request_too_large", tc.path, env.Error.Code)
			}
		})
	}
}

func TestBodyLimitOversizedRejectedOnManagement(t *testing.T) {
	h := newTestServer(t, nil, nil, nil)
	var b bytes.Buffer
	b.Write(bytes.Repeat([]byte(" "), maxRequestBodyBytes+1))
	b.WriteString(`{"id":"x","wire":"codex"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/providers", &b)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"request_too_large"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}
