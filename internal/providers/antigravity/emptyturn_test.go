package antigravity

import (
	"encoding/json"
	"testing"

	"github.com/deLiseLINO/prism/internal/canon"
)

// A replayed thought without a usable signature is dropped from the wire; the
// model turn that carried only that thought must not survive as an empty
// content, which the upstream rejects.
func TestEnvelopeUnsignedThoughtLeavesNoEmptyTurn(t *testing.T) {
	req := baseRequest()
	req.Model = "gemini-3-pro"
	req.Instructions = nil
	req.Input = []canon.Item{
		textMessage(canon.RoleUser, "u1"),
		canon.ReasoningItem{ID: "r1", Content: "think1"},
		textMessage(canon.RoleAssistant, "a1"),
		textMessage(canon.RoleUser, "u2"),
		canon.ReasoningItem{ID: "r2", Content: "think2"},
		textMessage(canon.RoleAssistant, "a2"),
		textMessage(canon.RoleUser, "u3"),
	}
	body, err := BuildEnvelope(req, "p", "r", "-1", ModelSelection{})
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Request struct {
			Contents []struct {
				Role  string
				Parts []json.RawMessage
			}
		}
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatal(err)
	}
	for i, c := range env.Request.Contents {
		if len(c.Parts) == 0 {
			t.Fatalf("contents[%d] (%s) has no parts: %s", i, c.Role, body)
		}
	}
}
