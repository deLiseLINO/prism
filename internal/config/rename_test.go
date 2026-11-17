package config

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// providerRefFields lists every string-bearing Document field that can name
// a provider id; walkProviderRefs must visit each one.
var providerRefFields = []string{
	"Providers{key}",
	"Providers{}.APIKeyRef",
	"Providers{}.ModelCatalogs[].Account",
	"Providers{}.Pool.PinnedAccount",
	"Combos{}.Targets[].Provider",
	"Routes{key}",
	"Routes{}",
	"Aliases{key}",
	"Aliases{}",
	"VisionSidecar.Target",
}

// nonProviderFields lists string-bearing fields that never hold a provider
// id. The ledger names old ids on purpose.
var nonProviderFields = []string{
	"Daemon.Listen",
	"Providers{}.Wire",
	"Providers{}.BaseURL",
	"Providers{}.DefaultModel",
	"Providers{}.ModelMode",
	"Providers{}.ModelCatalogs[].BaseURL",
	"Providers{}.ModelCatalogs[].Project",
	"Providers{}.ModelCatalogs[].RawModels[]",
	"Providers{}.Models[]",
	"Providers{}.DisabledModels[]",
	"Providers{}.SyncedModels[]",
	"Providers{}.Discovered{key}",
	"Providers{}.Discovered{}.Kind",
	"Providers{}.ModelSettings{key}",
	"Providers{}.ModelSettings{}.ReasoningEfforts[]",
	"Providers{}.ModelSettings{}.Wire",
	"Providers{}.Pool.AccountsPath",
	"Combos{key}",
	"Combos{}.Targets[].Model",
	"Combos{}.Strategy",
	"Combos{}.Alias",
	"Combos{}.NativeAlias",
	"Combos{}.DisplayName",
	"Hosts{key}",
	"Hosts{}.Address",
	"Integrations{key}",
	"ProviderRenames[].ID",
	"ProviderRenames[].From",
	"ProviderRenames[].To",
}

func stringFields(t reflect.Type, path string, out *[]string) {
	switch t.Kind() {
	case reflect.String:
		*out = append(*out, path)
	case reflect.Pointer, reflect.Slice:
		suffix := ""
		if t.Kind() == reflect.Slice {
			suffix = "[]"
		}
		stringFields(t.Elem(), path+suffix, out)
	case reflect.Map:
		if t.Key().Kind() == reflect.String {
			*out = append(*out, path+"{key}")
		}
		stringFields(t.Elem(), path+"{}", out)
	case reflect.Struct:
		if t == reflect.TypeFor[time.Time]() {
			return
		}
		for i := range t.NumField() {
			f := t.Field(i)
			if !f.IsExported() || f.Tag.Get("json") == "-" {
				continue
			}
			name := f.Name
			if path != "" {
				name = path + "." + f.Name
			}
			stringFields(f.Type, name, out)
		}
	}
}

func TestEveryDocumentStringFieldIsClassifiedForProviderRefs(t *testing.T) {
	var got []string
	stringFields(reflect.TypeFor[Document](), "", &got)
	known := append(slices.Clone(providerRefFields), nonProviderFields...)
	for _, path := range got {
		if !slices.Contains(known, path) {
			t.Errorf("Document field %s is not classified: add it to providerRefFields and walkProviderRefs if it can hold a provider id, else to nonProviderFields", path)
		}
	}
	for _, path := range known {
		if !slices.Contains(got, path) {
			t.Errorf("classified field %s no longer exists", path)
		}
	}
}

func referencingDoc() Document {
	return Document{
		Version: SchemaVersion,
		Providers: map[string]Provider{
			"gw": {
				Wire: WireOpenAIChat, BaseURL: "https://gw.example.test/v1", APIKeyRef: "gw:default",
				Models: []string{"m1", "m2"}, DisabledModels: []string{"m2"}, DefaultModel: "m1",
				ModelSettings: map[string]ModelSettings{"m1": {ImageInput: boolPtr(true)}},
				Pool:          &PoolSettings{PinnedAccount: "gw:default"},
				ModelCatalogs: []ModelCatalog{{BaseURL: "https://gw.example.test", Account: "gw:default"}},
			},
			"gw2":    {Wire: WireOpenAIChat, BaseURL: "https://gw2.example.test/v1", Models: []string{"m1"}},
			"router": {Wire: WireOpenAIChat, BaseURL: "https://router.example.test/v1", Models: []string{"r1"}},
		},
		Combos: map[string]Combo{
			"fast":  {Strategy: ComboFailover, Targets: []Target{{Provider: "gw", Model: "m1"}, {Provider: "router", Model: "r1"}}},
			"solo":  {Strategy: ComboFailover, Targets: []Target{{Provider: "gw", Model: "m1"}}},
			"other": {Strategy: ComboFailover, Targets: []Target{{Provider: "gw2", Model: "m1"}}},
		},
		Routes:        map[string]string{"gw/m1": "router/r1", "pick": "gw/m1", "keep": "gw2/m1"},
		Aliases:       map[string]string{"claude-gw--m1": "gw/m1", "quick": "fast"},
		VisionSidecar: VisionSidecarSettings{Enabled: true, Target: "gw/m1"},
	}
}

// jsonStrings collects every key and string value in a marshaled document.
func jsonStrings(v any, out *[]string) {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			*out = append(*out, k)
			jsonStrings(child, out)
		}
	case []any:
		for _, child := range x {
			jsonStrings(child, out)
		}
	case string:
		*out = append(*out, x)
	}
}

func namesProvider(s, id string) bool {
	return s == id || strings.HasPrefix(s, id+"/") || strings.HasPrefix(s, id+":") || strings.HasPrefix(s, ClaudeAliasStem(id))
}

func TestRenameProviderLeavesNoReferenceToOldID(t *testing.T) {
	doc := referencingDoc()
	if err := doc.validate(); err != nil {
		t.Fatalf("fixture invalid: %v", err)
	}
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	out, rec, err := RenameProvider(doc, "gw", "edge", "r1", at)
	if err != nil {
		t.Fatalf("RenameProvider: %v", err)
	}
	if rec != (ProviderRename{ID: "r1", From: "gw", To: "edge", At: at}) {
		t.Fatalf("record = %+v", rec)
	}
	if !slices.Equal(out.ProviderRenames, []ProviderRename{rec}) {
		t.Fatalf("ledger = %+v", out.ProviderRenames)
	}
	out.ProviderRenames = nil
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		t.Fatal(err)
	}
	var all []string
	jsonStrings(tree, &all)
	for _, s := range all {
		if namesProvider(s, "gw") {
			t.Errorf("renamed document still names gw: %q", s)
		}
	}
	for _, want := range []string{"edge/m1", "edge:default", "claude-edge--m1", "edge", "gw2/m1"} {
		if !slices.Contains(all, want) {
			t.Errorf("renamed document lacks %q", want)
		}
	}
	if got := out.Providers["edge"]; got.BaseURL != "https://gw.example.test/v1" || !slices.Equal(got.Models, []string{"m1", "m2"}) || got.DefaultModel != "m1" {
		t.Fatalf("provider entry did not move unchanged: %+v", got)
	}
	if out.Combos["other"].Targets[0].Provider != "gw2" || out.Routes["keep"] != "gw2/m1" {
		t.Fatal("rename touched a provider whose id only starts with the old id")
	}
	if _, ok := doc.Providers["gw"]; !ok || doc.Routes["pick"] != "gw/m1" {
		t.Fatal("RenameProvider mutated its input")
	}
}

func TestCheckRenameReportsEveryCollisionAndRenameChangesNothing(t *testing.T) {
	doc := referencingDoc()
	doc.Providers["codex"] = Provider{Wire: WireCodex}
	doc.Providers["Edge_1"] = Provider{Wire: WireOpenAIChat, BaseURL: "https://x.example.test"}
	doc.Routes["side/x"] = "router/r1"
	cases := []struct {
		from, to string
		want     RenameConflictKind
	}{
		{"missing", "edge", ConflictNotFound},
		{"gw", "bad.id", ConflictInvalidID},
		{"gw", "bad/id", ConflictInvalidID},
		{"gw", "GW", ConflictSameID},
		{"gw", "router", ConflictProviderExists},
		{"gw", "codex", ConflictReservedLogin},
		{"codex", "edge", ConflictReservedLogin},
		{"gw", "edge_1", ConflictDirCollision},
		{"gw", "side", ConflictModelShadowed},
	}
	before, _ := json.Marshal(doc)
	for _, tc := range cases {
		conflicts := CheckRename(doc, tc.from, tc.to)
		if !slices.ContainsFunc(conflicts, func(c RenameConflict) bool { return c.Kind == tc.want }) {
			t.Errorf("%s -> %s: want %s, got %+v", tc.from, tc.to, tc.want, conflicts)
		}
		_, _, err := RenameProvider(doc, tc.from, tc.to, "id", time.Now())
		var blocked RenameConflicts
		if !errors.As(err, &blocked) {
			t.Errorf("%s -> %s: RenameProvider error = %v", tc.from, tc.to, err)
		}
	}
	after, _ := json.Marshal(doc)
	if string(before) != string(after) {
		t.Fatal("a refused rename changed the document")
	}
	if c := CheckRename(doc, "gw", "edge"); len(c) != 0 {
		t.Fatalf("clean rename reported %+v", c)
	}
}

func TestActiveRenamesFollowChainsAndYieldToLiveProviders(t *testing.T) {
	doc := referencingDoc()
	doc, _, err := RenameProvider(doc, "gw", "edge", "a", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	doc, _, err = RenameProvider(doc, "edge", "relay", "b", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	active := doc.ActiveRenames()
	if len(active) != 2 || active[0].Live != "relay" || active[1].Live != "relay" {
		t.Fatalf("chain: %+v", active)
	}
	if !doc.RenameApplied("gw", "relay") || !doc.RenameApplied("edge", "relay") || doc.RenameApplied("gw", "edge") {
		t.Fatal("RenameApplied does not follow the chain to the live id")
	}
	doc.Providers["gw"] = Provider{Wire: WireOpenAIChat, BaseURL: "https://new.example.test"}
	active = doc.ActiveRenames()
	if len(active) != 1 || active[0].From != "edge" {
		t.Fatalf("a live gw must shadow its record: %+v", active)
	}
	delete(doc.Providers, "gw")
	back, _, err := RenameProvider(doc, "relay", "edge", "c", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(back.ProviderRenames))
	for i, r := range back.ProviderRenames {
		ids[i] = r.ID
	}
	if !slices.Equal(ids, []string{"a", "c"}) {
		t.Fatalf("renaming back to edge must drop records leaving edge: %v", ids)
	}
}

func TestDeleteProviderDropsTargetsAndKeepsClientNames(t *testing.T) {
	doc := referencingDoc()
	doc.VisionSidecar = VisionSidecarSettings{}
	out := DeleteProvider(doc, "gw")
	if _, ok := out.Providers["gw"]; ok {
		t.Fatal("provider kept")
	}
	if _, ok := out.Combos["solo"]; ok {
		t.Fatal("combo left without targets kept")
	}
	if got := out.Combos["fast"].Targets; len(got) != 1 || got[0].Provider != "router" {
		t.Fatalf("fast targets = %+v", got)
	}
	if _, ok := out.Routes["pick"]; ok {
		t.Fatal("route to deleted provider kept")
	}
	if out.Routes["gw/m1"] != "router/r1" || out.Routes["keep"] != "gw2/m1" {
		t.Fatalf("unrelated routes changed: %v", out.Routes)
	}
	if _, ok := out.Aliases["claude-gw--m1"]; ok {
		t.Fatal("alias to deleted provider kept")
	}
	if err := out.validate(); err != nil {
		t.Fatalf("document after delete invalid: %v", err)
	}
}

func TestValidateRejectsMalformedLedger(t *testing.T) {
	doc := validDoc()
	doc.ProviderRenames = []ProviderRename{{ID: "a", From: "x", To: "x"}}
	if err := doc.validate(); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("self rename: %v", err)
	}
	doc.ProviderRenames = []ProviderRename{{ID: "a", From: "x", To: "y"}, {ID: "a", From: "y", To: "z"}}
	if err := doc.validate(); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("duplicate id: %v", err)
	}
	doc.ProviderRenames = []ProviderRename{{From: "x", To: "y"}}
	if err := doc.validate(); !errors.Is(err, ErrEmptyField) {
		t.Fatalf("missing id: %v", err)
	}
}
