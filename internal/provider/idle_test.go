package provider

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

func TestIdleBodyFailsOnSilence(t *testing.T) {
	pr, pw := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body := IdleBody(pr, 50*time.Millisecond, func() { cancel(); pw.CloseWithError(ctx.Err()) })
	buf := make([]byte, 8)
	if _, err := body.Read(buf); !errors.Is(err, ErrStreamIdle) {
		t.Fatalf("err = %v, want ErrStreamIdle", err)
	}
}

func TestIdleBodySurvivesSlowProgress(t *testing.T) {
	pr, pw := io.Pipe()
	body := IdleBody(pr, 100*time.Millisecond, func() { pw.Close() })
	go func() {
		for range 5 {
			time.Sleep(60 * time.Millisecond)
			pw.Write([]byte("x"))
		}
		pw.Close()
	}()
	got, err := io.ReadAll(body)
	if err != nil || string(got) != "xxxxx" {
		t.Fatalf("got %q err %v, want xxxxx nil", got, err)
	}
}
