package customresponses

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
)

func TestCustomOutputArraysKeepArrayShapeOnHTTPReplay(t *testing.T) {
	for _, content := range [][]canon.Content{{}, {canon.TextContent{Text: "receipt"}}, {canon.TextContent{Text: "receipt"}, canon.ImageContent{MIMEType: "image/png", Data: []byte("image")}}} {
		t.Run(fmt.Sprintf("parts=%d", len(content)), func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Input []struct{ Output json.RawMessage }
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				var parts []map[string]any
				if len(body.Input) != 1 || json.Unmarshal(body.Input[0].Output, &parts) != nil || len(parts) != len(content) {
					t.Errorf("output=%+v", body.Input)
				}
				if len(parts) > 0 && (parts[0]["type"] != "input_text" || parts[0]["text"] != "receipt") {
					t.Errorf("parts=%v", parts)
				}
				if len(parts) > 1 && (parts[1]["type"] != "input_image" || parts[1]["image_url"] != "data:image/png;base64,aW1hZ2U=") {
					t.Errorf("image=%v", parts[1])
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"status":"completed","output":[]}`)
			}))
			defer upstream.Close()
			err := New(nil, Options{}).Run(t.Context(), provider.RunRequest{Target: provider.Target{Provider: "edge", Wire: provider.WireResponses, BaseURL: upstream.URL}, Request: canon.Request{Model: "m", Input: []canon.Item{canon.CustomToolOutput{CallID: "c", Content: content}}}}, &collector{})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
