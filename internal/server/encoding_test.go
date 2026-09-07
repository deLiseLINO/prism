package server

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"prism/internal/canon"
	"prism/internal/provider"
	"prism/internal/routing"
	"github.com/klauspost/compress/zstd"
)

func encodeRequestBody(t *testing.T, encoding string, body string) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	switch encoding {
	case "zstd":
		encoder, err := zstd.NewWriter(&buf)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := encoder.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
		if err := encoder.Close(); err != nil {
			t.Fatal(err)
		}
	case "gzip":
		writer := gzip.NewWriter(&buf)
		if _, err := writer.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return &buf
}

func postEncoded(t *testing.T, h http.Handler, path, encoding, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, encodeRequestBody(t, encoding, body))
	req.Header.Set("Content-Type", "application/json")
	if encoding != "" {
		req.Header.Set("Content-Encoding", encoding)
	}
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func compressedTurnScripts() []fakeScript {
	events := []canon.Event{
		canon.ItemStarted{Item: messageAssistant("m1", "Hello")},
		canon.TextDelta{ItemID: "m1", Text: "Hello"},
		canon.ItemFinished{Item: messageAssistant("m1", "Hello")},
		canon.TurnFinished{Status: canon.Completed()},
	}
	return []fakeScript{{events: events}, {events: events}, {events: events}}
}

func TestCompressedRequestBodiesParse(t *testing.T) {
	runner := &fakeRunner{scripts: compressedTurnScripts()}
	plans := map[canon.ModelID]routing.Plan{"test-model": singlePlan("p1"), "p1/m1": singlePlan("p1")}
	h := newTestServer(t, nil, plans, func(reg *provider.Registry) {
		if err := reg.Register("p1", runner); err != nil {
			t.Fatal(err)
		}
	})
	body := `{"model":"test-model","input":"hi"}`
	for _, encoding := range []string{"zstd", "gzip"} {
		rec := postEncoded(t, h, "/v1/responses", encoding, body)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d body %s", encoding, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "event: response.completed") {
			t.Fatalf("%s: body missing completed event:\n%s", encoding, rec.Body.String())
		}
	}
	rec := postEncoded(t, h, "/v1/chat/completions", "zstd", `{"model":"p1/m1","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("chat zstd: status = %d body %s", rec.Code, rec.Body.String())
	}
}

func TestUnknownContentEncodingRefused(t *testing.T) {
	h := newTestServer(t, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test-model","input":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "br")
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "unsupported Content-Encoding") {
		t.Fatalf("body missing refusal reason:\n%s", rec.Body.String())
	}
}
