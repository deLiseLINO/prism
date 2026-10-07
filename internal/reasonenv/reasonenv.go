package reasonenv

import (
	"encoding/base64"
	"encoding/json"
	"strings"
)

// Prefix marks a prism-minted thinking signature. A routed model has no real
// Anthropic signature, but Claude Code must resend a non-empty one or the
// next turn is refused; the envelope carries the thinking text (or redacted
// payload) through that round-trip in a form prism can recognize and decode.
const Prefix = "prismr1:"

type Envelope struct {
	Txt string   `json:"txt,omitempty"`
	Red []string `json:"red,omitempty"`
	Sig string   `json:"sig,omitempty"`
}

func Encode(txt string) string {
	raw, err := json.Marshal(Envelope{Txt: txt})
	if err != nil {
		return Prefix + base64.StdEncoding.EncodeToString([]byte(`{"txt":""}`))
	}
	return Prefix + base64.StdEncoding.EncodeToString(raw)
}

func EncodeRedacted(data []string) string {
	if len(data) == 0 {
		data = []string{""}
	}
	raw, err := json.Marshal(Envelope{Red: data})
	if err != nil {
		return Prefix + base64.StdEncoding.EncodeToString([]byte(`{"red":[""]}`))
	}
	return Prefix + base64.StdEncoding.EncodeToString(raw)
}

func EncodeSignature(signature, text string) string {
	raw, _ := json.Marshal(Envelope{Sig: signature, Txt: text})
	return Prefix + base64.StdEncoding.EncodeToString(raw)
}

func Decode(signature string) (Envelope, bool) {
	if !strings.HasPrefix(signature, Prefix) {
		return Envelope{}, false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(signature, Prefix))
	if err != nil {
		return Envelope{}, false
	}
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return Envelope{}, false
	}
	return env, true
}
