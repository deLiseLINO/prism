package messages

import (
	"testing"

	"github.com/deLiseLINO/prism/internal/canon"
)

func TestRejectsLossyAttachmentAndToolResultInputs(t *testing.T) {
	for _, content := range []string{
		`[{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"cGRm"}}]`,
		`[{"type":"image","source":{"type":"url","url":"https://example.com/image.png"}}]`,
		`[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"invalid"}}]`,
		`[{"type":"tool_result","tool_use_id":"c","content":null}]`,
		`[{"type":"tool_result","content":"lost"}]`,
		`[{"type":"tool_result","tool_use_id":"c","content":[{"type":"document","source":{"type":"base64","data":"cGRm"}}]}]`,
	} {
		t.Run(content, func(t *testing.T) {
			_, _, err := parseBody(t, `{"model":"edge/model","max_tokens":64,"messages":[{"role":"user","content":`+content+`}]}`, nil)
			if err == nil {
				t.Fatal("accepted input that cannot be preserved")
			}
		})
	}
}

func TestAbsentToolResultContentIsEmptyOutput(t *testing.T) {
	for _, extra := range []string{"", `,"is_error":true`} {
		req := mustParse(t, `{"model":"edge/model","max_tokens":64,"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"c"`+extra+`}]}]}`)
		if len(req.Input) != 1 {
			t.Fatalf("input=%+v", req.Input)
		}
		output := req.Input[0].(canon.FunctionOutput)
		if output.CallID != "c" || extra == "" && len(output.Output) != 0 || extra != "" && output.Output[0].(canon.TextContent).Text != "[tool error]" {
			t.Fatalf("output=%+v", output)
		}
	}
}
