package remote

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestAgainstARealConsul runs the lock and the store against a real Consul,
// whose behavior the fake only imitates. It runs when DAMSTACK_TEST_CONSUL_ADDR
// is set (with DAMSTACK_TEST_CONSUL_CACERT and DAMSTACK_TEST_CONSUL_TOKEN as
// needed), under a project of its own that it removes after.
func TestAgainstARealConsul(t *testing.T) {
	addr := os.Getenv("DAMSTACK_TEST_CONSUL_ADDR")
	if addr == "" {
		t.Skip("set DAMSTACK_TEST_CONSUL_ADDR to run against a real Consul")
	}
	project := fmt.Sprintf("itest-%d", time.Now().UnixNano())
	env := map[string]string{
		"CONSUL_HTTP_ADDR":  addr,
		"CONSUL_CACERT":     os.Getenv("DAMSTACK_TEST_CONSUL_CACERT"),
		"CONSUL_HTTP_TOKEN": os.Getenv("DAMSTACK_TEST_CONSUL_TOKEN"),
	}
	r, err := Open(project, env)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Reachable(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pairs, _, _ := r.KV.List(r.Prefix+"/", nil)
		for _, p := range pairs {
			r.KV.Delete(p.Key, nil)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	first, err := r.Lock(ctx, who("eugene"), nil)
	if err != nil {
		t.Fatal(err)
	}
	waits := make(chan Waiting, 8)
	second := make(chan *Held, 1)
	go func() {
		h, err := r.Lock(ctx, who("aleksandr"), func(w Waiting) { waits <- w })
		if err != nil {
			t.Error(err)
		}
		second <- h
	}()
	if w := <-waits; w.Position != 1 || w.Holder == nil || w.Holder.Person != "eugene" {
		t.Fatalf("got %+v", w)
	}

	path := filepath.Join(t.TempDir(), "infra.tfstate")
	os.WriteFile(path, []byte(`{"serial":1}`), 0o600)
	store := NewStore(r, first)
	if err := store.Pull("infra", path); err != nil {
		t.Fatal(err)
	}
	if err := store.Push("infra", path); err != nil {
		t.Fatal(err)
	}
	if err := NewStore(r, first).Push("infra", path); !errors.Is(err, ErrStateMoved) {
		t.Fatalf("a store that never read the state must not overwrite it, got %v", err)
	}

	first.Release()
	h := <-second
	if holder, _ := r.Holder(); holder == nil || holder.Person != "aleksandr" {
		t.Fatalf("got %+v", holder)
	}
	if err := h.Check(); err != nil {
		t.Fatal(err)
	}
	h.Release()
	if holder, _ := r.Holder(); holder != nil {
		t.Fatalf("got %+v", holder)
	}
}
