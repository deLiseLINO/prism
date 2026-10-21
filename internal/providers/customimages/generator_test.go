package customimages

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/deLiseLINO/prism/internal/account"
	"github.com/deLiseLINO/prism/internal/provider"
)

func TestURLSitsBesideChatEndpoint(t *testing.T) {
	for base, want := range map[string]string{
		"http://gw.example":                     "http://gw.example/v1/images/generations",
		"http://gw.example/v1/":                 "http://gw.example/v1/images/generations",
		"http://gw.example/v1/chat/completions": "http://gw.example/v1/images/generations",
	} {
		if got := URL(base); got != want {
			t.Fatalf("URL(%q) = %q, want %q", base, got, want)
		}
	}
}

func TestClientCancelAbortsUpstream(t *testing.T) {
	aborted := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		<-r.Context().Done()
		close(aborted)
	}))
	defer upstream.Close()
	g := New(func(context.Context, provider.Target, account.Lease) (string, error) { return "", nil }, upstream.Client())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := g.GenerateImage(ctx, provider.ImageRequest{Target: provider.Target{Provider: "gw", BaseURL: upstream.URL}, Body: []byte(`{}`)})
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		var re provider.RunError
		if !errors.As(err, &re) || re.Kind != provider.UnsafeReplay {
			t.Fatalf("err = %v, want an unsafe-replay run error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("generation did not return after client cancel")
	}
	select {
	case <-aborted:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream request was not aborted")
	}
}
