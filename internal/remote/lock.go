package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/consul/api"
)

// Holder says who holds the lock, or waits for it, and what for.
type Holder struct {
	Person string    `json:"person"`
	Host   string    `json:"host"`
	Action string    `json:"action"`
	Since  time.Time `json:"since"`
}

func (h Holder) String() string {
	return fmt.Sprintf("%s on %s: %s, since %s", h.Person, h.Host, h.Action, h.Since.Local().Format("15:04"))
}

// Waiting is what Lock reports while it waits: who holds the lock now, and
// where in the queue this one is (1 is next).
type Waiting struct {
	Holder   *Holder
	Position int
}

// TTL is how long a lock outlives its holder's last sign of life: a holder
// that is gone (a laptop shut, a network lost) frees it after this long.
const TTL = 30 * time.Second

// ErrLost is returned by Held.Check when the lock is no longer held: its
// session ended, so another person may hold it now.
var ErrLost = errors.New("the lock on the project was lost")

// Held is the lock while it is held. Check before every step that changes the
// project; Release when done.
type Held struct {
	r       *Remote
	session string
	stop    context.CancelFunc
	done    chan struct{}

	mu     sync.Mutex
	lost   error
	gone   chan struct{}
	lastOK time.Time
}

// Lock takes the project's lock, waiting in turn behind those who asked first.
// wait is called each time what it waits for changes; ctx cancels the wait.
func (r *Remote) Lock(ctx context.Context, who Holder, wait func(Waiting)) (*Held, error) {
	session, _, err := r.Sessions.Create(&api.SessionEntry{
		Name:      "damstack " + r.Prefix,
		TTL:       TTL.String(),
		Behavior:  api.SessionBehaviorDelete,
		LockDelay: time.Second,
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	h := &Held{r: r, session: session, done: make(chan struct{}), gone: make(chan struct{}), lastOK: time.Now().Round(0)}
	renewCtx, stop := context.WithCancel(context.Background())
	h.stop = stop
	go h.renew(renewCtx)

	value, err := json.Marshal(who)
	if err != nil {
		h.Release()
		return nil, err
	}
	// The place in the queue is a key held by the session: it goes with the
	// session, so one who gives up or vanishes leaves the queue by itself.
	if ok, _, err := r.KV.Acquire(&api.KVPair{Key: r.key("queue", session), Value: value, Session: session}, nil); err != nil || !ok {
		h.Release()
		if err == nil {
			err = errors.New("Consul refused the place in the queue")
		}
		return nil, fmt.Errorf("joining the queue for the lock: %w", err)
	}

	var last Waiting
	var index uint64
	for {
		if err := h.Check(); err != nil {
			h.Release()
			return nil, err
		}
		first, position, err := h.place()
		if err != nil {
			h.Release()
			return nil, err
		}
		if first {
			ok, _, err := r.KV.Acquire(&api.KVPair{Key: r.key("lock"), Value: value, Session: session}, nil)
			if err != nil {
				h.Release()
				return nil, fmt.Errorf("taking the lock: %w", err)
			}
			if ok {
				return h, nil
			}
		}
		// First in the queue and refused: the lock was just freed and Consul
		// holds it back for the lock delay, so look again soon rather than
		// wait for a change that will not come.
		waitTime := 10 * time.Second
		if first {
			waitTime = time.Second
		}
		pair, meta, err := r.KV.Get(r.key("lock"), (&api.QueryOptions{WaitIndex: index, WaitTime: waitTime}).WithContext(ctx))
		if ctx.Err() != nil {
			h.Release()
			return nil, ctx.Err()
		}
		if err != nil {
			h.Release()
			return nil, fmt.Errorf("waiting for the lock: %w", err)
		}
		if meta != nil {
			index = meta.LastIndex
		}
		now := Waiting{Position: position, Holder: holderOf(pair)}
		if wait != nil && (now.Position != last.Position || !sameHolder(now.Holder, last.Holder)) {
			wait(now)
			last = now
		}
		// A blocking query returns at once on a fake or after a change: never spin.
		select {
		case <-ctx.Done():
			h.Release()
			return nil, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// place is whether this session is first in the queue, and its position: how
// many are ahead of it, the holder among them (1 is next once the holder is
// done). The holder keeps its place until it releases the lock.
func (h *Held) place() (bool, int, error) {
	pairs, _, err := h.r.KV.List(h.r.key("queue")+"/", nil)
	if err != nil {
		return false, 0, fmt.Errorf("reading the queue for the lock: %w", err)
	}
	pairs = slices.DeleteFunc(pairs, func(p *api.KVPair) bool { return p.Session == "" })
	slices.SortFunc(pairs, func(a, b *api.KVPair) int {
		if a.CreateIndex != b.CreateIndex {
			return int(a.CreateIndex) - int(b.CreateIndex)
		}
		return strings.Compare(a.Key, b.Key)
	})
	for i, p := range pairs {
		if p.Session == h.session {
			return i == 0, i, nil
		}
	}
	return false, 0, ErrLost
}

func holderOf(p *api.KVPair) *Holder {
	if p == nil || p.Session == "" {
		return nil
	}
	var h Holder
	if json.Unmarshal(p.Value, &h) != nil {
		return nil
	}
	return &h
}

func sameHolder(a, b *Holder) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// Holder is who holds the project's lock now, nil when no one does.
func (r *Remote) Holder() (*Holder, error) {
	pair, _, err := r.KV.Get(r.key("lock"), nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	return holderOf(pair), nil
}

// renew keeps the session alive while the lock is wanted; a renewal Consul
// refuses means the session ended, and the lock with it, and so does a TTL
// gone by with no renewal through, as when the computer slept: the clock read
// is the wall clock, which goes on during a sleep.
func (h *Held) renew(ctx context.Context) {
	defer close(h.done)
	tick := time.NewTicker(TTL / 3)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			// Consul answers a renewal of an ended session with no entry. An
			// error is a network blip: the next tick tries again, and the
			// session lasts TTL from the last renewal that got through.
			entry, _, err := h.r.Sessions.Renew(h.session, nil)
			now := time.Now().Round(0)
			h.mu.Lock()
			late := now.Sub(h.lastOK) > TTL
			if err == nil && entry != nil {
				h.lastOK = now
			}
			h.mu.Unlock()
			if (err == nil && entry == nil) || late {
				h.fail(ErrLost)
				return
			}
		}
	}
}

func (h *Held) fail(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.lost == nil {
		h.lost = err
		close(h.gone)
	}
}

// Watch is ctx, canceled with ErrLost as its cause once the lock is lost, so
// that a tool running then stops rather than change what another may be
// changing.
func (h *Held) Watch(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(ctx)
	go func() {
		select {
		case <-h.gone:
			cancel(ErrLost)
		case <-ctx.Done():
		}
	}()
	return ctx, func() { cancel(nil) }
}

// Check is nil while the lock is still held. Call it before every step that
// changes the project: a lock lost mid-deploy must stop it there.
func (h *Held) Check() error {
	h.mu.Lock()
	lost := h.lost
	h.mu.Unlock()
	if lost != nil {
		return lost
	}
	entry, _, err := h.r.Sessions.Info(h.session, nil)
	if err != nil {
		return fmt.Errorf("checking the lock: %w", err)
	}
	if entry == nil {
		h.fail(ErrLost)
		return ErrLost
	}
	return nil
}

// Release gives the lock up, and the place in the queue with it.
func (h *Held) Release() {
	h.stop()
	select {
	case <-h.done:
	case <-time.After(5 * time.Second):
	}
	// Destroying the session releases and deletes every key it holds.
	_, _ = h.r.Sessions.Destroy(h.session, nil)
}
