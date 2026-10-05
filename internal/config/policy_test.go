package config

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"
)

func TestLegacyProviderDefaultsRemainEnabled(t *testing.T) {
	p := Provider{Wire: WireCodex, Models: []string{"m1"}}
	if !p.IsEnabled() {
		t.Fatal("provider without enabled field must be enabled")
	}
	d := Document{
		Version:   SchemaVersion,
		Providers: map[string]Provider{"codex-main": p},
	}
	if err := d.validate(); err != nil {
		t.Fatalf("legacy document rejected: %v", err)
	}
}

func TestValidateAcceptsSelectionAndAccountsPath(t *testing.T) {
	disabled := false
	d := Document{
		Version: SchemaVersion,
		Providers: map[string]Provider{
			"codex-main": {
				Wire:           WireCodex,
				Models:         []string{"m1", "m2"},
				DisabledModels: []string{"m2"},
				Enabled:        &disabled,
				Pool: &PoolSettings{
					PinnedAccount: "acct-1",
					AccountsPath:  "accounts.json",
				},
			},
		},
	}
	if err := d.validate(); err != nil {
		t.Fatalf("valid policy document rejected: %v", err)
	}
}

func TestValidateRejectsDisabledModelMissingFromModels(t *testing.T) {
	d := Document{
		Version: SchemaVersion,
		Providers: map[string]Provider{
			"codex-main": {Wire: WireCodex, Models: []string{"m1"}, DisabledModels: []string{"m2"}},
		},
	}
	if err := d.validate(); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("unknown disabled model: got %v, want ErrInvalidValue", err)
	}
}

func TestValidateRejectsDisabledModelWithoutModels(t *testing.T) {
	d := Document{
		Version: SchemaVersion,
		Providers: map[string]Provider{
			"codex-main": {Wire: WireCodex, DisabledModels: []string{"m1"}},
		},
	}
	if err := d.validate(); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("disabled model without configured models: got %v, want ErrInvalidValue", err)
	}
}

func TestValidateRejectsInvalidPinnedAccount(t *testing.T) {
	for _, pinned := range []string{" acct-1", "acct 1", "acct-1\t"} {
		d := Document{
			Version: SchemaVersion,
			Providers: map[string]Provider{
				"codex-main": {Wire: WireCodex, Pool: &PoolSettings{PinnedAccount: pinned}},
			},
		}
		if err := d.validate(); !errors.Is(err, ErrInvalidValue) {
			t.Fatalf("pinned account %q: got %v, want ErrInvalidValue", pinned, err)
		}
	}
}

func TestPoolDecodeIgnoresRemovedFields(t *testing.T) {
	raw := []byte(`{"strategy":"round_robin","autoSwitch":false,"autoSwitchThreshold":0.9,"affinity":"off","pinnedAccount":"acct-1","accountsPath":"accounts.json","maxFailovers":2,"cooldownDefault":1,"cooldownMax":2,"probeEvery":3}`)
	var pool PoolSettings
	if err := json.Unmarshal(raw, &pool); err != nil {
		t.Fatalf("old pool fields must be ignored: %v", err)
	}
	if pool.PinnedAccount != "acct-1" || pool.AccountsPath != "accounts.json" {
		t.Fatalf("decoded pool = %+v", pool)
	}
}

func msPtr(v int64) *int64 { return &v }

func TestValidateWaitSettings(t *testing.T) {
	tests := []struct {
		name string
		wait *WaitSettings
		ok   bool
	}{
		{"absent", nil, true},
		{"empty object", &WaitSettings{}, true},
		{"explicit zero disables", &WaitSettings{FirstProgressMs: msPtr(0), IdleMs: msPtr(0)}, true},
		{"beyond a day is allowed", &WaitSettings{FirstProgressMs: msPtr(48 * 3600 * 1000)}, true},
		{"largest duration-safe value", &WaitSettings{IdleMs: msPtr(maxWaitMs)}, true},
		{"negative first progress", &WaitSettings{FirstProgressMs: msPtr(-1)}, false},
		{"negative idle", &WaitSettings{IdleMs: msPtr(-1)}, false},
		{"first progress overflows duration", &WaitSettings{FirstProgressMs: msPtr(maxWaitMs + 1)}, false},
		{"idle overflows duration", &WaitSettings{IdleMs: msPtr(math.MaxInt64)}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := Document{Version: SchemaVersion, Providers: map[string]Provider{"router": {Wire: WireCodex, Wait: tc.wait}}}
			err := d.validate()
			if tc.ok && err != nil {
				t.Fatalf("rejected: %v", err)
			}
			if !tc.ok && !errors.Is(err, ErrInvalidValue) {
				t.Fatalf("got %v, want ErrInvalidValue", err)
			}
		})
	}
}

func TestMaxWaitMsConvertsToDurationWithoutOverflow(t *testing.T) {
	ms := maxWaitMs
	if got := time.Duration(ms) * time.Millisecond; got <= 0 {
		t.Fatalf("MaxWaitMs overflows time.Duration: %v", got)
	}
	ms++
	if got := time.Duration(ms) * time.Millisecond; got > 0 {
		t.Fatalf("MaxWaitMs is not the largest safe value: %v", got)
	}
}

func TestWaitSettingsDecodeKeepsOmissionDistinctFromZero(t *testing.T) {
	var w WaitSettings
	if err := json.Unmarshal([]byte(`{"idleMs":0}`), &w); err != nil {
		t.Fatal(err)
	}
	if w.FirstProgressMs != nil {
		t.Fatalf("omitted firstProgressMs decoded as %d", *w.FirstProgressMs)
	}
	if w.IdleMs == nil || *w.IdleMs != 0 {
		t.Fatalf("explicit zero idleMs lost: %v", w.IdleMs)
	}
	out, err := json.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"idleMs":0}` {
		t.Fatalf("re-encoded = %s", out)
	}
}
