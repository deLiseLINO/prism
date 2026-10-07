package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/deLiseLINO/prism/internal/account"
)

func TestStagedCredentialRevisionsPreserveLegacyAndRestart(t *testing.T) {
	root := t.TempDir()
	s := NewFileCredentialStore(root)
	ctx := context.Background()
	if err := s.Put(ctx, "edge", "edge:default", 1, []byte("original")); err != nil {
		t.Fatal(err)
	}
	ref, err := s.StageCustomDefaultSecret(ctx, "edge", "", []byte("replacement"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ref, "prism-key:") {
		t.Fatalf("reference=%q", ref)
	}
	reopened := NewFileCredentialStore(root)
	for _, tc := range []struct {
		ref, key   string
		generation account.CredentialGeneration
	}{{"", "original", 1}, {"edge:default", "original", 1}, {ref, "replacement", 2}} {
		got, ok, err := reopened.GetCustomDefaultSecret(ctx, "edge", tc.ref)
		gen, genErr := reopened.CustomDefaultGeneration(ctx, "edge", tc.ref)
		if err != nil || !ok || string(got) != tc.key || genErr != nil || gen != tc.generation {
			t.Fatalf("restart ref=%q key=%q gen=%d err=%v/%v", tc.ref, got, gen, err, genErr)
		}
	}
	same, err := reopened.StageCustomDefaultSecret(ctx, "edge", ref, []byte("replacement"))
	if err != nil || same != ref {
		t.Fatalf("same key changed revision=%q err=%v", same, err)
	}
	legacyUUID := "00000000-0000-4000-8000-000000000000"
	path := filepath.Join(root, "edge", "edge:default", "key-"+legacyUUID+".blob")
	if err := os.WriteFile(path, []byte("prior-revision"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, ok, err := reopened.GetCustomDefaultSecret(ctx, "edge", "prism-key:"+legacyUUID)
	gen, genErr := reopened.CustomDefaultGeneration(ctx, "edge", "prism-key:"+legacyUUID)
	if !ok || err != nil || string(got) != "prior-revision" || gen != 1 || genErr != nil {
		t.Fatalf("legacy UUID key=%q gen=%d err=%v/%v", got, gen, err, genErr)
	}
	for _, bad := range []string{"prism-key:../outside", "router:default", "prism-key:invalid"} {
		if _, _, err := reopened.GetCustomDefaultSecret(ctx, "edge", bad); err == nil {
			t.Fatalf("unsafe ref accepted: %q", bad)
		}
	}
}

func TestConcurrentGenerationPublicationNeverOverwritesWinner(t *testing.T) {
	s := NewFileCredentialStore(t.TempDir())
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, 2)
	keys := []string{"first", "second"}
	for i := range keys {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = s.Put(context.Background(), "codex", "account", 1, []byte(keys[i]))
		}(i)
	}
	close(start)
	wg.Wait()
	winner := -1
	for i, err := range errs {
		if err == nil {
			if winner >= 0 {
				t.Fatal("two writers published same generation")
			}
			winner = i
		}
	}
	if winner < 0 {
		t.Fatalf("no writer succeeded: %v", errs)
	}
	got, ok, err := s.Get(context.Background(), "codex", "account", 1)
	if err != nil || !ok || string(got) != keys[winner] {
		t.Fatalf("winner overwritten: %q err=%v", got, err)
	}
}
