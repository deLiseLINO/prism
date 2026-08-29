package conformance

import (
	"context"
	"fmt"
	"strings"

	"prism/internal/canon"
)

type UpstreamRequest struct {
	Method  string
	URL     string
	Headers Headers
	Body    []byte
}

type BuildOptions struct {
	BaseURL             string
	APIKey              string
	UpstreamProtocol    string
	ReasoningWireFormat string
	ReasoningEffortMap  map[string]string
}

type RequestBuilder interface {
	Build(ctx context.Context, req canon.Request, opts BuildOptions) (*UpstreamRequest, error)
}

type Golden struct {
	CaseID  string  `json:"caseId"`
	Method  string  `json:"method"`
	URL     string  `json:"url"`
	Headers Headers `json:"headers"`
	Body    string  `json:"body"`
}

func CheckGolden(got *UpstreamRequest, want Golden) error {
	_, _, diff := goldenDiff(got, want)
	if diff == "" {
		return nil
	}
	return fmt.Errorf("golden mismatch: %s", diff)
}

func goldenDiff(got *UpstreamRequest, want Golden) (matched, headerMatched bool, diff string) {
	if got.Method != want.Method {
		return false, false, fmt.Sprintf("method %s want %s", got.Method, want.Method)
	}
	if got.URL != want.URL {
		return false, false, fmt.Sprintf("url %s want %s", got.URL, want.URL)
	}
	if string(got.Body) != want.Body {
		return false, false, fmt.Sprintf("body %d bytes want %d bytes", len(got.Body), len(want.Body))
	}
	headersOK := len(got.Headers) == len(want.Headers)
	if headersOK {
		for i := range got.Headers {
			if got.Headers[i] != want.Headers[i] {
				headersOK = false
			}
		}
	}
	if !headersOK {
		return true, false, fmt.Sprintf("headers %v want %v", got.Headers, want.Headers)
	}
	return true, true, ""
}

func ChatCompletionsURL(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.HasSuffix(base, "/chat/completions") {
		return base
	}
	return base + "/chat/completions"
}

func ResponsesURL(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	withoutEndpoint := strings.TrimSuffix(base, "/responses")
	withoutV1 := strings.TrimSuffix(withoutEndpoint, "/v1")
	return withoutV1 + "/v1/responses"
}
