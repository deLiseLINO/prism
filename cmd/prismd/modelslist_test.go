package main

import "testing"

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
