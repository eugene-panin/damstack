package project

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestSetList(t *testing.T) {
	for name, tc := range map[string]struct {
		before, after string
		names         []string
	}{
		"block emptied": {
			"network:\n  clients:\n    - laptop\n  cidr: 10.77.0.0/24\n",
			"network:\n  clients: []\n  cidr: 10.77.0.0/24\n", []string{},
		},
		"block with a comment before its first item, emptied": {
			"network:\n  clients:\n    # devices\n    - laptop\n  cidr: 10.77.0.0/24\n",
			"network:\n  clients: []\n    # devices\n  cidr: 10.77.0.0/24\n", []string{},
		},
		"flow": {
			"# the network\nnetwork:\n  cidr: 10.77.0.0/24\n  clients: [laptop]  # devices\napps: {}\n",
			"# the network\nnetwork:\n  cidr: 10.77.0.0/24\n  clients: [laptop, phone]  # devices\napps: {}\n", nil,
		},
		"block": {
			"network:\n  clients:\n    - laptop\n  # the next\n  cidr: 10.77.0.0/24\n",
			"network:\n  clients:\n    - laptop\n    - phone\n  # the next\n  cidr: 10.77.0.0/24\n", nil,
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			os.WriteFile(filepath.Join(dir, ConfigFile), []byte(tc.before), 0o644)
			p := &Project{Dir: dir}
			want := tc.names
			if want == nil {
				want = []string{"laptop", "phone"}
			}
			if err := p.SetList("network.clients", want); err != nil {
				t.Fatal(err)
			}
			got, _ := os.ReadFile(filepath.Join(dir, ConfigFile))
			if string(got) != tc.after {
				t.Errorf("got\n%s\nwant\n%s", got, tc.after)
			}
			config, _ := p.Config()
			if names, err := List(config, "network.clients"); err != nil || !slices.Equal(names, want) {
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
