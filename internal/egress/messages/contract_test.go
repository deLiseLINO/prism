package messages

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/deLiseLINO/prism/internal/canon"
)

func TestToolSnapshotsPreserveNumbersAndIntroductionOrder(t *testing.T) {
	var buffer bytes.Buffer
	e := New(&buffer, false)
	defer e.Close()
	if err := e.Begin(ResponseHeader{ID: "m", Model: "model"}); err != nil {
		t.Fatal(err)
	}
	for _, event := range []canon.Event{
		canon.ItemStarted{Item: canon.FunctionCall{ID: "f", CallID: "call-f", Name: "lookup"}},
		canon.ItemStarted{Item: canon.CustomToolCall{ID: "c", CallID: "call-c", Name: "patch"}},
		canon.CustomToolInputDelta{ItemID: "c", Text: "raw\n"},
		canon.ItemFinished{Item: canon.CustomToolCall{ID: "c", CallID: "call-c", Name: "patch", Input: "raw\npatch"}},
		canon.ItemFinished{Item: canon.FunctionCall{ID: "f", CallID: "call-f", Name: "lookup", Arguments: []byte(`{"id":9007199254740993,"fraction":1.234567890123456789,"power":1e300}`)}},
		canon.TurnFinished{Status: canon.Completed()},
	} {
		if err := e.Frame(event); err != nil {
			t.Fatalf("%T: %v", event, err)
		}
	}
	if err := e.Flush(); err != nil {
		t.Fatal(err)
	}
	var message struct {
		Content []struct {
			ID    string
			Input json.RawMessage
		}
	}
	if err := json.Unmarshal(buffer.Bytes(), &message); err != nil {
		t.Fatal(err)
	}
	if len(message.Content) != 2 || message.Content[0].ID != "call-f" || message.Content[1].ID != "call-c" {
		t.Fatalf("content=%s", buffer.String())
	}
	for _, literal := range []string{"9007199254740993", "1.234567890123456789", "1e300"} {
		if !strings.Contains(string(message.Content[0].Input), literal) {
			t.Fatalf("number %s changed: %s", literal, message.Content[0].Input)
		}
	}
	var input struct{ Input string }
	if err := json.Unmarshal(message.Content[1].Input, &input); err != nil || input.Input != "raw\npatch" {
		t.Fatalf("input=%+v error=%v", input, err)
	}
}
