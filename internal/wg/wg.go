// Package wg keeps the WireGuard keys of a server and of the devices that
// reach it: made on this machine, kept in the vault of the project, given an
// address once, and rotated on demand.
package wg

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"strings"
	"time"

	"golang.org/x/crypto/curve25519"
	"gopkg.in/yaml.v3"
)

// Keys is the private key and its public key, base64 as wg prints them.
type Keys struct {
	PrivateKey string `yaml:"private_key"`
	PublicKey  string `yaml:"public_key"`
}

// Device is a device on the private network.
type Device struct {
	Keys         `yaml:",inline"`
	Address      string    `yaml:"address"`
	PresharedKey string    `yaml:"preshared_key"`
	Created      time.Time `yaml:"created"`
}

// State is every key of a private network, as the project keeps it.
type State struct {
	Server  Keys               `yaml:"server"`
	Devices map[string]*Device `yaml:"devices"`
}

// NewKeys makes a key pair, clamped as wg genkey does.
func NewKeys() (Keys, error) {
	var priv [32]byte
	if _, err := rand.Read(priv[:]); err != nil {
		return Keys{}, err
	}
	priv[0] &= 248
	priv[31] = priv[31]&127 | 64
	pub, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		return Keys{}, err
	}
	return Keys{PrivateKey: base64.StdEncoding.EncodeToString(priv[:]), PublicKey: base64.StdEncoding.EncodeToString(pub)}, nil
}

// PublicKey is the public key of a private key.
func PublicKey(private string) (string, error) {
	priv, err := base64.StdEncoding.DecodeString(private)
	if err != nil || len(priv) != 32 {
		return "", fmt.Errorf("not a WireGuard private key")
	}
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(pub), nil
}

func newPSK() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// FromSecret reads the state from the value of its secret, empty when there
// is none yet.
func FromSecret(v any) (*State, error) {
	s := &State{}
	if v != nil {
		data, err := yaml.Marshal(v)
		if err != nil {
			return nil, err
		}
		if err := yaml.Unmarshal(data, s); err != nil {
			return nil, fmt.Errorf("the WireGuard keys of the project: %w", err)
		}
	}
	if s.Devices == nil {
		s.Devices = map[string]*Device{}
	}
	return s, nil
}

// Secret is the state as the value of its secret.
func (s *State) Secret() (map[string]any, error) {
	data, err := yaml.Marshal(s)
	if err != nil {
		return nil, err
	}
	var v map[string]any
	return v, yaml.Unmarshal(data, &v)
}

// Change is what Reconcile or a rotation did, for people.
type Change struct {
	Server  bool
	Added   []string
	Removed []string
}

func (c Change) Any() bool { return c.Server || len(c.Added) > 0 || len(c.Removed) > 0 }

// Reconcile makes the keys the server lacks, and gives every device in names
// keys and the first free address of network, in the order of names; the
// devices no longer in names lose theirs. The server takes the first address
// of network.
func (s *State) Reconcile(names []string, network string, now time.Time) (Change, error) {
	var c Change
	prefix, err := netip.ParsePrefix(network)
	if err != nil {
		return c, fmt.Errorf("the network %q: %w", network, err)
	}
	prefix = prefix.Masked()
	if s.Server.PrivateKey == "" {
		if s.Server, err = NewKeys(); err != nil {
			return c, err
		}
		c.Server = true
	}
	for _, name := range slices.Sorted(maps.Keys(s.Devices)) {
		if !slices.Contains(names, name) {
			delete(s.Devices, name)
			c.Removed = append(c.Removed, name)
		}
	}
	for _, name := range names {
		if _, ok := s.Devices[name]; ok {
			continue
		}
		addr, err := s.freeAddress(prefix)
		if err != nil {
			return c, err
		}
		d := &Device{Address: addr.String(), Created: now.UTC()}
		if err := d.renew(now); err != nil {
			return c, err
		}
		s.Devices[name] = d
		c.Added = append(c.Added, name)
	}
	return c, nil
}

func (s *State) freeAddress(prefix netip.Prefix) (netip.Addr, error) {
	used := map[string]bool{}
	for _, d := range s.Devices {
		used[d.Address] = true
	}
	addr := prefix.Addr().Next().Next()
	for prefix.Contains(addr) {
		if !used[addr.String()] && addr.Next().IsValid() && prefix.Contains(addr.Next()) {
			return addr, nil
		}
		addr = addr.Next()
	}
	return netip.Addr{}, fmt.Errorf("no address is left in %s", prefix)
}

func (d *Device) renew(now time.Time) error {
	keys, err := NewKeys()
	if err != nil {
		return err
	}
	psk, err := newPSK()
	if err != nil {
		return err
	}
	d.Keys, d.PresharedKey, d.Created = keys, psk, now.UTC()
	return nil
}

// RotateDevice gives a device new keys and a new preshared key; its address
// stays.
func (s *State) RotateDevice(name string, now time.Time) error {
	d, ok := s.Devices[name]
	if !ok {
		return fmt.Errorf("%s is not a device of the project; damstack tunnel list shows them", name)
	}
	return d.renew(now)
}

// RotateServer gives the server new keys: every device then needs a new
// configuration.
func (s *State) RotateServer() error {
	keys, err := NewKeys()
	if err != nil {
		return err
	}
	s.Server = keys
	return nil
}

// Config is the configuration a device imports: its keys, and the server at
// endpoint, through which it reaches network.
func (s *State) Config(name, endpoint, network string) (string, error) {
	d, ok := s.Devices[name]
	if !ok {
		return "", fmt.Errorf("%s is not a device of the project; damstack tunnel list shows them", name)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[Interface]\nPrivateKey = %s\nAddress = %s/32\n\n", d.PrivateKey, d.Address)
	fmt.Fprintf(&b, "[Peer]\nPublicKey = %s\nPresharedKey = %s\nAllowedIPs = %s\nEndpoint = %s\nPersistentKeepalive = 25\n",
		s.Server.PublicKey, d.PresharedKey, network, endpoint)
	return b.String(), nil
}
