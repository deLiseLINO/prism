package codex

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
)

type closeCountingBody struct {
	io.Reader
	closes int
}

func (b *closeCountingBody) Close() error { b.closes++; return nil }

func TestInspectedReasoningErrorClosesOriginalBody(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary failure", true: "reasoning retry"}[rejected], func(t *testing.T) {
			payload := `{"error":{"message":"ordinary failure"}}`
			if rejected {
				payload = `{"error":{"code":"invalid_encrypted_content","message":"cannot verify reasoning"}}`
			}
			body := &closeCountingBody{Reader: strings.NewReader(payload)}
			calls := 0
			runner := newRunnerWithTransport(t, func(*http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					return &http.Response{StatusCode: 400, Header: http.Header{}, Body: body}, nil
				}
				return newResp(200, nil, joinFrames(`{"type":"response.completed","response":{}}`)), nil
			})
			err := runner.Run(t.Context(), provider.RunRequest{Request: canon.Request{Input: []canon.Item{canon.ReasoningItem{State: canon.OpaqueRef{Store: canon.StoreWire, Key: "opaque"}}}}}, &captureSink{})
			if rejected && err != nil || !rejected && err == nil || body.closes != 1 {
				t.Fatalf("error=%v closes=%d calls=%d", err, body.closes, calls)
			}
		})
	}
}
