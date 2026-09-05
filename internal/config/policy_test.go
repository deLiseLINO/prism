package config

import (
	"errors"
	"testing"
)

func TestLegacyProviderDefaultsRemainEnabled(t *testing.T) {
	p := Provider{Wire: WireCodex, Models: []string{"m1"}}
	if !p.IsEnabled() {
		t.Fatal("provider without enabled field must be enabled")
	}
	pool := PoolSettings{Strategy: PoolQuota, AutoSwitchThreshold: 0, MaxFailovers: 3}
	if !pool.AutoSwitchEnabled() {
		t.Fatal("pool without autoSwitch field must keep auto-switch enabled")
	}
	d := Document{
		Version:   SchemaVersion,
		Providers: map[string]Provider{"codex-main": p},
	}
	if err := d.validate(); err != nil {
		t.Fatalf("legacy document rejected: %v", err)
	}
}

func TestValidateAcceptsFullSelectionPolicy(t *testing.T) {
	disabled := false
	autoOff := false
	d := Document{
		Version: SchemaVersion,
		Providers: map[string]Provider{
			"codex-main": {
				Wire:           WireCodex,
				Models:         []string{"m1", "m2"},
				DisabledModels: []string{"m2"},
				Enabled:        &disabled,
				Pool: &PoolSettings{
					Strategy:            PoolRoundRobin,
					AutoSwitch:          &autoOff,
					AutoSwitchThreshold: 0.9,
					Affinity:            AffinitySticky,
					PinnedAccount:       "acct-1",
					MaxFailovers:        2,
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

func TestValidateRejectsUnknownAffinity(t *testing.T) {
	d := Document{
		Version: SchemaVersion,
		Providers: map[string]Provider{
			"codex-main": {Wire: WireCodex, Pool: &PoolSettings{Strategy: PoolQuota, Affinity: "least_loaded"}},
		},
	}
	if err := d.validate(); !errors.Is(err, ErrUnknownAffinity) {
		t.Fatalf("unknown affinity: got %v, want ErrUnknownAffinity", err)
	}
}

func TestValidateRejectsInvalidPinnedAccount(t *testing.T) {
	for _, pinned := range []string{" acct-1", "acct 1", "acct-1\t"} {
		d := Document{
			Version: SchemaVersion,
			Providers: map[string]Provider{
				"codex-main": {Wire: WireCodex, Pool: &PoolSettings{Strategy: PoolQuota, PinnedAccount: pinned}},
			},
		}
		if err := d.validate(); !errors.Is(err, ErrInvalidValue) {
			t.Fatalf("pinned account %q: got %v, want ErrInvalidValue", pinned, err)
		}
	}
}

func TestValidateRejectsInvalidThreshold(t *testing.T) {
	d := Document{
		Version: SchemaVersion,
		Providers: map[string]Provider{
			"codex-main": {Wire: WireCodex, Pool: &PoolSettings{Strategy: PoolQuota, AutoSwitchThreshold: 1.5}},
		},
	}
	if err := d.validate(); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("threshold above one: got %v, want ErrInvalidValue", err)
	}
}
