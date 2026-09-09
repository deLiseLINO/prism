package tui

import (
	"context"
	"fmt"
	"sync"

	"prism/internal/integrations"
	"prism/internal/management"
)

type fakeClient struct {
	mu sync.Mutex

	accounts []management.Account
	usage    management.UsageResponse
	quota    map[string]management.QuotaView
	quotaErr map[string]error

	paused   []string
	resumed  []string
	deleted  []string
	versions map[string]uint64

	authStart    map[string]management.AuthStartResponse
	authStartErr map[string]error
	authStatus   map[string]management.AuthStatusResponse

	integrations []integrations.Status
	applyResults map[string]integrations.ApplyResult
	applyErr     map[string]error

	providers    []management.Provider
	generation   uint64
	replacedID   string
	replacedBody management.ProviderWrite
}

func newFakeClient(accounts ...management.Account) *fakeClient {
	return &fakeClient{
		accounts:     append([]management.Account(nil), accounts...),
		quota:        make(map[string]management.QuotaView),
		quotaErr:     make(map[string]error),
		versions:     make(map[string]uint64),
		authStart:    make(map[string]management.AuthStartResponse),
		authStatus:   make(map[string]management.AuthStatusResponse),
		applyResults: make(map[string]integrations.ApplyResult),
		applyErr:     make(map[string]error),
	}
}

func (f *fakeClient) Usage(ctx context.Context) (management.UsageResponse, error) {
	return f.usage, nil
}

func (f *fakeClient) Accounts(ctx context.Context) (management.AccountsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return management.AccountsResponse{Accounts: append([]management.Account(nil), f.accounts...)}, nil
}

func (f *fakeClient) AccountQuota(ctx context.Context, id string) (management.QuotaResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.quotaErr[id]; ok {
		return management.QuotaResponse{}, err
	}
	quota, ok := f.quota[id]
	if !ok {
		return management.QuotaResponse{}, fmt.Errorf("no quota for %s", id)
	}
	return management.QuotaResponse{Account: id, Quota: quota}, nil
}

func (f *fakeClient) PauseAccount(ctx context.Context, id string, version uint64) (management.Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paused = append(f.paused, id)
	f.versions[id] = version
	for i := range f.accounts {
		if f.accounts[i].ID == id {
			f.accounts[i].State = "paused"
			return f.accounts[i], nil
		}
	}
	return management.Account{}, fmt.Errorf("unknown account %s", id)
}

func (f *fakeClient) ResumeAccount(ctx context.Context, id string, version uint64) (management.Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resumed = append(f.resumed, id)
	f.versions[id] = version
	for i := range f.accounts {
		if f.accounts[i].ID == id {
			f.accounts[i].State = "active"
			return f.accounts[i], nil
		}
	}
	return management.Account{}, fmt.Errorf("unknown account %s", id)
}

func (f *fakeClient) DeleteAccount(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, id)
	kept := make([]management.Account, 0, len(f.accounts))
	for _, account := range f.accounts {
		if account.ID != id {
			kept = append(kept, account)
		}
	}
	f.accounts = kept
	return nil
}

func (f *fakeClient) AuthStart(ctx context.Context, provider string) (management.AuthStartResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.authStartErr[provider]; ok {
		return management.AuthStartResponse{}, err
	}
	start, ok := f.authStart[provider]
	if !ok {
		return management.AuthStartResponse{}, fmt.Errorf("no auth start for %s", provider)
	}
	return start, nil
}

func (f *fakeClient) AuthStatus(ctx context.Context, provider, session string) (management.AuthStatusResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	status, ok := f.authStatus[session]
	if !ok {
		return management.AuthStatusResponse{}, fmt.Errorf("no auth status for session %s", session)
	}
	status.Provider = provider
	return status, nil
}

func (f *fakeClient) IntegrationsList(ctx context.Context) ([]integrations.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]integrations.Status(nil), f.integrations...), nil
}

func (f *fakeClient) IntegrationApply(ctx context.Context, id string) (integrations.ApplyResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.applyErr[id]; ok {
		return integrations.ApplyResult{}, err
	}
	result, ok := f.applyResults[id]
	if !ok {
		return integrations.ApplyResult{}, fmt.Errorf("no apply result for %s", id)
	}
	return result, nil
}

func (f *fakeClient) ProvidersList(ctx context.Context) (management.ProvidersResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return management.ProvidersResponse{Generation: f.generation, Providers: append([]management.Provider(nil), f.providers...)}, nil
}

func (f *fakeClient) ProvidersReplace(ctx context.Context, id string, w management.ProviderWrite) (management.ProviderMutationResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.providers {
		if f.providers[i].ID == id {
			if w.Pool != nil {
				pool := *w.Pool
				f.providers[i].Pool = &pool
			}
			f.replacedID = id
			f.replacedBody = w
			return management.ProviderMutationResponse{Generation: f.generation + 1, Provider: f.providers[i]}, nil
		}
	}
	return management.ProviderMutationResponse{}, fmt.Errorf("unknown provider %s", id)
}
