package tui

import (
	"time"

	"prism/internal/management"
)

type DataMsg struct {
	AccountKey string
	Windows    []quotaWindow
}

type ErrMsg struct {
	AccountKey string
	Err        error
}

type AccountsMsg struct {
	ActiveKey string
	Accounts  []management.Account
	Notice    string
}

type NoticeMsg struct {
	Text string
}

type NoticeTimeoutMsg struct {
	Seq int
}

type AuthStartedMsg struct {
	Provider string
	Session  string
	AuthURL  string
}

type AuthPendingMsg struct{}

type AuthFinishedMsg struct {
	Provider string
	Err      error
}

type AuthBrowserOpenResultMsg struct {
	Text string
	Err  error
}

type AuthCopyResultMsg struct {
	Text string
	Err  error
}

type PinnedAccountsMsg struct {
	Pinned map[string]string
}

type IntegrationsMsg struct {
	Host         string
	Integrations []integrationStatus
}

type HostsMsg struct {
	Hosts []hostView
}

type IntegrationApplyResultMsg struct {
	OK     bool
	ID     string
	Reason string
	Err    error
}

type StatsMsg struct {
	Range string
	Stats management.StatsResponse
}

type StatsErrMsg struct {
	Err error
}

type AnimationFrameMsg struct {
	Now time.Time
}

type AutoRefreshTickMsg struct {
	Now time.Time
}
