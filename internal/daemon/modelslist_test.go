package daemon

import (
	"testing"

	"github.com/deLiseLINO/prism/internal/config"
)

func TestParseOpenAIModelList(t *testing.T) {
	body := []byte(`{"object":"list","data":[
		{"id":"long","max_model_len":128000,"context_length":32000,"input":["text","image"]},
		{"id":"ctx","context_length":64000,"input_modalities":["text","image"]},
		{"id":"arch","architecture":{"input_modalities":["image"]}},
		{"id":"text","max_model_len":8000,"input":["text"]},
		{"id":"absent","context_length":1000},
		{"id":"zero","max_model_len":0,"context_length":null},
		{"id":"bad-window","max_model_len":"wide","context_length":"also","input":"image"},
		{"id":"bad-mod","input":{"text":true},"input_modalities":["text"]},
		{"id":""},
		"not-an-object"
	]}`)
	rows, err := parseOpenAIModelList(body)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]listedModel{}
	for _, row := range rows {
		byID[row.ID] = row
	}
	if len(byID) != 8 {
		t.Fatalf("rows = %d, want 8 named models", len(byID))
	}
	long := byID["long"]
	if long.ContextWindow == nil || *long.ContextWindow != 128000 {
		t.Fatalf("max_model_len = %v, want 128000", long.ContextWindow)
	}
	if long.Image == nil || !*long.Image {
		t.Fatalf("input image = %v, want true", long.Image)
	}
	ctx := byID["ctx"]
	if ctx.ContextWindow == nil || *ctx.ContextWindow != 64000 {
		t.Fatalf("context_length = %v, want 64000", ctx.ContextWindow)
	}
	if ctx.Image == nil || !*ctx.Image {
		t.Fatalf("input_modalities image = %v, want true", ctx.Image)
	}
	if arch := byID["arch"]; arch.Image == nil || !*arch.Image || arch.ContextWindow != nil {
		t.Fatalf("architecture image = %+v", arch)
	}
	text := byID["text"]
	if text.Image == nil || *text.Image {
		t.Fatalf("text-only image = %v, want false", text.Image)
	}
	if absent := byID["absent"]; absent.Image != nil || absent.ContextWindow == nil || *absent.ContextWindow != 1000 {
		t.Fatalf("absent modalities = %+v", absent)
	}
	if zero := byID["zero"]; zero.ContextWindow != nil || zero.Image != nil {
		t.Fatalf("zero and null stored: %+v", zero)
	}
	if bad := byID["bad-window"]; bad.ContextWindow != nil || bad.Image != nil {
		t.Fatalf("bad window stored: %+v", bad)
	}
	if bad := byID["bad-mod"]; bad.Image == nil || *bad.Image {
		t.Fatalf("one bad modality field failed the row: %+v", bad)
	}

	if _, err := parseOpenAIModelList([]byte(`[]`)); err == nil {
		t.Fatal("array body must error")
	}
	if _, err := parseOpenAIModelList([]byte(`{"models":[]}`)); err == nil {
		t.Fatal("missing data array must error")
	}
	if _, err := parseOpenAIModelList([]byte(`{"data":null}`)); err == nil {
		t.Fatal("null data must error")
	}
	rows, err = parseOpenAIModelList([]byte(`{"data":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("empty data = %v, want success with no rows", rows)
	}
}

func TestParseOpenAIModelListMarksImageGenerationModels(t *testing.T) {
	body := []byte(`{"object":"list","data":[
		{"id":"img-model-a","object":"model","owned_by":"gw","output_modalities":["image"]},
		{"id":"img-model-b","architecture":{"input_modalities":["text","image"],"output_modalities":["image"]}},
		{"id":"chat-a","output_modalities":["text"]},
		{"id":"mixed","output_modalities":["text","image"]},
		{"id":"plain"},
		{"id":"bad-out","output_modalities":"image"}
	]}`)
	rows, err := parseOpenAIModelList(body)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]config.ModelKind{}
	for _, row := range rows {
		kinds[row.ID] = row.Kind
	}
	want := map[string]config.ModelKind{
		"img-model-a": config.ModelKindImage,
		"img-model-b": config.ModelKindImage,
		"chat-a":      "",
		"mixed":       "",
		"plain":       "",
		"bad-out":     "",
	}
	for id, kind := range want {
		got, ok := kinds[id]
		if !ok || got != kind {
			t.Fatalf("%s kind = %q (listed %v), want %q", id, got, ok, kind)
		}
	}
}

func TestParseOpenAIModelListReadsMaxOutput(t *testing.T) {
	body := []byte(`{"object":"list","data":[
		{"id":"output","max_output_tokens":64000,"max_completion_tokens":32000},
		{"id":"completion","max_output_tokens":0,"max_completion_tokens":32000},
		{"id":"top","context_length":200000,"top_provider":{"context_length":200000,"max_completion_tokens":16384}},
		{"id":"bare","context_length":131072,"max_tokens":131072},
		{"id":"null-top","top_provider":null},
		{"id":"bad-top","top_provider":"x","max_completion_tokens":-1}
	]}`)
	rows, err := parseOpenAIModelList(body)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*int{}
	for _, row := range rows {
		got[row.ID] = row.MaxOutput
	}
	want := map[string]int{"output": 64000, "completion": 32000, "top": 16384, "bare": 0, "null-top": 0, "bad-top": 0}
	if len(got) != len(want) {
		t.Fatalf("rows = %d, want %d", len(got), len(want))
	}
	for id, n := range want {
		v := got[id]
		if n == 0 && v != nil || n > 0 && (v == nil || *v != n) {
			t.Fatalf("%s max output = %v, want %d", id, v, n)
		}
	}
}
