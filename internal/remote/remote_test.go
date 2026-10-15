package remote

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hashicorp/consul/api"
)

func who(name string) Holder {
	return Holder{Person: name, Host: "laptop", Action: "deploy", Since: time.Unix(1_700_000_000, 0)}
}

func TestOneHoldsTheLockTheOthersWaitInTurn(t *testing.T) {
	f := newFake()
	r := New("twinmind", f, f)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	first, err := r.Lock(ctx, who("eugene"), nil)
	if err != nil {
		t.Fatal(err)
	}

	type result struct {
		name string
		held *Held
	}
	got := make(chan result, 2)
	waits := make(chan Waiting, 16)
	go func() {
		h, err := r.Lock(ctx, who("aleksandr"), func(w Waiting) { waits <- w })
		if err != nil {
			t.Error(err)
		}
		got <- result{"aleksandr", h}
	}()
	// Aleksandr asks first, then Garen: Aleksandr must come first.
	w := <-waits
	if w.Position != 1 || w.Holder == nil || w.Holder.Person != "eugene" {
		t.Fatalf("aleksandr must see he is next, behind eugene, got %+v", w)
	}
	go func() {
		h, err := r.Lock(ctx, who("garen"), nil)
		if err != nil {
			t.Error(err)
		}
		got <- result{"garen", h}
	}()
	time.Sleep(500 * time.Millisecond)
	select {
	case r := <-got:
		t.Fatalf("%s took the lock while eugene held it", r.name)
	default:
	}

	first.Release()
	next := <-got
	if next.name != "aleksandr" {
		t.Fatalf("the lock must go to who asked first, went to %s", next.name)
	}
	if h, _ := r.Holder(); h == nil || h.Person != "aleksandr" {
		t.Fatalf("the holder must be aleksandr, got %+v", h)
	}
	next.held.Release()
	last := <-got
	if last.name != "garen" {
		t.Fatalf("garen must get it last, got %s", last.name)
	}
	last.held.Release()
	if h, _ := r.Holder(); h != nil {
		t.Fatalf("no one must hold it after the last release, got %+v", h)
	}
}

func TestALostSessionIsCaughtBeforeTheNextStep(t *testing.T) {
	f := newFake()
	r := New("twinmind", f, f)
	h, err := r.Lock(context.Background(), who("eugene"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Check(); err != nil {
		t.Fatal(err)
	}
	f.expire(h.session) // the laptop slept past the TTL
	if err := h.Check(); !errors.Is(err, ErrLost) {
		t.Fatalf("a lost lock must stop the next step, got %v", err)
	}
	// someone else can take it now
	other, err := r.Lock(context.Background(), who("aleksandr"), nil)
	if err != nil {
		t.Fatal(err)
	}
	other.Release()
	h.Release()
}

func TestAWaitEndsWithItsContext(t *testing.T) {
	f := newFake()
	r := New("twinmind", f, f)
	h, err := r.Lock(context.Background(), who("eugene"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Release()
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	if _, err := r.Lock(ctx, who("aleksandr"), nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("giving up must end the wait, got %v", err)
	}
	pairs, _, _ := f.List(r.key("queue")+"/", nil)
	if len(pairs) != 1 {
		t.Fatalf("one who gave up must leave the queue, %d places left", len(pairs))
	}
}

func TestUnreachableConsulIsSaidSo(t *testing.T) {
	f := newFake()
	f.down = true
	r := New("twinmind", f, f)
	if err := r.Reachable(); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("got %v", err)
	}
	if _, err := r.Lock(context.Background(), who("eugene"), nil); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("got %v", err)
	}
}

func TestStateGoesUpUnderTheLockAndComesBack(t *testing.T) {
	f := newFake()
	r := New("twinmind", f, f)
	dir := t.TempDir()
	path := filepath.Join(dir, "sauron-infra.tfstate")

	reader := NewStore(r, nil)
	if err := reader.Pull("sauron-infra", path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("pulling a unit the platform does not have must leave the local file alone")
	}
	os.WriteFile(path, []byte(`{"serial":1}`), 0o600)
	if err := reader.Push("sauron-infra", path); err == nil {
		t.Fatal("pushing without the lock must be refused")
	}

	h, err := r.Lock(context.Background(), who("eugene"), nil)
	if err != nil {
		t.Fatal(err)
	}
	mine := NewStore(r, h)
	if err := mine.Pull("sauron-infra", path); err != nil {
		t.Fatal(err)
	}
	if err := mine.Push("sauron-infra", path); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path, []byte(`{"serial":2}`), 0o600)
	if err := mine.Push("sauron-infra", path); err != nil {
		t.Fatalf("a second push after its own must go through: %v", err)
	}

	// Someone wrote it behind the lock (a broken lock): the next push stops.
	pair, _, _ := f.Get(r.key("state", "sauron-infra"), nil)
	pair.Value = []byte(`{"serial":9}`)
	f.CAS(pair, nil)
	os.WriteFile(path, []byte(`{"serial":3}`), 0o600)
	if err := mine.Push("sauron-infra", path); !errors.Is(err, ErrStateMoved) {
		t.Fatalf("a state changed on the platform must not be overwritten, got %v", err)
	}
	h.Release()

	other := filepath.Join(t.TempDir(), "x.tfstate")
	if err := NewStore(r, nil).Pull("sauron-infra", other); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(other); string(data) != `{"serial":9}` {
		t.Fatalf("pull must bring the platform's state, got %s", data)
	}
	if units, _ := NewStore(r, nil).Units(); len(units) != 1 || units[0] != "sauron-infra" {
		t.Fatalf("got %v", units)
	}
}

func TestATooLargeStateIsRefusedWithItsSize(t *testing.T) {
	f := newFake()
	r := New("twinmind", f, f)
	h, _ := r.Lock(context.Background(), who("eugene"), nil)
	defer h.Release()
	path := filepath.Join(t.TempDir(), "big.tfstate")
	os.WriteFile(path, make([]byte, MaxState+1), 0o600)
	if err := NewStore(r, h).Push("big", path); err == nil {
		t.Fatal("a state Consul cannot keep must be refused before writing")
	}
}

func TestAStateThatFailedToGoUpIsNeverPulledOver(t *testing.T) {
	f := newFake()
	r := New("twinmind", f, f)
	path := filepath.Join(t.TempDir(), "infra.tfstate")
	h, _ := r.Lock(context.Background(), who("eugene"), nil)
	store := NewStore(r, h)
	os.WriteFile(path, []byte(`{"serial":1}`), 0o600)
	store.Pull("infra", path)
	if err := store.Push("infra", path); err != nil {
		t.Fatal(err)
	}

	os.WriteFile(path, []byte(`{"serial":2}`), 0o600) // OpenTofu applied
	f.down = true
	if err := store.Push("infra", path); err == nil {
		t.Fatal("expected the push to fail with Consul down")
	}
	f.down = false
	if err := NewStore(r, nil).Pull("infra", path); !errors.Is(err, ErrUnpushed) {
		t.Fatalf("pulling over a state the platform lacks must be refused, got %v", err)
	}
	if data, _ := os.ReadFile(path); string(data) != `{"serial":2}` {
		t.Fatalf("the newer local state must stay, got %s", data)
	}
	if err := store.Push("infra", path); err != nil {
		t.Fatalf("pushing again once Consul answers must work: %v", err)
	}
	if err := NewStore(r, nil).Pull("infra", path); err != nil {
		t.Fatalf("once pushed, pulls work again: %v", err)
	}
	h.Release()
}

func TestAStateThatFailedToGoUpGoesUpOnTheNextDeploy(t *testing.T) {
	f := newFake()
	r := New("twinmind", f, f)
	path := filepath.Join(t.TempDir(), "infra.tfstate")
	h, _ := r.Lock(context.Background(), who("eugene"), nil)
	store := NewStore(r, h)
	os.WriteFile(path, []byte(`{"serial":1}`), 0o600)
	store.Pull("infra", path)
	store.Push("infra", path)
	os.WriteFile(path, []byte(`{"serial":2}`), 0o600)
	f.down = true
	store.Push("infra", path)
	f.down = false
	h.Release()

	h, _ = r.Lock(context.Background(), who("eugene"), nil)
	defer h.Release()
	next := NewStore(r, h)
	if err := next.Pull("infra", path); err != nil {
		t.Fatalf("the next deploy must take up the state that failed to go up: %v", err)
	}
	if data, _ := os.ReadFile(path); string(data) != `{"serial":2}` {
		t.Fatalf("the newer local state must stay, got %s", data)
	}
	if err := next.Push("infra", path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".unpushed"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a pushed state must not stay marked")
	}
}

func TestAStateThatFailedToGoUpIsNotForcedOverAChangedOne(t *testing.T) {
	f := newFake()
	r := New("twinmind", f, f)
	path := filepath.Join(t.TempDir(), "infra.tfstate")
	h, _ := r.Lock(context.Background(), who("eugene"), nil)
	store := NewStore(r, h)
	os.WriteFile(path, []byte(`{"serial":1}`), 0o600)
	store.Pull("infra", path)
	store.Push("infra", path)
	os.WriteFile(path, []byte(`{"serial":2}`), 0o600)
	f.down = true
	store.Push("infra", path)
	f.down = false
	pair, _, _ := f.Get(r.key("state", "infra"), nil)
	pair.Value = []byte(`{"serial":7}`)
	f.CAS(pair, nil)

	if err := NewStore(r, h).Pull("infra", path); !errors.Is(err, ErrUnpushed) {
		t.Fatalf("got %v", err)
	}
	if data, _ := os.ReadFile(path); string(data) != `{"serial":2}` {
		t.Fatalf("the local state must stay, got %s", data)
	}
	h.Release()
}

func TestAStateThePlatformGotAfterAllIsNoLongerMarked(t *testing.T) {
	f := newFake()
	r := New("twinmind", f, f)
	path := filepath.Join(t.TempDir(), "infra.tfstate")
	os.WriteFile(path, []byte(`{"serial":2}`), 0o600)
	os.WriteFile(path+".unpushed", []byte("1"), 0o600)
	f.CAS(&api.KVPair{Key: r.key("state", "infra"), Value: []byte(`{"serial":2}`)}, nil)
	if err := NewStore(r, nil).Pull("infra", path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".unpushed"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the mark must go once the platform has the same state")
	}
}
