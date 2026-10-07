package cline

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEnvelopeReadFailureIsTransportFailure(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "200")
		io.WriteString(w, `{"data":{"choices":[]}}`)
	}))
	defer up.Close()
	client := &http.Client{Transport: UnwrapTransport{Next: up.Client().Transport}}
	resp, err := client.Get(up.URL)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("truncated HTTP envelope returned success")
	}
}
