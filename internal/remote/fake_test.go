package remote

import (
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/hashicorp/consul/api"
)

// fake is Consul's KV store and sessions in memory, with the rules the package
// relies on: raft indexes, CAS, Acquire held by a session, and keys deleted
// with the session that holds them.
type fake struct {
	mu       sync.Mutex
	index    uint64
	pairs    map[string]*api.KVPair
	sessions map[string]bool
	n        int
	down     bool
}

func newFake() *fake {
	return &fake{pairs: map[string]*api.KVPair{}, sessions: map[string]bool{}}
}

func (f *fake) err() error {
	if f.down {
		return fmt.Errorf("dial tcp 10.0.0.1:8501: connection refused")
	}
	return nil
}

func (f *fake) put(p *api.KVPair, session string) {
	f.index++
	old := f.pairs[p.Key]
	c := &api.KVPair{Key: p.Key, Value: slices.Clone(p.Value), Session: session, ModifyIndex: f.index, CreateIndex: f.index}
	if old != nil {
		c.CreateIndex = old.CreateIndex
	}
	f.pairs[p.Key] = c
}

func (f *fake) Get(key string, _ *api.QueryOptions) (*api.KVPair, *api.QueryMeta, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.err(); err != nil {
		return nil, nil, err
	}
	p := f.pairs[key]
	if p == nil {
		return nil, &api.QueryMeta{LastIndex: f.index}, nil
	}
	c := *p
	return &c, &api.QueryMeta{LastIndex: f.index}, nil
}

func (f *fake) List(prefix string, _ *api.QueryOptions) (api.KVPairs, *api.QueryMeta, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.err(); err != nil {
		return nil, nil, err
	}
	var out api.KVPairs
	for k, p := range f.pairs {
		if strings.HasPrefix(k, prefix) {
			c := *p
			out = append(out, &c)
		}
	}
	return out, &api.QueryMeta{LastIndex: f.index}, nil
}

func (f *fake) CAS(p *api.KVPair, _ *api.WriteOptions) (bool, *api.WriteMeta, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.err(); err != nil {
		return false, nil, err
	}
	old := f.pairs[p.Key]
	switch {
	case p.ModifyIndex == 0 && old != nil:
		return false, nil, nil
	case p.ModifyIndex != 0 && (old == nil || old.ModifyIndex != p.ModifyIndex):
		return false, nil, nil
	}
	f.put(p, "")
	return true, nil, nil
}

func (f *fake) Acquire(p *api.KVPair, _ *api.WriteOptions) (bool, *api.WriteMeta, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.err(); err != nil {
		return false, nil, err
	}
	if !f.sessions[p.Session] {
		return false, nil, fmt.Errorf("invalid session %q", p.Session)
	}
	if old := f.pairs[p.Key]; old != nil && old.Session != "" && old.Session != p.Session {
		return false, nil, nil
	}
	f.put(p, p.Session)
	return true, nil, nil
}

func (f *fake) Release(p *api.KVPair, _ *api.WriteOptions) (bool, *api.WriteMeta, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if old := f.pairs[p.Key]; old != nil && old.Session == p.Session {
		old.Session = ""
		return true, nil, nil
	}
	return false, nil, nil
}

func (f *fake) Delete(key string, _ *api.WriteOptions) (*api.WriteMeta, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.pairs, key)
	return nil, nil
}

func (f *fake) Create(*api.SessionEntry, *api.WriteOptions) (string, *api.WriteMeta, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.err(); err != nil {
		return "", nil, err
	}
	f.n++
	id := fmt.Sprintf("session-%d", f.n)
	f.sessions[id] = true
	return id, nil, nil
}

func (f *fake) Renew(id string, _ *api.WriteOptions) (*api.SessionEntry, *api.WriteMeta, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.sessions[id] {
		return nil, nil, nil
	}
	return &api.SessionEntry{ID: id}, nil, nil
}

func (f *fake) Destroy(id string, _ *api.WriteOptions) (*api.WriteMeta, error) {
	f.expire(id)
	return nil, nil
}

func (f *fake) Info(id string, _ *api.QueryOptions) (*api.SessionEntry, *api.QueryMeta, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.err(); err != nil {
		return nil, nil, err
	}
	if !f.sessions[id] {
		return nil, nil, nil
	}
	return &api.SessionEntry{ID: id}, nil, nil
}

// expire ends a session as its TTL would: the keys it holds are deleted
// (behavior delete).
func (f *fake) expire(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.sessions, id)
	for k, p := range f.pairs {
		if p.Session == id {
			delete(f.pairs, k)
			f.index++
		}
	}
}
