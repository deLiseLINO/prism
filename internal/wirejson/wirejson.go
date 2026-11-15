// Package wirejson encodes request bodies sent to upstream providers.
package wirejson

import (
	"bytes"
	"encoding/json"
)

// Marshal encodes v like json.Marshal but leaves <, > and & as literal
// bytes. json.Marshal turns each into a six-byte \u escape, which inflates
// code-heavy request bodies against upstreams that cap the body by bytes.
func Marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
