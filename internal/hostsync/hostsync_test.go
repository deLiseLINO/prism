package hostsync_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deLiseLINO/prism/internal/config"
	"github.com/deLiseLINO/prism/internal/hostsync"
	"github.com/deLiseLINO/prism/internal/management"
)

// hostCreds is the host-side credential store: staged keys become live on
// commit, like the daemon's store.
type hostCreds struct {
	mu    sync.Mutex
	store map[string][]byte
}

func (c *hostCreds) Stage(_ context.Context, id, _ string, secret []byte) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.store[id] = append([]byte(nil), secret...)
	return id + ":default", nil
}

func (c *hostCreds) Committed(context.Context, string, config.Provider, config.Provider) error {
	return nil
}

func (c *hostCreds) Delete(_ context.Context, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.store, id)
	return nil
}

func (c *hostCreds) Configured(_ context.Context, id, _ string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.store[id]
	return ok, nil
}

func openConfig(t *testing.T) *config.Manager {
	t.Helper()
	m, err := config.Open(filepath.Join(t.TempDir(), "prism.json"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// hostDaemon is a host's management API with its own config and keys.
func hostDaemon(t *testing.T) (*config.Manager, *hostCreds, http.Handler) {
	t.Helper()
	cfg := openConfig(t)
	creds := &hostCreds{store: map[string][]byte{}}
	return cfg, creds, management.New(nil, cfg, nil, nil, creds, nil, nil, nil, nil).Handler()
}

func serve(handler http.Handler) hostsync.Transport {
	return func(_ context.Context, _ config.Host, req *http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Result(), nil
	}
}

func mainDoc(hosts map[string]config.Host) config.Document {
	return config.Document{
		Version: config.SchemaVersion,
		Daemon:  config.Daemon{Listen: "127.0.0.1:4000"},
		Providers: map[string]config.Provider{
			"edge": {Wire: config.WireOpenAIChat, BaseURL: "http://edge.example/v1", APIKeyRef: "edge:default", Models: []string{"m"}},
		},
		Routes: map[string]string{"default": "edge/m"},
		Hosts:  hosts,
	}
}

func mainSecrets(_ context.Context, provider, ref string) ([]byte, bool, error) {
	if provider == "edge" && ref == "edge:default" {
		return []byte("sk-main"), true, nil
	}
	return nil, false, nil
}

func TestPushAllSyncsInstalledHostsAndReportsEachOutcome(t *testing.T) {
	off := false
	main := openConfig(t)
	if _, err := main.Update(mainDoc(map[string]config.Host{
		"box":   {Address: "box.lan", DaemonPort: 4100},
		"dead":  {Address: "dead.lan", DaemonPort: 4100},
		"old":   {Address: "old.lan", DaemonPort: 4100},
		"fresh": {Address: "fresh.lan"},
		"quiet": {Address: "quiet.lan", DaemonPort: 4100, AutoSync: &off},
	}), 0); err != nil {
		t.Fatal(err)
	}
	boxCfg, boxCreds, box := hostDaemon(t)
	routes := map[string]hostsync.Transport{
		"box.lan": serve(box),
		"dead.lan": func(context.Context, config.Host, *http.Request) (*http.Response, error) {
			return nil, errors.New("ssh: connect to host dead.lan port 22: Connection refused")
		},
		"old.lan": serve(http.NewServeMux()),
	}
	var mu sync.Mutex
	var called []string
	pusher := hostsync.NewPusher(hostsync.Options{
		Config:  main,
		Secrets: mainSecrets,
		Transport: func(ctx context.Context, host config.Host, req *http.Request) (*http.Response, error) {
			mu.Lock()
			called = append(called, host.Address)
			mu.Unlock()
			route, ok := routes[host.Address]
			if !ok {
				t.Errorf("pushed to %s, which is not installed or has auto sync off", host.Address)
				return nil, errors.New("unexpected host")
			}
			return route(ctx, host, req)
		},
		Timeout: 5 * time.Second,
		Now:     func() time.Time { return time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC) },
	})

	pusher.PushAll(context.Background())

	hosts := main.Get().Config.Hosts
	if st := pusher.Status("box", hosts["box"]); st.State != hostsync.StateSynced || st.At != "2026-11-04T12:00:00Z" {
		t.Fatalf("box: %+v", st)
	}
	if st := pusher.Status("dead", hosts["dead"]); st.State != hostsync.StateFailed || !strings.Contains(st.Error, "Connection refused") {
		t.Fatalf("dead: %+v", st)
	}
	if st := pusher.Status("old", hosts["old"]); st.State != hostsync.StateFailed || !strings.Contains(st.Error, "too old") {
		t.Fatalf("old: %+v", st)
	}
	if st := pusher.Status("quiet", hosts["quiet"]); st.State != hostsync.StateOff {
		t.Fatalf("quiet: %+v", st)
	}
	if len(called) != 3 {
		t.Fatalf("pushed to %v, want only the three installed hosts with auto sync on", called)
	}
	got := boxCfg.Get().Config
	if got.Routes["default"] != "edge/m" || got.Providers["edge"].APIKeyRef != "edge:default" || string(boxCreds.store["edge"]) != "sk-main" {
		t.Fatalf("box did not get the main config and key: %+v, key %q", got, boxCreds.store["edge"])
	}
	if len(got.Hosts) != 0 || got.Daemon.Listen != "" {
		t.Fatalf("the main machine's host list and listen address leaked to the host: %+v %+v", got.Hosts, got.Daemon)
	}
}

func TestRunPushesAfterAConfigChange(t *testing.T) {
	main := openConfig(t)
	if _, err := main.Update(mainDoc(map[string]config.Host{"box": {Address: "box.lan", DaemonPort: 4100}}), 0); err != nil {
		t.Fatal(err)
	}
	boxCfg, _, box := hostDaemon(t)
	pusher := hostsync.NewPusher(hostsync.Options{
		Config:    main,
		Secrets:   mainSecrets,
		Transport: serve(box),
		Interval:  time.Hour,
		Settle:    10 * time.Millisecond,
		Timeout:   5 * time.Second,
		Now:       time.Now,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pusher.Run(ctx, main.Subscribe())
	waitFor(t, func() bool { return boxCfg.Get().Config.Routes["default"] == "edge/m" })

	snap := main.Get()
	doc := snap.Config
	doc.Routes["default"] = "edge/m2"
	p := doc.Providers["edge"]
	p.Models = []string{"m", "m2"}
	doc.Providers["edge"] = p
	if _, err := main.Update(doc, snap.Generation); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return boxCfg.Get().Config.Routes["default"] == "edge/m2" })
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met")
}

// TestMain lets the test binary stand in for ssh: run with PRISM_FAKE_SSH it
// pipes stdin and stdout to the -W target, as `ssh -W` does.
func TestMain(m *testing.M) {
	if os.Getenv("PRISM_FAKE_SSH") == "1" {
		fakeSSH()
		return
	}
	os.Exit(m.Run())
}

func fakeSSH() {
	var target string
	for i, arg := range os.Args {
		if arg == "-W" && i+1 < len(os.Args) {
			target = os.Args[i+1]
		}
	}
	conn, err := net.Dial("tcp", target)
	if err != nil {
		io.WriteString(os.Stderr, "channel 0: open failed: connect failed: "+err.Error()+"\n")
		os.Exit(255)
	}
	go func() {
		io.Copy(conn, os.Stdin)
		conn.(*net.TCPConn).CloseWrite()
	}()
	io.Copy(os.Stdout, conn)
}

func TestSSHTransportKeepsTheRequestAliveUntilTheResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		time.Sleep(100 * time.Millisecond)
		if r.Context().Err() != nil {
			t.Error("the daemon saw the request canceled before it answered")
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"echo": string(body)})
	}))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	t.Setenv("PRISM_FAKE_SSH", "1")
	transport := hostsync.SSHTransport(os.Args[0])

	req, err := http.NewRequest(http.MethodPut, "http://prism/x", strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := transport(context.Background(), config.Host{Address: "box.lan", DaemonPort: port}, req)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil || got["echo"] != "payload" {
		t.Fatalf("response %+v, err %v", got, err)
	}

	srv.Close()
	req, err = http.NewRequest(http.MethodPut, "http://prism/x", strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = transport(context.Background(), config.Host{Address: "box.lan", DaemonPort: port}, req)
	if err == nil || !strings.Contains(err.Error(), "connect failed") {
		t.Fatalf("a closed daemon port must surface ssh's error, got %v", err)
	}
}
