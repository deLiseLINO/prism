package responses

import (
	"testing"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/reasonenv"
)

func TestCustomOutputArrayPreservesTextAndImages(t *testing.T) {
	req, _, _ := mustParse(t, `{"model":"edge/model","input":[{"type":"custom_tool_call_output","call_id":"c","output":[{"type":"input_text","text":"receipt"},{"type":"input_image","image_url":"data:image/png;base64,aGVsbG8="}]},{"type":"custom_tool_call_output","call_id":"empty","output":[]}]}`, nil)
	output := req.Input[0].(canon.CustomToolOutput)
	if output.CallID != "c" || len(output.Content) != 2 || output.Content[0].(canon.TextContent).Text != "receipt" || string(output.Content[1].(canon.ImageContent).Data) != "hello" {
		t.Fatalf("output=%+v", output)
	}
	if req.Input[1].(canon.CustomToolOutput).Content == nil {
		t.Fatal("empty array became string output")
	}
}

func TestReasoningCarrierRestoresNativeSignature(t *testing.T) {
	carrier := reasonenv.EncodeSignature("native-signature", "thought")
	req, _, _ := mustParse(t, `{"model":"edge/model","input":[{"type":"reasoning","encrypted_content":"`+carrier+`","summary":[]}]}`, nil)
	item := req.Input[0].(canon.ReasoningItem)
	if item.Signature != "native-signature" || item.Content != "thought" || !item.State.IsEmpty() {
		t.Fatalf("reasoning=%+v", item)
	}
	plain, _, _ := mustParse(t, `{"model":"edge/model","input":[{"type":"reasoning","encrypted_content":"opaque-state","summary":[]}]}`, nil)
	if plain.Input[0].(canon.ReasoningItem).State != (canon.OpaqueRef{Store: canon.StoreWire, Key: "opaque-state"}) {
		t.Fatalf("raw state changed: %+v", plain.Input)
	}
}
