package hostsync_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deLiseLINO/prism/internal/config"
	"github.com/deLiseLINO/prism/internal/hostsync"
)

func loginDoc(pin string, catalogs ...config.ModelCatalog) config.Document {
	p := config.Provider{Wire: config.WireAntigravity, Models: []string{"m"}, ModelCatalogs: catalogs}
	if pin != "" {
		p.Pool = &config.PoolSettings{PinnedAccount: pin}
	}
	return config.Document{Version: config.SchemaVersion, Providers: map[string]config.Provider{"ag": p}}
}

func TestMergeTakesTheMainPinUnlessTheHostPinnedItsOwnAccount(t *testing.T) {
	incoming := loginDoc("ag:main")
	none := func(string) []string { return nil }
	own := func(string) []string { return []string{"ag:here"} }
	for _, tc := range []struct {
		name     string
		localPin string
		owned    hostsync.Owned
		want     string
	}{
		{"no host pin", "", none, "ag:main"},
		{"host pinned a replica", "ag:old", none, "ag:main"},
		{"host pinned its own account", "ag:here", own, "ag:here"},
		{"host owns an account it did not pin", "", own, "ag:main"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := hostsync.Merge(loginDoc(tc.localPin), incoming, tc.owned).Providers["ag"]
			pin := ""
			if got.Pool != nil {
				pin = got.Pool.PinnedAccount
			}
			if pin != tc.want {
				t.Fatalf("pin = %q, want %q", pin, tc.want)
			}
		})
	}
}

func TestMergeKeepsALocalSingleAccountLoginOverTheReplica(t *testing.T) {
	cline := func(pin string) config.Document {
		p := config.Provider{Wire: config.WireCline, BaseURL: "http://cline.example", Models: []string{"m"}}
		if pin != "" {
			p.Pool = &config.PoolSettings{PinnedAccount: pin}
		}
		return config.Document{Version: config.SchemaVersion, Providers: map[string]config.Provider{"cline": p}}
	}
	got := hostsync.Merge(cline(""), cline("cline:main"), func(string) []string { return []string{"cline:here"} }).Providers["cline"]
	if got.Pool != nil && got.Pool.PinnedAccount != "" {
		t.Fatalf("the main pin replaced a local cline login: %+v", got.Pool)
	}
}

func TestMergeTakesTheMainCatalogsOfReplicaAccounts(t *testing.T) {
	mainCatalog := config.ModelCatalog{BaseURL: "https://ag.example", Account: "ag:main", Project: "p-main", RawModels: []string{"raw-main"}}
	staleReplica := config.ModelCatalog{BaseURL: "https://ag.example", Account: "ag:main", Project: "p-old", RawModels: []string{"raw-old"}}
	ownCatalog := config.ModelCatalog{BaseURL: "https://ag.example", Account: "ag:here", Project: "p-here", RawModels: []string{"raw-here"}}
	pushedOwn := config.ModelCatalog{BaseURL: "https://ag.example", Account: "ag:here", Project: "p-main-view", RawModels: []string{"raw-x"}}
	got := hostsync.Merge(loginDoc("", staleReplica, ownCatalog), loginDoc("", mainCatalog, pushedOwn), func(string) []string { return []string{"ag:here"} }).Providers["ag"]
	want := []config.ModelCatalog{mainCatalog, ownCatalog}
	if len(got.ModelCatalogs) != 2 || !slices.EqualFunc(got.ModelCatalogs, want, func(a, b config.ModelCatalog) bool {
		return a.Account == b.Account && a.Project == b.Project
	}) {
		t.Fatalf("catalogs = %+v, want %+v", got.ModelCatalogs, want)
	}
}

func TestBaseKeepsNoAccountToken(t *testing.T) {
	path := t.TempDir() + "/sync-base.json"
	bundle := hostsync.Bundle{
		Document: mainDoc(nil),
		Secrets:  map[string]string{"edge": "sk-plain"},
		Accounts: []hostsync.Replica{{Provider: "codex", ID: "codex:a", State: hostsync.ReplicaActive, Credential: json.RawMessage(`{"version":1,"access":"at-plain","refresh":"rt-plain"}`)}},
	}
	if err := hostsync.NewBaseFile(path).Save(hostsync.NewBase(bundle)); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"sk-plain", "at-plain", "rt-plain"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("sync base holds %q in plain text: %s", secret, raw)
		}
	}
}

type accountHost struct {
	mu       sync.Mutex
	bundles  []hostsync.Bundle
	rejected []hostsync.RejectedToken
}

func (h *accountHost) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch r.URL.Path {
	case hostsync.EditsPath:
		json.NewEncoder(w).Encode(hostsync.Edits{RejectedTokens: h.rejected})
	case hostsync.ConfigPath:
		var b hostsync.Bundle
		json.NewDecoder(r.Body).Decode(&b)
		h.bundles = append(h.bundles, b)
		json.NewEncoder(w).Encode(hostsync.ApplyResponse{Changed: true})
	}
}

func (h *accountHost) pushes() []hostsync.Bundle {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.bundles)
}

func TestPushRenewsReportedTokensBeforeSendingReplicas(t *testing.T) {
	main := openConfig(t)
	if _, err := main.Update(mainDoc(map[string]config.Host{"box": {Address: "box.lan", DaemonPort: 4100}}), 0); err != nil {
		t.Fatal(err)
	}
	report := hostsync.RejectedToken{Provider: "codex", Account: "codex:a", Hash: hostsync.TokenHash("at-1")}
	host := &accountHost{rejected: []hostsync.RejectedToken{report}}
	access := "at-1"
	var renewed []hostsync.RejectedToken
	pusher := hostsync.NewPusher(hostsync.Options{
		Config:    main,
		Secrets:   mainSecrets,
		Transport: serve(host),
		Timeout:   5 * time.Second,
		Now:       time.Now,
		Renew: func(_ context.Context, id string, rejected []hostsync.RejectedToken) error {
			if id != "box" {
				t.Errorf("renew for host %q", id)
			}
			renewed = append(renewed, rejected...)
			access = "at-2"
			return nil
		},
		Accounts: func(context.Context, config.Document) ([]hostsync.Replica, error) {
			return []hostsync.Replica{{Provider: "codex", ID: "codex:a", State: hostsync.ReplicaActive, Credential: json.RawMessage(`{"version":1,"access":"` + access + `"}`)}}, nil
		},
	})
	pusher.PushAll(context.Background())
	if len(renewed) != 1 || renewed[0] != report {
		t.Fatalf("renewed %+v, want the reported token", renewed)
	}
	pushes := host.pushes()
	if len(pushes) != 1 || len(pushes[0].Accounts) != 1 || string(pushes[0].Accounts[0].Credential) != `{"version":1,"access":"at-2"}` {
		t.Fatalf("push did not carry the renewed credential: %+v", pushes)
	}
	if st := pusher.Status("box", main.Get().Config.Hosts["box"]); st.State != hostsync.StateSynced {
		t.Fatalf("status %+v", st)
	}
}

func TestRunPushesAfterACredentialChange(t *testing.T) {
	main := openConfig(t)
	if _, err := main.Update(mainDoc(map[string]config.Host{"box": {Address: "box.lan", DaemonPort: 4100}}), 0); err != nil {
		t.Fatal(err)
	}
	host := &accountHost{}
	credentials := make(chan struct{}, 1)
	pusher := hostsync.NewPusher(hostsync.Options{
		Config:    main,
		Secrets:   mainSecrets,
		Transport: serve(host),
		Interval:  time.Hour,
		Settle:    10 * time.Millisecond,
		Timeout:   5 * time.Second,
		Now:       time.Now,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pusher.Run(ctx, nil, credentials)
	waitFor(t, func() bool { return len(host.pushes()) == 1 })
	credentials <- struct{}{}
	waitFor(t, func() bool { return len(host.pushes()) == 2 })
}
