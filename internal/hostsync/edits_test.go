package hostsync_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/deLiseLINO/prism/internal/config"
	"github.com/deLiseLINO/prism/internal/hostsync"
	"github.com/deLiseLINO/prism/internal/management"
)

func (c *hostCreds) secret(_ context.Context, id, _ string) ([]byte, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.store[id]
	return append([]byte(nil), s...), ok, nil
}

type machine struct {
	cfg   *config.Manager
	creds *hostCreds
	mgmt  *management.Server
}

// syncedMachine is a daemon that keeps a sync base, as the real one does.
func syncedMachine(t *testing.T) machine {
	t.Helper()
	cfg := openConfig(t)
	creds := &hostCreds{store: map[string][]byte{}}
	mgmt := management.New(nil, cfg, nil, nil, creds, nil, nil, nil, nil)
	mgmt.SetSyncBase(hostsync.NewBaseFile(filepath.Join(t.TempDir(), "sync-base.json")), creds.secret)
	return machine{cfg: cfg, creds: creds, mgmt: mgmt}
}

func newMain(t *testing.T) machine {
	t.Helper()
	m := syncedMachine(t)
	m.creds.store["edge"] = []byte("sk-main")
	if _, err := m.cfg.Update(mainDoc(map[string]config.Host{"box": {Address: "box.lan", DaemonPort: 4100}}), 0); err != nil {
		t.Fatal(err)
	}
	return m
}

func pusherFor(main, host machine, adopt bool) *hostsync.Pusher {
	opts := hostsync.Options{
		Config:    main.cfg,
		Secrets:   main.creds.secret,
		Transport: serve(host.mgmt.Handler()),
		Timeout:   5 * time.Second,
		Now:       time.Now,
	}
	if adopt {
		opts.Adopt = main.mgmt.AdoptHostEdits
	}
	return hostsync.NewPusher(opts)
}

func edit(t *testing.T, cfg *config.Manager, change func(*config.Document)) {
	t.Helper()
	snap := cfg.Get()
	doc := snap.Config
	change(&doc)
	if _, err := cfg.Update(doc, snap.Generation); err != nil {
		t.Fatal(err)
	}
}

func addRelay(doc *config.Document) {
	doc.Providers["relay"] = config.Provider{Wire: config.WireOpenAIChat, BaseURL: "http://relay.example/v1", APIKeyRef: "relay:default", Models: []string{"r"}}
	doc.Routes["default"] = "relay/r"
}

func TestHostEditsReachTheMainMachineAndOtherHosts(t *testing.T) {
	main, box := newMain(t), syncedMachine(t)
	pusher := pusherFor(main, box, true)
	pusher.PushAll(context.Background())

	box.creds.store["relay"] = []byte("sk-relay")
	edit(t, box.cfg, addRelay)
	edit(t, box.cfg, func(doc *config.Document) {
		p := doc.Providers["edge"]
		p.Models = []string{"m", "m2"}
		doc.Providers["edge"] = p
	})
	pusher.PushAll(context.Background())

	got := main.cfg.Get().Config
	relay, ok := got.Providers["relay"]
	if !ok || relay.BaseURL != "http://relay.example/v1" || string(main.creds.store["relay"]) != "sk-relay" {
		t.Fatalf("main did not take the provider added on the host: %+v, key %q", relay, main.creds.store["relay"])
	}
	if got.Routes["default"] != "relay/r" || len(got.Providers["edge"].Models) != 2 {
		t.Fatalf("main did not take the host's route and model edits: routes %+v edge %+v", got.Routes, got.Providers["edge"])
	}
	if got.Providers["edge"].APIKeyRef != "edge:default" || string(main.creds.store["edge"]) != "sk-main" {
		t.Fatalf("main lost its own key for a provider the host edited: %+v", got.Providers["edge"])
	}
	if _, kept := box.cfg.Get().Config.Providers["relay"]; !kept {
		t.Fatal("the push removed the provider added on the host")
	}
	if st := pusher.Status("box", got.Hosts["box"]); st.State != hostsync.StateSynced {
		t.Fatalf("box: %+v", st)
	}

	edit(t, box.cfg, func(doc *config.Document) {
		delete(doc.Providers, "relay")
		doc.Routes["default"] = "edge/m"
	})
	pusher.PushAll(context.Background())
	if _, kept := main.cfg.Get().Config.Providers["relay"]; kept {
		t.Fatal("main kept a provider the host removed")
	}
	if _, kept := main.creds.store["relay"]; kept {
		t.Fatal("main kept the key of a provider the host removed")
	}

	before := main.cfg.Get().Generation
	pusher.PushAll(context.Background())
	if after := main.cfg.Get().Generation; after != before {
		t.Fatalf("a host without new edits changed the main config: generation %d -> %d", before, after)
	}
}

func TestHostEditsSurviveAPushThatDoesNotTakeThem(t *testing.T) {
	main, box := newMain(t), syncedMachine(t)
	pusher := pusherFor(main, box, false)
	pusher.PushAll(context.Background())

	box.creds.store["relay"] = []byte("sk-relay")
	edit(t, box.cfg, addRelay)
	edit(t, box.cfg, func(doc *config.Document) {
		p := doc.Providers["edge"]
		p.BaseURL = "http://edge-box.example/v1"
		doc.Providers["edge"] = p
	})
	box.creds.store["edge"] = []byte("sk-box")
	edit(t, main.cfg, func(doc *config.Document) {
		p := doc.Providers["edge"]
		p.BaseURL = "http://edge-main.example/v1"
		doc.Providers["edge"] = p
	})
	pusher.PushAll(context.Background())

	got := box.cfg.Get().Config
	if _, kept := got.Providers["relay"]; !kept || got.Routes["default"] != "relay/r" || string(box.creds.store["relay"]) != "sk-relay" {
		t.Fatalf("the push overwrote edits the main machine left alone: %+v routes %+v", got.Providers, got.Routes)
	}
	if got.Providers["edge"].BaseURL != "http://edge-main.example/v1" {
		t.Fatalf("an entry both machines changed must take the main value, got %q", got.Providers["edge"].BaseURL)
	}
	if string(box.creds.store["edge"]) != "sk-box" {
		t.Fatalf("the push replaced a key set on the host with the same main key, got %q", box.creds.store["edge"])
	}
}
