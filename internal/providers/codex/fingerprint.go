package codex

import (
	"crypto/rand"
	"fmt"
	"strings"

	"prism/internal/execution"
	"prism/internal/provider"
)

type Header struct {
	Name  string
	Value string
}

type Fingerprint struct {
	Method  string
	URL     string
	Headers []Header
	Body    []byte
}

type fingerprintInput struct {
	Target     provider.Target
	Facts      execution.Facts
	Credential Credential
	Body       []byte
}

func responsesURL(baseURL string) string {
	if strings.TrimSpace(baseURL) == "" {
		return ResponsesURL
	}
	return strings.TrimRight(baseURL, "/") + "/responses"
}

func compactURL(baseURL string) string {
	if strings.TrimSpace(baseURL) == "" {
		return CompactURL
	}
	return strings.TrimRight(baseURL, "/") + "/responses/compact"
}

func BuildFingerprint(in fingerprintInput) (Fingerprint, error) {
	f := Fingerprint{Method: "POST", URL: responsesURL(in.Target.BaseURL), Body: in.Body}
	requestID := string(in.Facts.RequestID)
	if requestID == "" {
		var err error
		requestID, err = newRequestID()
		if err != nil {
			return Fingerprint{}, fmt.Errorf("codex fingerprint: %w", err)
		}
	}
	forwardOrder := []struct {
		wire string
		name execution.ForwardName
	}{
		{"session_id", execution.ForwardSessionID},
		{"originator", execution.ForwardOriginator},
		{"user-agent", execution.ForwardUserAgent},
	}
	f.Headers = append(f.Headers, Header{Name: HeaderContentType, Value: ContentTypeJSON})
	for _, p := range forwardOrder {
		if v, ok := in.Facts.Forward.Get(p.name); ok {
			f.Headers = append(f.Headers, Header{Name: p.wire, Value: v})
		}
	}
	f.Headers = append(f.Headers, Header{Name: HeaderIncludeTiming, Value: "true"})
	f.Headers = append(f.Headers, Header{Name: HeaderAuthorization, Value: "Bearer " + in.Credential.AccessToken})
	if in.Credential.ChatGPTAccountID != "" {
		f.Headers = append(f.Headers, Header{Name: HeaderChatGPTAccountID, Value: in.Credential.ChatGPTAccountID})
	}
	f.Headers = append(f.Headers, Header{Name: HeaderOpenAIBeta, Value: OpenAIBetaResponsesExpr})
	f.Headers = append(f.Headers, Header{Name: HeaderClientRequestID, Value: requestID})
	return f, nil
}

func newRequestID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
