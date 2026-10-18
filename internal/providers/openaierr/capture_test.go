package openaierr

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestCaptureRetainsHTTPBodyAndStatus(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		error   string
		details string
	}{
		{name: "empty"},
		{name: "error fields", body: `{"error":{"message":"denied","extra":{"id":7}}}`, error: `{"message":"denied","extra":{"id":7}}`},
		{name: "status details", body: `{"status_details":{"message":"denied","extra":7}}`, details: `{"message":"denied","extra":7}`},
		{name: "other object", body: `{"detail":"denied","extra":7}`, error: `{"detail":"denied","extra":7}`},
		{name: "array", body: `["denied",7]`, error: `["denied",7]`},
		{name: "number", body: `7`, error: `7`},
		{name: "boolean", body: `false`, error: `false`},
		{name: "null", body: `null`, error: `null`},
		{name: "JSON string", body: `"denied"`, error: `"denied"`},
		{name: "plain text", body: "denied\nby edge", error: `"denied\nby edge"`},
		{name: "malformed JSON", body: `{"detail":`, error: `"{\"detail\":"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := strings.NewReader(tc.body)
			resp := &http.Response{StatusCode: 418, Body: io.NopCloser(reader)}
			got, err := Capture(resp)
			if err != nil {
				t.Fatal(err)
			}
			if got.Reported.Status != 418 || string(got.Body) != tc.body || reader.Len() != 0 {
				t.Fatalf("capture lost body or status: %+v", got)
			}
			if tc.error == "" {
				if len(got.Reported.Error) != 0 {
					t.Fatalf("unexpected error: %s", got.Reported.Error)
				}
			} else {
				assertJSONEqual(t, got.Reported.Error, []byte(tc.error))
			}
			if tc.details == "" {
				if len(got.Reported.StatusDetails) != 0 {
					t.Fatalf("unexpected status details: %s", got.Reported.StatusDetails)
				}
			} else {
				assertJSONEqual(t, got.Reported.StatusDetails, []byte(tc.details))
			}
		})
	}
}

func TestCaptureBoundsUpstreamRead(t *testing.T) {
	reader := strings.NewReader(strings.Repeat("x", (1<<20)+9))
	got, err := Capture(&http.Response{StatusCode: 503, Body: io.NopCloser(reader)})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Body) != 1<<20 || reader.Len() != 9 || got.Reported.Status != 503 {
		t.Fatalf("capture exceeded its read bound: bytes=%d remaining=%d status=%d", len(got.Body), reader.Len(), got.Reported.Status)
	}
	assertJSONEqual(t, got.Reported.Error, append(append([]byte{'"'}, got.Body...), '"'))
}

type interruptedErrorBody struct {
	data []byte
	err  error
}

func (b *interruptedErrorBody) Read(p []byte) (int, error) {
	n := copy(p, b.data)
	b.data = b.data[n:]
	return n, b.err
}

func TestCaptureRetainsPartialBodyOnReadFailure(t *testing.T) {
	readErr := errors.New("interrupted response")
	body := &interruptedErrorBody{data: []byte("denied"), err: readErr}
	got, err := Capture(&http.Response{StatusCode: 401, Body: io.NopCloser(body)})
	if !errors.Is(err, readErr) || got.Reported.Status != 401 || !bytes.Equal(got.Body, []byte("denied")) {
		t.Fatalf("capture lost partial response: capture=%+v err=%v", got, err)
	}
	assertJSONEqual(t, got.Reported.Error, []byte(`"denied"`))
}
