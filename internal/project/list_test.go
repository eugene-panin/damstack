package project

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestSetList(t *testing.T) {
	for name, tc := range map[string]struct{ before, after string }{
		"flow": {
			"# the network\nnetwork:\n  cidr: 10.77.0.0/24\n  clients: [laptop]  # devices\napps: {}\n",
			"# the network\nnetwork:\n  cidr: 10.77.0.0/24\n  clients: [laptop, phone]  # devices\napps: {}\n",
		},
		"block": {
			"network:\n  clients:\n    - laptop\n  # the next\n  cidr: 10.77.0.0/24\n",
			"network:\n  clients:\n    - laptop\n    - phone\n  # the next\n  cidr: 10.77.0.0/24\n",
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			os.WriteFile(filepath.Join(dir, ConfigFile), []byte(tc.before), 0o644)
			p := &Project{Dir: dir}
			if err := p.SetList("network.clients", []string{"laptop", "phone"}); err != nil {
				t.Fatal(err)
			}
			got, _ := os.ReadFile(filepath.Join(dir, ConfigFile))
			if string(got) != tc.after {
				t.Errorf("got\n%s\nwant\n%s", got, tc.after)
			}
			config, _ := p.Config()
			if names, err := List(config, "network.clients"); err != nil || !slices.Equal(names, []string{"laptop", "phone"}) {
				t.Errorf("read back %v, %v", names, err)
			}
		})
	}
	p := &Project{Dir: t.TempDir()}
	os.WriteFile(filepath.Join(p.Dir, ConfigFile), []byte("network:\n  cidr: x\n"), 0o644)
	if err := p.SetList("network.clients", nil); err == nil {
		t.Error("set a list that is not there")
	}
}
