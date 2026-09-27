// Package project reads the stack.yaml of a damstack project.
package project

import (
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const File = "stack.yaml"

type Stack struct {
	Name   string `yaml:"name"`
	Server struct {
		OpsUser string `yaml:"ops_user"`
	} `yaml:"server"`
	Network struct {
		CIDR string `yaml:"cidr"`
	} `yaml:"network"`
}

type Project struct {
	Dir   string
	Stack Stack
}

// Find walks up from dir to the first directory with a stack.yaml. It returns
// nil and no error when there is none.
func Find(dir string) (*Project, error) {
	for {
		data, err := os.ReadFile(filepath.Join(dir, File))
		switch {
		case err == nil:
			var s Stack
			if err := yaml.Unmarshal(data, &s); err != nil {
				return nil, fmt.Errorf("%s: %w", filepath.Join(dir, File), err)
			}
			return &Project{Dir: dir, Stack: s}, nil
		case !errors.Is(err, fs.ErrNotExist):
			return nil, err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, nil
		}
		dir = parent
	}
}

// Name is the directory name, which is also where the project keeps its vault
// password: ~/.config/<name>/vault-pass.
func (p *Project) Name() string { return filepath.Base(p.Dir) }

// ServerAddress is the first host of the WireGuard network, which the server
// takes.
func (p *Project) ServerAddress() (netip.Addr, error) {
	prefix, err := netip.ParsePrefix(p.Stack.Network.CIDR)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("network.cidr in %s: %w", File, err)
	}
	return prefix.Masked().Addr().Next(), nil
}
