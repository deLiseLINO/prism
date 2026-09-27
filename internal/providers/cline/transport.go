package cline

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
)

type UnwrapTransport struct {
	Next http.RoundTripper
}

func (t UnwrapTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	next := t.Next
	if next == nil {
		next = http.DefaultTransport
	}
	resp, err := next.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if !enveloped(resp) {
		return resp, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 100<<20))
	resp.Body.Close()
	if err != nil {
		resp.Body = io.NopCloser(bytes.NewReader(nil))
		return resp, nil
	}
	var wrapper struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &wrapper); err != nil || len(wrapper.Data) == 0 {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return resp, nil
	}
	resp.Body = io.NopCloser(bytes.NewReader(wrapper.Data))
	resp.ContentLength = int64(len(wrapper.Data))
	resp.Header.Set("Content-Length", strconv.Itoa(len(wrapper.Data)))
	return resp, nil
}

func enveloped(resp *http.Response) bool {
	if resp.StatusCode != http.StatusOK {
		return false
	}
	ct := resp.Header.Get("Content-Type")
	return ct == "application/json" || ct == "application/json; charset=utf-8"
}
