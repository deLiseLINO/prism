package antigravity

import (
	"os"
	"testing"
	"time"

	"prism/internal/quota"
)

func TestDecodeQuotaFixture(t *testing.T) {
	data, err := os.ReadFile("fixtures/quota-models.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	windows, err := DecodeQuota(data)
	if err != nil {
		t.Fatalf("DecodeQuota: %v", err)
	}
	if windows.Gem == nil || windows.Cla == nil {
		t.Fatalf("both family windows must be present: %+v", windows)
	}
	if windows.Gem.Used != 2500 || windows.Gem.Limit == nil || *windows.Gem.Limit != 10000 {
		t.Fatalf("gem used = %d limit = %+v, want 2500/10000 basis points (remainingFraction 0.75)", windows.Gem.Used, windows.Gem.Limit)
	}
	if windows.Gem.Source != quota.SourceEndpoint {
		t.Fatalf("gem source = %v, want SourceEndpoint", windows.Gem.Source)
	}
	wantReset := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
	if !windows.Gem.WindowEnd.Equal(wantReset) {
		t.Fatalf("gem window end = %v, want %v", windows.Gem.WindowEnd, wantReset)
	}
	if windows.Cla.Used != 6000 {
		t.Fatalf("cla used = %d, want 6000 basis points (remainingPercentage 40)", windows.Cla.Used)
	}
	wantClaReset := time.UnixMilli(1_770_000_000_000)
	if !windows.Cla.WindowEnd.Equal(wantClaReset) {
		t.Fatalf("cla window end = %v, want %v (numeric-seconds resetTime)", windows.Cla.WindowEnd, wantClaReset)
	}
}

func TestDecodeQuotaFirstTierWins(t *testing.T) {
	payload := []byte(`{"models":{"gemini-3.7-flash":{"quotaInfoByTier":{
		"tier-b":{"remainingFraction":0.1},
		"tier-a":{"remainingFraction":0.9}}}}}`)
	windows, err := DecodeQuota(payload)
	if err != nil {
		t.Fatalf("DecodeQuota: %v", err)
	}
	if windows.Gem == nil {
		t.Fatal("gem window missing")
	}
	if windows.Gem.Used != 1000 {
		t.Fatalf("used = %d, want 1000 basis points from deterministic first tier", windows.Gem.Used)
	}
}

func TestDecodeQuotaClaudeFamilyAliases(t *testing.T) {
	payload := []byte(`{"models":{"claude-opus-4":{"quotaInfo":{"remainingPercentage":0}}},
		"gpt-oss-20b":{"quotaInfo":{"remainingPercentage":100}}}`)
	windows, err := DecodeQuota(payload)
	if err != nil {
		t.Fatalf("DecodeQuota: %v", err)
	}
	if windows.Gem != nil {
		t.Fatalf("gpt-oss must classify as cla family; gem = %+v", windows.Gem)
	}
	if windows.Cla == nil || windows.Cla.Used != 10000 {
		t.Fatalf("cla window = %+v, want used 10000 basis points from the first cla entry", windows.Cla)
	}
}

func TestDecodeQuotaMissingModels(t *testing.T) {
	if _, err := DecodeQuota([]byte(`{}`)); err == nil {
		t.Fatal("payload without models object must error")
	}
	if _, err := DecodeQuota([]byte(`{invalid`)); err == nil {
		t.Fatal("malformed payload must error")
	}
}

func TestDecodeQuotaPercentClamping(t *testing.T) {
	payload := []byte(`{"models":{"gemini-3.7-flash":{"quotaInfo":{"remainingPercentage":150}}}}`)
	windows, err := DecodeQuota(payload)
	if err != nil {
		t.Fatalf("DecodeQuota: %v", err)
	}
	if windows.Gem.Used != 0 {
		t.Fatalf("used = %d, want clamped 0", windows.Gem.Used)
	}
}
