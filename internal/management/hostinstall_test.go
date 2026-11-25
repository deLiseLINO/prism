package management

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deLiseLINO/prism/internal/config"
	"github.com/deLiseLINO/prism/internal/hostinstall"
	"github.com/deLiseLINO/prism/internal/integrations"
)

type fakeHostInstaller struct{ port int }

func (f fakeHostInstaller) Install(_ context.Context, host, _ string, register func(port int) error) hostinstall.Reply {
	if err := register(f.port); err != nil {
		return hostinstall.Reply{Host: host, Phase: hostinstall.PhaseRegister, Reason: err.Error()}
	}
	return hostinstall.Reply{OK: true, Host: host, Version: "1.2.3"}
}

func remoteHostsEnv(t *testing.T, hosts map[string]config.Host, transport func(config.Host, *http.Request) (*http.Response, error)) (*httptest.Server, *config.Manager) {
	t.Helper()
	m, err := config.Open(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	doc := m.Get().Config
	doc.Hosts = hosts
	if _, err := m.Update(doc, m.Get().Generation); err != nil {
		t.Fatal(err)
	}
	srv := New(&fakePool{}, m, &fakeCatalog{}, &fakeQuotaSource{}, &fakeCreds{store: map[string][]byte{}}, &fakeAuth{}, integrations.NewRegistry(), nil, nil)
	srv.SetHostInstaller(context.Background(), fakeHostInstaller{port: 10200}, func(_ context.Context, host config.Host, req *http.Request) (*http.Response, error) {
		return transport(host, req)
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, m
}

func TestHostHeaderForwardsToThatHostsDaemon(t *testing.T) {
	var got *http.Request
	var gotBody string
	ts, _ := remoteHostsEnv(t, map[string]config.Host{
		"box": {Address: "user@box", DaemonPort: 18802},
		"new": {Address: "user@new"},
	}, func(host config.Host, req *http.Request) (*http.Response, error) {
		got = req
		b, _ := io.ReadAll(req.Body)
		gotBody = string(b)
		if host.Address != "user@box" {
			t.Errorf("forwarded to %q", host.Address)
		}
		return &http.Response{StatusCode: http.StatusCreated, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"remote":true}`))}, nil
	})

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/providers?expectedGeneration=3", strings.NewReader(`{"id":"p"}`))
	req.Header.Set(HostHeader, "box")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusCreated || string(body) != `{"remote":true}` {
		t.Fatalf("reply %d %s", res.StatusCode, body)
	}
	if got.Method != http.MethodPost || got.URL.String() != "http://127.0.0.1:18802/api/v1/providers?expectedGeneration=3" || gotBody != `{"id":"p"}` {
		t.Fatalf("forwarded %s %s %q", got.Method, got.URL, gotBody)
	}

	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/api/v1/hosts", nil)
	req.Header.Set(HostHeader, "new")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("a host without prism must not be forwarded to, got %d", res.StatusCode)
	}
}

func TestHostInstallRunsInBackgroundAndRegistersThePort(t *testing.T) {
	ts, m := remoteHostsEnv(t, map[string]config.Host{"box": {Address: "user@box"}}, nil)
	res, err := http.Post(ts.URL+"/api/v1/hosts/box/install", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("start: status %d", res.StatusCode)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var status HostInstallStatus
		res, err := http.Get(ts.URL + "/api/v1/hosts/box/install")
		if err != nil {
			t.Fatal(err)
		}
		err = json.NewDecoder(res.Body).Decode(&status)
		res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if !status.Running {
			if status.Reply == nil || !status.Reply.OK || status.Reply.Version != "1.2.3" {
				t.Fatalf("finished with %+v", status.Reply)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("install did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if port := m.Get().Config.Hosts["box"].DaemonPort; port != 10200 {
		t.Fatalf("daemon port %d, want 10200", port)
	}
}
