package config

import (
	"encoding/json"
	"errors"
	"testing"
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
