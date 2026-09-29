package messages

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCloseStopsPingBeforeHandlerReturns(t *testing.T) {
	prevEvery, prevIdle := pingEvery, pingIdleAfter
	pingEvery, pingIdleAfter = 5*time.Millisecond, 0
	t.Cleanup(func() { pingEvery, pingIdleAfter = prevEvery, prevIdle })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		eg := New(w, true)
		defer eg.Close()
		if err := eg.Begin(ResponseHeader{ID: "msg_1", Model: "m"}); err != nil {
			t.Errorf("Begin: %v", err)
		}
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	time.Sleep(100 * time.Millisecond)
}

func TestCloseBeforeBeginAndTwiceIsSafe(t *testing.T) {
	eg := New(httptest.NewRecorder(), true)
	eg.Close()
	eg.Close()
}
