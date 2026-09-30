package setup

import (
	"errors"
	"math/rand/v2"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"text/template"

	"gopkg.in/yaml.v3"

	"github.com/eugene-panin/damstack/internal/config"
	"github.com/eugene-panin/damstack/internal/project"
)

// DefaultFuncs are what the defaults of questions call when a project is set
// up.
func DefaultFuncs() template.FuncMap {
	return template.FuncMap{"freeSubnet": func() (string, error) { return FreeSubnet(usedNetworks()) }}
}

var (
	pool = netip.MustParsePrefix("10.64.0.0/10")
	// before each project had its own network, stacks took this one
	legacy = netip.MustParsePrefix("10.77.0.0/24")
)

// FreeSubnet is a /24 of 10.64.0.0/10 that overlaps none of used, picked at
// random so that two machines setting up projects seldom pick the same.
func FreeSubnet(used []netip.Prefix) (string, error) {
	used = append(used, legacy)
	const count = 1 << 14
	start := rand.IntN(count)
	base := pool.Addr().As4()
	for i := range count {
		n := (start + i) % count
		a := base
		a[1] += byte(n >> 8)
		a[2] = byte(n)
		p := netip.PrefixFrom(netip.AddrFrom4(a), 24)
		if !overlapsAny(p, used) {
			return p.String(), nil
		}
	}
	return "", errors.New("no /24 of 10.64.0.0/10 is free on this machine")
}

func overlapsAny(p netip.Prefix, used []netip.Prefix) bool {
	for _, u := range used {
		if p.Overlaps(u) {
			return true
		}
	}
	return false
}

// usedNetworks are the private networks of the projects damstack knows and
// the networks of this machine's interfaces.
func usedNetworks() []netip.Prefix {
	var used []netip.Prefix
	if cfg, err := config.Load(); err == nil {
		for _, p := range cfg.Projects {
			data, err := os.ReadFile(filepath.Join(p.Path, project.ConfigFile))
			if err != nil {
				continue
			}
			var s struct {
				Network struct {
					CIDR string `yaml:"cidr"`
				} `yaml:"network"`
			}
			if yaml.Unmarshal(data, &s) != nil {
				continue
			}
			if prefix, err := netip.ParsePrefix(s.Network.CIDR); err == nil {
				used = append(used, prefix)
			}
		}
	}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok {
				if prefix, err := netip.ParsePrefix(ipnet.String()); err == nil {
					used = append(used, prefix.Masked())
				}
			}
		}
	}
	return used
}
