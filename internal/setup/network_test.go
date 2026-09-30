package setup

import (
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/eugene-panin/damstack/internal/config"
)

func TestFreeSubnet(t *testing.T) {
	pool := netip.MustParsePrefix("10.64.0.0/10")
	for range 50 {
		got, err := FreeSubnet([]netip.Prefix{netip.MustParsePrefix("10.64.0.0/11")})
		if err != nil {
			t.Fatal(err)
		}
		p := netip.MustParsePrefix(got)
		if p.Bits() != 24 || !pool.Contains(p.Addr()) || netip.MustParsePrefix("10.64.0.0/11").Overlaps(p) || p.Overlaps(legacy) {
			t.Fatalf("got %s", got)
		}
	}
	if _, err := FreeSubnet([]netip.Prefix{pool}); err == nil {
		t.Error("a subnet out of a full pool")
	}
	a, _ := FreeSubnet(nil)
	b, _ := FreeSubnet(nil)
	c, _ := FreeSubnet(nil)
	if a == b && b == c {
		t.Errorf("three projects took %s", a)
	}
}

func TestUsedNetworks(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "stack.yaml"), []byte("network: {cidr: 10.70.5.0/24}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Projects = append(cfg.Projects, config.Project{Name: "other", Path: dir})
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	used := usedNetworks()
	if !slices.Contains(used, netip.MustParsePrefix("10.70.5.0/24")) {
		t.Errorf("the network of another project is not among %v", used)
	}
	if !slices.ContainsFunc(used, func(p netip.Prefix) bool { return p.Addr().IsLoopback() }) {
		t.Errorf("the loopback network of this machine is not among %v", used)
	}
}
