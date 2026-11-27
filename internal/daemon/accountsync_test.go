package daemon

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deLiseLINO/prism/internal/account"
	"github.com/deLiseLINO/prism/internal/auth"
	"github.com/deLiseLINO/prism/internal/config"
	"github.com/deLiseLINO/prism/internal/hostsync"
	"github.com/deLiseLINO/prism/internal/integrations"
	"github.com/deLiseLINO/prism/internal/management"
	"github.com/deLiseLINO/prism/internal/server"
)

type gateway struct {
	mu      sync.Mutex
	valid   map[string]bool
	next    string
	refresh atomic.Int32
	seen    []string
}

func (g *gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/token" {
		g.refresh.Add(1)
		g.valid[g.next] = true
		fmt.Fprintf(w, `{"access_token":%q,"refresh_token":"grant-%d","expires_in":3600}`, g.next, g.refresh.Load()+1)
		return
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	g.seen = append(g.seen, token)
	if !g.valid[token] {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"token revoked"}}`)
		return
	}
	fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
}

func (g *gateway) allow(tokens ...string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.valid = map[string]bool{}
	for _, tok := range tokens {
		g.valid[tok] = true
	}
}

// syncedPair is a main machine and one host wired through the real sync
// handlers, with the host serving inference.
type syncedPair struct {
	main      *daemonEnv
	mainCfg   *config.Manager
	mainSync  *accountSync
	mainSink  *auth.FileSink
	host      *daemonEnv
	hostCfg   *config.Manager
	hostMgmt  http.Handler
	hostInfer http.Handler
	pusher    *hostsync.Pusher
}

func newSyncedPair(t *testing.T, up *httptest.Server, wire config.Wire) *syncedPair {
	t.Helper()
	flow := func() map[account.ProviderID]auth.Flow {
		return map[account.ProviderID]auth.Flow{"edge": mustCodexFlow(t, up)}
	}
	provider := config.Provider{Wire: wire, BaseURL: up.URL, Models: []string{"model"}}
	mainEnv, mainCfg := testEnv(t, config.Document{
		Version:   config.SchemaVersion,
		Providers: map[string]config.Provider{"edge": provider},
		Hosts:     map[string]config.Host{"box": {Address: "box.lan", DaemonPort: 4100}},
	})
	mainPool := mainEnv.pool.(replicaPool)
	mainRef, err := auth.NewRefresher(auth.RefresherOptions{File: mainEnv.creds.file, Repos: mainEnv.repos, Pool: mainEnv.pool.(auth.RefreshPool), Flows: flow()})
	if err != nil {
		t.Fatal(err)
	}
	mainEnv.refresher = mainRef
	mainEnv.ensureRepo("edge", nil)
	mainSync := newAccountSync(mainEnv, mainPool)

	hostEnv, hostCfg := testEnv(t, config.Document{Version: config.SchemaVersion})
	hostPool := hostEnv.pool.(replicaPool)
	hostPool.(interface {
		SetPolicyWriter(func(account.Account) error)
	}).SetPolicyWriter(func(a account.Account) error { return hostEnv.repos[a.Provider].SavePolicy(a) })
	hostSync := newAccountSync(hostEnv, hostPool)
	hostRef, err := auth.NewRefresher(auth.RefresherOptions{File: hostEnv.creds.file, Repos: hostEnv.repos, Pool: hostEnv.pool.(auth.RefreshPool), Flows: flow(), ReplicaRejected: hostSync.noteRejected})
	if err != nil {
		t.Fatal(err)
	}
	hostEnv.refresher = hostRef
	mgmt := management.New(hostPool, hostCfg, nil, nil, hostEnv.creds, nil, integrations.NewRegistry(), nil, nil)
	mgmt.SetAccountReplicas(hostSync)
	hostMgmt := mgmt.Handler()
	p := &syncedPair{
		main:      mainEnv,
		mainCfg:   mainCfg,
		mainSync:  mainSync,
		mainSink:  auth.NewFileSink(mainEnv.creds.file, mainEnv.repos, mainPool),
		host:      hostEnv,
		hostCfg:   hostCfg,
		hostMgmt:  hostMgmt,
		hostInfer: server.New(server.Options{Planner: server.NewConfigPlanner(hostCfg), Registry: hostEnv.registry, Pool: hostPool, Config: hostCfg}).Handler(),
	}
	p.pusher = hostsync.NewPusher(hostsync.Options{
		Config:  mainCfg,
		Secrets: func(context.Context, string, string) ([]byte, bool, error) { return nil, false, nil },
		Transport: func(_ context.Context, _ config.Host, req *http.Request) (*http.Response, error) {
			rec := httptest.NewRecorder()
			hostMgmt.ServeHTTP(rec, req)
			return rec.Result(), nil
		},
		Timeout:  5 * time.Second,
		Now:      time.Now,
		Accounts: mainSync.Replicas,
		Renew:    mainSync.Renew,
	})
	return p
}

func (p *syncedPair) login(t *testing.T, identity, access string) account.Account {
	t.Helper()
	a, err := p.mainSink.Persist(context.Background(), "edge", account.Credential{AccountID: identity, Access: access, Refresh: "grant-1", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	p.mainSink.Register(a)
	return a
}

func (p *syncedPair) push(t *testing.T) {
	t.Helper()
	p.pusher.PushAll(context.Background())
	if st := p.pusher.Status("box", p.mainCfg.Get().Config.Hosts["box"]); st.State != hostsync.StateSynced {
		t.Fatalf("push: %+v", st)
	}
}

func (p *syncedPair) hostCredential(t *testing.T, id account.AccountID) (account.Credential, account.CredentialGeneration) {
	t.Helper()
	repo := p.host.repos["edge"]
	gen, err := repo.CurrentGeneration("edge", id)
	if err != nil || gen == 0 {
		t.Fatalf("host has no credential for %s: gen %d err %v", id, gen, err)
	}
	blob, ok, err := p.host.creds.file.Get(context.Background(), "edge", id, gen)
	if err != nil || !ok {
		t.Fatalf("host credential blob of %s missing: %v", id, err)
	}
	cred, err := account.ParseCredential(blob)
	if err != nil {
		t.Fatal(err)
	}
	return cred, gen
}

func (p *syncedPair) hostAccount(id account.AccountID) (account.Account, bool) {
	for _, a := range p.host.pool.Snapshot().Accounts {
		if a.ID == id {
			return a, true
		}
	}
	return account.Account{}, false
}

func TestHostServesAPushedReplicaWithoutItsRefreshGrant(t *testing.T) {
	g := &gateway{}
	g.allow("main-1")
	up := httptest.NewServer(g)
	defer up.Close()
	p := newSyncedPair(t, up, config.WireCline)
	a := p.login(t, "identity", "main-1")

	p.push(t)

	if rec := credentialInfer(p.hostInfer); rec.Code != http.StatusOK {
		t.Fatalf("host request: status %d body %s", rec.Code, rec.Body.String())
	}
	cred, gen := p.hostCredential(t, a.ID)
	if cred.Access != "main-1" || cred.Refresh != "" || gen != 1 {
		t.Fatalf("host credential = %+v at gen %d, want the access token only", cred, gen)
	}
	if replica, _ := p.host.repos["edge"].IsReplica("edge", a.ID); !replica {
		t.Fatal("pushed account is not marked as a replica")
	}

	p.push(t)
	if _, again := p.hostCredential(t, a.ID); again != gen {
		t.Fatalf("an unchanged push moved the host generation from %d to %d", gen, again)
	}
}

func TestRejectedReplicaWaitsForTheMainMachineAndRecoversAfterItRenews(t *testing.T) {
	g := &gateway{next: "main-2"}
	g.allow("main-1")
	up := httptest.NewServer(g)
	defer up.Close()
	p := newSyncedPair(t, up, config.WireCline)
	a := p.login(t, "identity", "main-1")
	p.push(t)

	g.allow()
	rec := credentialInfer(p.hostInfer)
	if rec.Code == http.StatusOK || !strings.Contains(rec.Body.String(), "waiting for fresh credentials from the main machine") {
		t.Fatalf("rejected replica: status %d body %s", rec.Code, rec.Body.String())
	}
	if g.refresh.Load() != 0 {
		t.Fatal("the host spent the refresh grant")
	}
	if cred, gen := p.hostCredential(t, a.ID); cred.Access != "main-1" || gen != 1 {
		t.Fatalf("the rejected replica was rewritten: %+v gen %d", cred, gen)
	}
	if acct, ok := p.hostAccount(a.ID); !ok || acct.State != account.Active {
		t.Fatalf("the rejected replica left the pool or needs re-login: %+v %v", acct, ok)
	}
	rejected := p.host.repos["edge"]
	rows, err := rejected.Load("edge")
	if err != nil || len(rows) != 1 || rows[0].State != account.Active {
		t.Fatalf("the host wrote a re-login state for the replica: %+v %v", rows, err)
	}

	p.push(t)

	if g.refresh.Load() != 1 {
		t.Fatalf("main renewed %d times, want once after the report", g.refresh.Load())
	}
	if cred, _ := p.hostCredential(t, a.ID); cred.Access != "main-2" {
		t.Fatalf("host did not get the renewed token: %+v", cred)
	}
	if rec := credentialInfer(p.hostInfer); rec.Code != http.StatusOK {
		t.Fatalf("host request after renewal: status %d body %s", rec.Code, rec.Body.String())
	}
	p.push(t)
	if g.refresh.Load() != 1 {
		t.Fatal("a stale report renewed the credential again")
	}
}

func TestReLoginOnTheMainMachineWinsOnTheHost(t *testing.T) {
	for _, pushBetween := range []bool{false, true} {
		t.Run(fmt.Sprintf("push-between=%t", pushBetween), func(t *testing.T) {
			g := &gateway{}
			g.allow("first", "second")
			up := httptest.NewServer(g)
			defer up.Close()
			p := newSyncedPair(t, up, config.WireCline)
			a := p.login(t, "identity", "first")
			p.push(t)
			p.push(t)

			if err := p.main.pool.DeleteAccount(context.Background(), a.ID); err != nil {
				t.Fatal(err)
			}
			if err := (durableAccountStore{pool: p.main.pool, file: p.main.creds.file, repos: p.main.repos}).DeleteDurable(context.Background(), a.ID); err != nil {
				t.Fatal(err)
			}
			if pushBetween {
				p.push(t)
				if _, ok := p.hostAccount(a.ID); ok {
					t.Fatal("the host kept a replica the main machine deleted")
				}
				if rows, _ := p.host.repos["edge"].Load("edge"); len(rows) != 0 {
					t.Fatalf("the deleted replica stayed on disk: %+v", rows)
				}
			}
			again := p.login(t, "identity", "second")
			if again.CredGen != 1 {
				t.Fatalf("main generation after re-login = %d, want a restart at 1", again.CredGen)
			}
			p.push(t)

			if cred, _ := p.hostCredential(t, a.ID); cred.Access != "second" {
				t.Fatalf("host kept the old login: %+v", cred)
			}
			g.mu.Lock()
			g.seen = nil
			g.mu.Unlock()
			if rec := credentialInfer(p.hostInfer); rec.Code != http.StatusOK {
				t.Fatalf("host request: status %d body %s", rec.Code, rec.Body.String())
			}
			if len(g.seen) != 1 || g.seen[0] != "second" {
				t.Fatalf("host sent %v, want the new login", g.seen)
			}
		})
	}
}

func TestHostLoginIsNeverReplacedByAReplica(t *testing.T) {
	g := &gateway{}
	g.allow("main-1", "host-1")
	up := httptest.NewServer(g)
	defer up.Close()
	p := newSyncedPair(t, up, config.WireCline)
	p.login(t, "identity", "main-1")
	p.push(t)
	if _, ok := p.hostAccount("edge:identity"); !ok {
		t.Fatal("replica missing before the host login")
	}
	hostSink := auth.NewFileSink(p.host.creds.file, p.host.repos, p.host.pool)
	local, err := hostSink.Persist(context.Background(), "edge", account.Credential{AccountID: "local", Access: "host-1", Refresh: "host-grant", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	hostSink.Register(local)

	p.push(t)

	if _, ok := p.hostAccount("edge:identity"); ok {
		t.Fatal("a cline replica stayed beside the host's own login")
	}
	if cred, _ := p.hostCredential(t, local.ID); cred.Refresh != "host-grant" {
		t.Fatalf("the host login was rewritten: %+v", cred)
	}
	if rec := credentialInfer(p.hostInfer); rec.Code != http.StatusOK {
		t.Fatalf("host request: status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestReplicaStateFollowsTheMainMachine(t *testing.T) {
	g := &gateway{}
	g.allow("main-1")
	up := httptest.NewServer(g)
	defer up.Close()
	p := newSyncedPair(t, up, config.WireCline)
	a := p.login(t, "identity", "main-1")
	p.push(t)

	cur := a
	for _, acct := range p.main.pool.Snapshot().Accounts {
		if acct.ID == a.ID {
			cur = acct
		}
	}
	p.main.pool.(interface {
		SetPolicyWriter(func(account.Account) error)
	}).SetPolicyWriter(func(a account.Account) error { return p.main.repos[a.Provider].SavePolicy(a) })
	if err := p.main.pool.Pause(context.Background(), a.ID, cur.Version); err != nil {
		t.Fatal(err)
	}
	p.push(t)
	if acct, _ := p.hostAccount(a.ID); acct.State != account.Paused {
		t.Fatalf("host replica state = %v, want paused", acct.State)
	}
	for _, acct := range p.main.pool.Snapshot().Accounts {
		if acct.ID == a.ID {
			cur = acct
		}
	}
	if err := p.main.pool.Resume(context.Background(), a.ID, cur.Version); err != nil {
		t.Fatal(err)
	}
	p.push(t)
	if acct, _ := p.hostAccount(a.ID); acct.State != account.Active {
		t.Fatalf("host replica state = %v, want active", acct.State)
	}
}

func TestReplicasCarryTheRefreshGrantOnlyWhereItDoesNotRotate(t *testing.T) {
	env, _ := testEnv(t, config.Document{Version: config.SchemaVersion, Providers: map[string]config.Provider{
		"ag":    {Wire: config.WireAntigravity, Models: []string{"m"}},
		"codex": {Wire: config.WireCodex, Models: []string{"m"}},
	}})
	sink := auth.NewFileSink(env.creds.file, env.repos, env.pool)
	for _, id := range []account.ProviderID{"ag", "codex"} {
		env.ensureRepo(id, nil)
		if _, err := sink.Persist(context.Background(), id, account.Credential{AccountID: "who", Access: "at", Refresh: "rt", ProjectID: "proj", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	replicas, err := newAccountSync(env, env.pool.(replicaPool)).Replicas(context.Background(), env.cfg.Get().Config)
	if err != nil || len(replicas) != 2 {
		t.Fatalf("replicas %+v err %v", replicas, err)
	}
	for _, r := range replicas {
		cred, err := account.ParseCredential(r.Credential)
		if err != nil {
			t.Fatal(err)
		}
		wantRefresh := map[string]string{"ag": "rt", "codex": ""}[r.Provider]
		if cred.Refresh != wantRefresh || cred.Access != "at" || cred.ProjectID != "proj" {
			t.Fatalf("%s replica credential = %+v", r.Provider, cred)
		}
	}
}

func TestMainRenewsPushedCredentialsBeforeHostsLoseThem(t *testing.T) {
	g := &gateway{next: "renewed"}
	g.allow()
	up := httptest.NewServer(g)
	defer up.Close()
	p := newSyncedPair(t, up, config.WireCline)
	if _, err := p.mainSink.Persist(context.Background(), "edge", account.Credential{AccountID: "identity", Access: "soon", Refresh: "grant-1", ExpiresAt: time.Now().Add(3 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	published := make(chan struct{}, 1)
	ref, err := auth.NewRefresher(auth.RefresherOptions{File: p.main.creds.file, Repos: p.main.repos, Pool: p.main.pool.(auth.RefreshPool), Flows: map[account.ProviderID]auth.Flow{"edge": mustCodexFlow(t, up)},
		Published: func(account.ProviderID, account.AccountID) { published <- struct{}{} }})
	if err != nil {
		t.Fatal(err)
	}
	p.main.refresher = ref

	p.mainSync.renewDue(context.Background(), time.Minute)
	if g.refresh.Load() != 0 {
		t.Fatal("renewed a credential outside the window")
	}
	p.mainSync.renewDue(context.Background(), 2*pushInterval+renewAheadMargin)
	if g.refresh.Load() != 1 {
		t.Fatalf("renewals = %d, want one inside the window", g.refresh.Load())
	}
	select {
	case <-published:
	default:
		t.Fatal("the renewal did not signal a push")
	}

	doc := p.mainCfg.Get().Config
	doc.Hosts = nil
	if _, err := p.mainCfg.Update(doc, p.mainCfg.Get().Generation); err != nil {
		t.Fatal(err)
	}
	g.next = "again"
	p.mainSync.renewDue(context.Background(), 2*time.Hour)
	if g.refresh.Load() != 1 {
		t.Fatal("renewed ahead with no host taking pushes")
	}
}

func mustCodexFlow(t *testing.T, up *httptest.Server) auth.Flow {
	t.Helper()
	cfg := auth.CodexProduction
	cfg.TokenURL = up.URL + "/token"
	f, err := auth.NewCodexFlow(cfg, auth.Options{HTTP: up.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return f
}
