package manifest

import (
	"slices"
	"strings"
	"testing"
)

const validDomain = `apiVersion: damstack/v2
name: cloudflare
kind: domain
description: Your domain on Cloudflare
provides: [public-dns@1]
requires:
  provides: [nomad]
secrets:
  - name: cloudflare_api_token
    ask: A Cloudflare API token
targets:
  nomad:
    steps:
      - name: records
        tofu: {dir: infra, action: apply}
check:
  answers: test/answers.yaml
  platform: test/answers.yaml
`

func TestValidDomainLoads(t *testing.T) {
	m, err := Load(stackDir(t, validDomain), "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if !m.IsApp() || !m.IsDomain() || !slices.Equal(m.Provides, []string{"public-dns@1"}) {
		t.Errorf("got %+v", m)
	}
	if name, _, ok := m.Target([]string{"nomad"}); !ok || name != "nomad" {
		t.Errorf("no target on a platform with nomad: %s", name)
	}
	app, err := Load(stackDir(t, validApp), "0.1.0")
	if err != nil || app.IsDomain() {
		t.Errorf("an app is a domain: %v", err)
	}
}

func TestDomainProblems(t *testing.T) {
	for _, tc := range []struct{ name, from, to, want string }{
		{"secret without the domain's name", "name: cloudflare_api_token", "name: api_token", "begin with its name: cloudflare_"},
		{"without requires", "requires:\n  provides: [nomad]\n", "", "requires.provides: is required"},
		{"with steps", "targets:", "steps:\n  - name: x\n    run: [bin/extra]\ntargets:", "under targets"},
		{"with a server", "kind: domain", "kind: domain\nserver: {address: x, first_user: root}", "is the platform's"},
		{"a bad capability", "provides: [public-dns@1]", "provides: [Public-DNS]", "provides"},
		{"an unknown kind", "kind: domain", "kind: domains", "platform, app or domain"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(validDomain, tc.from) {
				t.Fatalf("the valid domain has no %q", tc.from)
			}
			_, err := Load(stackDir(t, strings.Replace(validDomain, tc.from, tc.to, 1)), "0.1.0")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %v, want %q", err, tc.want)
			}
		})
	}
}

func TestTheDNSOfTheTunnelIsOffUnlessSaid(t *testing.T) {
	m, err := Load(stackDir(t, valid), "0.1.0")
	if err != nil || m.Server.WireGuard.DNS {
		t.Fatalf("dns without dns: %v", err)
	}
	m, err = Load(stackDir(t, strings.Replace(valid, "    apply: output\n  ssh_key:", "    apply: output\n    dns: true\n  ssh_key:", 1)), "0.1.0")
	if err != nil || !m.Server.WireGuard.DNS {
		t.Errorf("no dns with dns: true: %v", err)
	}
}
