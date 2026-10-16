package manifest

import (
	"reflect"
	"strings"
	"testing"
)

func TestACapabilityIsANameAndAMajor(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{"metrics@1", "metrics@1"},
		{"postgres@16", "postgres@16"},
		{"nomad", "nomad@1"},
		{"vault-kv", "vault-kv@1"},
		{"s3@01", "s3@1"},
	} {
		c, err := ParseCapability(tc.in)
		if err != nil || c.String() != tc.want {
			t.Errorf("%q: got %v, %v; want %s", tc.in, c, err, tc.want)
		}
	}
	for _, bad := range []string{"", "Metrics", "metrics@", "metrics@x", "metrics@1.2", "@1", "m", "metrics@1@2", "postgres@>=15"} {
		if _, err := ParseCapability(bad); err == nil {
			t.Errorf("%q was taken for a capability", bad)
		}
	}
	if !Has([]string{"nomad", "vault-kv"}, "nomad@1") || !Has([]string{"nomad@1"}, "nomad") || Has([]string{"postgres@15"}, "postgres@16") {
		t.Error("a bare name is the major 1 of its capability")
	}
}

func TestANeedHoldsByItsCondition(t *testing.T) {
	needs := []Need{{Capability: "metrics@1"}, {Capability: "postgres@16", When: "postgres.mode=external"}, {Capability: "s3@1", When: "files"}}
	sure, maybe := Split(needs, nil)
	if len(sure) != 1 || len(maybe) != 2 {
		t.Errorf("before the settings: sure %v, maybe %v", sure, maybe)
	}
	sure, maybe = Split(needs, map[string]any{"postgres": map[string]any{"mode": "external"}, "files": false})
	if got := NeedCapabilities(sure); len(maybe) != 0 || len(got) != 2 || got[1].String() != "postgres@16" {
		t.Errorf("external, no files: sure %v, maybe %v", sure, maybe)
	}
	sure, _ = Split(needs, map[string]any{"postgres": map[string]any{"mode": "bundled"}, "files": true})
	if got := NeedCapabilities(sure); len(got) != 2 || got[1].String() != "s3@1" {
		t.Errorf("bundled, files: sure %v", sure)
	}
}

const appWithCapabilities = `apiVersion: damstack/v2
name: odoo
kind: app
description: Odoo
provides: [erp@1]
requires:
  provides: [nomad]
  capabilities:
    - ingress-public@1
    - {capability: postgres@16, when: postgres_mode=external}
uses: [metrics@1, {capability: logs, when: postgres.mode=external}]
questions:
  - name: postgres_mode
    prompt: Where the database is
    type: enum
    options: [bundled, external]
    default: bundled
config: template/stack.yaml.tmpl
targets:
  nomad:
    steps:
      - name: apply
        tofu: {dir: infra, action: apply}
check:
  answers: test/answers.yaml
  platform: test/answers.yaml
`

func TestAnAppSaysWhatItProvidesRequiresAndUses(t *testing.T) {
	m, err := Load(stackDir(t, appWithCapabilities), "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	want := []Need{{Capability: "ingress-public@1"}, {Capability: "postgres@16", When: "postgres_mode=external"}}
	if !reflect.DeepEqual(m.Requires.Capabilities, want) || !reflect.DeepEqual(m.Provides, []string{"erp@1"}) ||
		!reflect.DeepEqual(m.Uses, []Need{{Capability: "metrics@1"}, {Capability: "logs", When: "postgres.mode=external"}}) {
		t.Errorf("got requires %+v, provides %v, uses %+v", m.Requires.Capabilities, m.Provides, m.Uses)
	}
	if name, _, ok := m.Target([]string{"nomad@1", "vault-kv@1"}); !ok || name != "nomad" {
		t.Error("a target by a bare name runs on a platform that provides it at major 1")
	}
}

func TestCapabilityProblems(t *testing.T) {
	for _, tc := range []struct{ name, from, to, want string }{
		{"a bad capability", "    - ingress-public@1\n", "    - Ingress\n", `"Ingress" is not a capability`},
		{"a bad provide", "provides: [erp@1]", "provides: [erp@one]", `provides[0]: "erp@one" is not a capability`},
		{"twice", "provides: [erp@1]", "provides: [erp@1, erp]", "provides[1]: erp@1 is there already"},
		{"when on no question", "when: postgres_mode=external}", "when: db=external}", `"db" is not a question asked before this`},
		{"when on no option", "when: postgres_mode=external}", "when: postgres_mode=far}", `"far" is not an option of postgres_mode`},
		{"a bad setting", "when: postgres.mode=external}]", "when: postgres..mode=x}]", `"postgres..mode" is not a setting`},
		{"an item of another shape", "    - ingress-public@1\n", "    - {cap: ingress-public@1}\n", "a capability is name@major, or {capability"},
		{"an unknown field", "    - ingress-public@1\n", "    - {capability: ingress-public@1, if: x}\n", "unknown field if"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(appWithCapabilities, tc.from) {
				t.Fatalf("no %q", tc.from)
			}
			_, err := Load(stackDir(t, strings.Replace(appWithCapabilities, tc.from, tc.to, 1)), "0.1.0")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %v, want %q", err, tc.want)
			}
		})
	}
	for _, tc := range []struct{ to, want string }{
		{"  host: [ssh-key, wireguard]\nuses: [metrics@1]", "uses: is for an app"},
		{"  host: [ssh-key, wireguard]\n  capabilities: [metrics@1]", "requires.capabilities: is for an app"},
	} {
		_, err := Load(stackDir(t, strings.Replace(valid, "  host: [ssh-key, wireguard]", tc.to, 1)), "0.1.0")
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("a platform: got %v, want %q", err, tc.want)
		}
	}
}

func TestANeedIsWrittenAsItIsRead(t *testing.T) {
	for _, n := range []Need{{Capability: "metrics@1"}, {Capability: "postgres@16", When: "postgres.mode=external"}} {
		v, err := n.MarshalYAML()
		if err != nil {
			t.Fatal(err)
		}
		if _, plain := v.(string); plain != (n.When == "") {
			t.Errorf("%+v is written as %v", n, v)
		}
	}
}
