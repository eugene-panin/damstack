package remote

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/eugene-panin/damstack/internal/atomicfile"
	"github.com/hashicorp/consul/api"
)

// MaxState is the largest state Consul's KV store takes as one value (its
// default limit is 512 KiB).
const MaxState = 500 * 1024

// ErrStateMoved is returned by Push when the state on the platform changed
// since it was pulled: someone else wrote it, which the lock should prevent.
var ErrStateMoved = errors.New("the state on the platform changed since it was read")

// Store keeps the OpenTofu state of a project's steps on the platform, one key
// per unit (damstack/<project>/state/<unit>), and a copy in the project's
// directory that OpenTofu works on and that stays as the cache when the
// platform is out of reach. The state is the file OpenTofu writes, encrypted
// by OpenTofu itself.
type Store struct {
	r *Remote
	// Held is the lock Push needs; without it the store only pulls.
	Held *Held

	mu      sync.Mutex
	indexes map[string]uint64
}

// NewStore is a Store on r; held may be nil, for one that only reads.
func NewStore(r *Remote, held *Held) *Store {
	return &Store{r: r, Held: held, indexes: map[string]uint64{}}
}

// ErrUnpushed is returned by Pull when the local state of a unit is newer than
// the platform's: a push failed after OpenTofu changed things, and pulling now
// would throw that state away.
var ErrUnpushed = errors.New("the local state was never written to the platform")

func unpushed(path string) string { return path + ".unpushed" }

// Pull writes the platform's state of unit to path. When the platform has none
// it leaves path as it is: a unit not published yet. When an earlier push of
// path failed, it keeps path and, under the lock and with the platform's state
// as that push left it, readies the next Push to write path there.
func (s *Store) Pull(unit, path string) error {
	pair, _, err := s.r.KV.Get(s.r.key("state", unit), nil)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if mark, err := os.ReadFile(unpushed(path)); err == nil {
		return s.resume(unit, path, pair, mark)
	}
	if pair == nil {
		s.indexes[unit] = 0
		return nil
	}
	s.indexes[unit] = pair.ModifyIndex
	return writeFile(path, pair.Value)
}

func (s *Store) resume(unit, path string, pair *api.KVPair, mark []byte) error {
	local, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var index uint64
	if pair != nil {
		index = pair.ModifyIndex
	}
	if pair != nil && bytes.Equal(pair.Value, local) {
		s.indexes[unit] = index
		os.Remove(unpushed(path))
		return nil
	}
	base, err := strconv.ParseUint(strings.TrimSpace(string(mark)), 10, 64)
	known := err == nil
	if s.Held != nil && known && base == index {
		s.indexes[unit] = index
		return nil
	}
	if s.Held == nil {
		return fmt.Errorf("%s: %w; it is in %s, and the platform has an older one. Run damstack deploy "+
			"again to write it there", unit, ErrUnpushed, path)
	}
	return fmt.Errorf("%s: %w; it is in %s, and the platform's state changed since that push failed, "+
		"so neither is kept over the other. Compare the two, keep the right one in %s and remove %s",
		unit, ErrUnpushed, path, path, unpushed(path))
}

// Push writes path to the platform as the state of unit, only if no one wrote
// it since this store pulled it (a unit never pulled must be new there). When
// it cannot, it marks the local state as newer, with the platform's state it
// was meant to follow, so no Pull throws it away.
func (s *Store) Push(unit, path string) error {
	if s.Held == nil {
		return errors.New("the state is written to the platform only under the project's lock")
	}
	s.mu.Lock()
	base := s.indexes[unit]
	s.mu.Unlock()
	err := s.push(unit, path)
	if err != nil {
		if merr := os.WriteFile(unpushed(path), []byte(strconv.FormatUint(base, 10)), 0o600); merr != nil {
			return errors.Join(err, fmt.Errorf("and %s could not be marked as newer than the platform's: %w", path, merr))
		}
		return err
	}
	os.Remove(unpushed(path))
	return nil
}

func (s *Store) push(unit, path string) error {
	if err := s.Held.Check(); err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(data) > MaxState {
		return fmt.Errorf("the state of %s is %d KiB, more than the %d KiB the platform's Consul keeps as one value", unit, len(data)/1024, MaxState/1024)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	index := s.indexes[unit]
	ok, _, err := s.r.KV.CAS(&api.KVPair{Key: s.r.key("state", unit), Value: data, ModifyIndex: index}, nil)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	if !ok {
		return fmt.Errorf("%s: %w", unit, ErrStateMoved)
	}
	pair, _, err := s.r.KV.Get(s.r.key("state", unit), nil)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	if pair != nil {
		s.indexes[unit] = pair.ModifyIndex
	}
	return nil
}

// Units are the units whose state the platform keeps.
func (s *Store) Units() ([]string, error) {
	pairs, _, err := s.r.KV.List(s.r.key("state")+"/", nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	units := make([]string, 0, len(pairs))
	for _, p := range pairs {
		units = append(units, filepath.Base(p.Key))
	}
	return units, nil
}

// writeFile replaces path at once, so OpenTofu never reads half a state.
func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return atomicfile.Write(path, data, 0o600)
}
