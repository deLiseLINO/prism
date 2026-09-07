package tui

import (
	"testing"

	"prism/internal/management"
)

func TestExhaustedStates(t *testing.T) {
	limit := int64(10)
	cases := []struct {
		name    string
		account management.Account
		window  *management.QuotaView
		want    bool
	}{
		{
			name:    "active with quota left",
			account: management.Account{ID: "acc", State: "active"},
			window:  &management.QuotaView{Used: 2, Limit: &limit},
			want:    false,
		},
		{
			name:    "quota drained",
			account: management.Account{ID: "acc", State: "active"},
			window:  &management.QuotaView{Used: 10, Limit: &limit},
			want:    true,
		},
		{
			name:    "cooling down",
			account: management.Account{ID: "acc", State: "cooling_down"},
			want:    true,
		},
		{
			name:    "needs reauth",
			account: management.Account{ID: "acc", State: "needs_reauth"},
			want:    true,
		},
		{
			name:    "paused",
			account: management.Account{ID: "acc", State: "paused"},
			want:    true,
		},
		{
			name:    "loading not exhausted",
			account: management.Account{ID: "acc", State: "active"},
			want:    false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := testModel(nil, tc.account)
			m.LoadingMap = map[string]bool{"acc": tc.name == "loading not exhausted"}
			if tc.window != nil {
				m.UsageData["acc"] = quotaWindowsFromView(*tc.window)
			}
			if got := m.isCompactAccountExhausted("acc"); got != tc.want {
				t.Fatalf("exhausted = %v, want %v", got, tc.want)
			}
		})
	}
}
