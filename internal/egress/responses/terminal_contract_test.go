package responses

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/reasonenv"
)

func TestFinalSnapshotFollowsIntroductions(t *testing.T) {
	for _, buffered := range []bool{false, true} {
		t.Run(map[bool]string{false: "stream", true: "buffered"}[buffered], func(t *testing.T) {
			buffer := &lockBuffer{}
			clock := newFakeClock(time.Unix(0, 0))
			e := NewWithClock(buffer, codexFacts(), clock, nil)
			if buffered {
				e.Close()
				e = NewBufferedWithClock(buffer, codexFacts(), clock, nil)
			}
			defer e.Close()
			if err := e.Begin(header()); err != nil {
				t.Fatal(err)
			}
			for _, event := range []canon.Event{
				canon.ItemStarted{Item: canon.Message{ID: "m", Role: canon.RoleAssistant}},
				canon.TextDelta{ItemID: "m", Text: "prefix"},
				canon.ItemStarted{Item: canon.FunctionCall{ID: "f", CallID: "c", Name: "lookup"}},
				canon.ItemStarted{Item: canon.ReasoningItem{ID: "r"}},
				canon.ItemStateAvailable{ItemID: "r", State: canon.OpaqueRef{Store: canon.StoreWire, Key: "opaque"}},
				canon.ItemFinished{Item: canon.ReasoningItem{ID: "r"}},
				canon.ItemFinished{Item: canon.FunctionCall{ID: "f", CallID: "c", Name: "lookup", Arguments: []byte(`{"id":9007199254740993}`)}},
				canon.ItemFinished{Item: canon.Message{ID: "m", Role: canon.RoleAssistant, Content: []canon.Content{canon.TextContent{Text: "prefix suffix"}}}},
				canon.TurnFinished{Status: canon.Completed()},
			} {
				if err := e.Frame(event); err != nil {
					t.Fatalf("%T: %v", event, err)
				}
			}
			if err := e.Flush(); err != nil {
				t.Fatal(err)
			}
			var response map[string]any
			if buffered {
				if err := json.Unmarshal([]byte(buffer.String()), &response); err != nil {
					t.Fatal(err)
				}
			} else {
				var text string
				for _, frame := range eventFrames(t, buffer) {
					data := dataMap(t, frame)
					if frame.event == "response.output_text.delta" {
						text += data["delta"].(string)
					}
					if frame.event == "response.completed" {
						response = data["response"].(map[string]any)
					}
				}
				if text != "prefix suffix" {
					t.Fatalf("text=%q", text)
				}
			}
			output := response["output"].([]any)
			if len(output) != 3 || output[0].(map[string]any)["type"] != "message" || output[1].(map[string]any)["type"] != "function_call" || output[2].(map[string]any)["encrypted_content"] != "opaque" {
				t.Fatalf("output=%+v", output)
			}
		})
	}
}

func TestNativeSignatureCarrierSurvivesPublicReplay(t *testing.T) {
	buffer := &lockBuffer{}
	e := NewBufferedWithClock(buffer, codexFacts(), newFakeClock(time.Unix(0, 0)), nil)
	defer e.Close()
	if err := e.Begin(header()); err != nil {
		t.Fatal(err)
	}
	for _, event := range []canon.Event{canon.ItemStarted{Item: canon.ReasoningItem{ID: "r"}}, canon.ItemFinished{Item: canon.ReasoningItem{ID: "r", Content: "thought", Signature: "native-signature"}}, canon.TurnFinished{Status: canon.Completed()}} {
		if err := e.Frame(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.Flush(); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Output []struct {
			Encrypted string `json:"encrypted_content"`
		}
	}
	if err := json.Unmarshal([]byte(buffer.String()), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Output) != 1 {
		t.Fatalf("output=%s", buffer.String())
	}
	envelope, ok := reasonenv.Decode(response.Output[0].Encrypted)
	if !ok || envelope.Sig != "native-signature" || envelope.Txt != "thought" {
		t.Fatalf("carrier=%+v", envelope)
	}
}
