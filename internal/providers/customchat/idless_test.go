package customchat

import "testing"

// Gateways that omit ids and reuse index 0 for every call announce each call by
// its function name; a second name starts a second call.
func TestStreamIdlessCallsReusingIndexStaySeparate(t *testing.T) {
	calls := finishedCalls(t, frames(
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"f","arguments":"{\"a\":1}"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"g","arguments":"{\"b\":2}"}}]}}]}`,
		finishToolCalls))
	if len(calls) != 2 || calls[0].Name != "f" || string(calls[0].Arguments) != `{"a":1}` || calls[1].Name != "g" || string(calls[1].Arguments) != `{"b":2}` {
		t.Fatalf("calls = %+v", calls)
	}
}
