package manifest

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const valid = `apiVersion: damstack/v1
name: hashistack
description: Consul, Vault and Nomad on one server, over WireGuard
requires:
  damstack: ">=0.1.0"
  toolbox: ">=1.0.0, <2.0.0"
  host: [docker, ssh-key, wireguard]
questions:
  - name: project_name
    prompt: A short name for your setup
    pattern: "^[a-z][a-z0-9-]+$"
  - name: provider
    prompt: Where the server comes from
    type: enum
    options: [ssh, ovh]
    default: ssh
  - name: mail
    prompt: Should the server handle mail?
    type: bool
    default: false
  - name: mail_hostname
    prompt: The mail host name
    when: mail
secrets:
  - name: ca
    generate: ca
    cert: ca.pem
  - name: state_passphrase
    generate: hex
    bytes: 32
  - name: api_token
    ask: An API token
config: template/stack.yaml.tmpl
server:
  address: "{{ .config.server.address }}"
  first_user: root
  ops_user: "{{ .config.server.ops_user }}"
  tunnel: "{{ firstHost .config.network.cidr }}"
backup:
  from: "{{ .config.backup.server }}"
  to: ~/Backups/{{ .config.name }}
  password: '{{ secret "backup_password" }}'
  keep: {daily: "30", weekly: "{{ .config.backup.weekly }}"}
  every: 1h
steps:
  - name: provision
    ansible:
      playbook: ansible/provision.yml
      inventory: ansible/inventory
    keep:
      file: .damstack/work/vault-init.json
      secrets: {root_token: root_token}
  - name: apply
    tofu:
      dir: infra
      action: apply
      policy: {dir: policy, nomad_jobs: true}
    tunnel: true
    env:
      NOMAD_ADDR: "https://{{ firstHost .config.network.cidr }}:4646"
    confirm: true
commands:
  output:
    tofu: {dir: infra, action: output}
  backup-pull:
    run: [bin/extra]
check:
  answers: test/answers.yaml
  steps:
    - name: resolve
      ansible: {playbook: ansible/provision.yml, inventory: ansible/inventory}
`

func stackDir(t *testing.T, manifest string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		File:                       manifest,
		"ansible/provision.yml":    "---\n",
		"template/stack.yaml.tmpl": "name: {{ .project_name }}\n",
		"test/answers.yaml":        "project_name: demo\n",
		"bin/extra":                "#!/bin/sh\n",
		"ansible/inventory/hosts":  "server\n",
		"infra/main.tf":            "",
		"policy/terraform.rego":    "package terraform\n",
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if strings.HasPrefix(name, "bin/") {
			mode = 0o755
		}
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// textLine is the line of the first occurrence of text in doc.
func textLine(doc, text string) int {
	i := strings.Index(doc, text)
	if i < 0 {
		return 0
	}
	return strings.Count(doc[:i], "\n") + 1
}

func TestValidManifestLoads(t *testing.T) {
	m, err := Load(stackDir(t, valid), "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "hashistack" || len(m.Steps) != 2 || m.Steps[0].Ansible.Playbook != "ansible/provision.yml" ||
		m.Steps[1].Tofu.Policy == nil || !m.Steps[1].Confirm || m.Commands["output"].Tofu.Action != "output" ||
		m.Secrets[0].Generate != "ca" || m.Check.Answers != "test/answers.yaml" || m.Server.FirstUser != "root" || len(m.Check.Steps) != 1 || !m.Steps[1].Tunnel ||
		m.Backup == nil || m.Backup.Keep["daily"] != "30" {
		t.Errorf("got %+v", m)
	}
}

func TestProblems(t *testing.T) {
	tests := []struct {
		name string
		from string
		to   string
		want string
		at   string // text of the line the problem is reported on
	}{
		{"unknown field", "steps:", "stpes: []\nsteps:", "unknown field stpes", "stpes"},
		{"newer contract", "damstack/v1", "damstack/v2", "newer than this damstack knows", "apiVersion"},
		{"not a damstack manifest", "damstack/v1", "v1", "must be damstack/v1", "apiVersion"},
		{"bad name", "name: hashistack", "name: Hashi Stack", "lowercase letters", "name: Hashi"},
		{"needs a newer damstack", `damstack: ">=0.1.0"`, `damstack: ">=0.9.0"`, "update damstack", `damstack: ">=0.9.0"`},
		{"bad toolbox constraint", `">=1.0.0, <2.0.0"`, `"latest"`, "not a version constraint", "toolbox"},
		{"unknown host check", "[docker, ssh-key, wireguard]", "[docker, kubernetes]", `"kubernetes" is not a check`, "host:"},
		{"enum default outside the options", "default: ssh", "default: hetzner", "must be one of the options", "default: hetzner"},
		{"when on a non-bool question", "when: mail", "when: provider", `"provider" is not a bool question`, "when: provider"},
		{"when on an option that is not there", "when: mail", "when: provider=hetzner", `"hetzner" is not an option of provider`, "when: provider=hetzner"},
		{"when on a later question", "when: mail", "when: mail_hostname", `"mail_hostname" is not a question asked before this`, "when: mail_hostname"},
		{"secret when on no question", "    bytes: 32", "    bytes: 32\n    when: nothing", `"nothing" is not a question`, "when: nothing"},
		{"secret both generated and asked", "generate: hex\n", "generate: hex\n    ask: A passphrase\n", "exactly one of generate and ask", "- name: state_passphrase"},
		{"secret neither generated nor asked", "    ask: An API token\n", "", "exactly one of generate and ask", "- name: api_token"},
		{"unknown generator", "generate: hex", "generate: random", `"random" is not a generator`, "generate: random"},
		{"cert on a non-ca secret", "bytes: 32", "bytes: 32\n    cert: x.pem", "for a generated ca only", "cert: x.pem"},
		{"missing config template", "config: template/stack.yaml.tmpl", "config: template/other.tmpl", "template/other.tmpl does not exist", "config:"},
		{"questions without config", "config: template/stack.yaml.tmpl\n", "", "config: is required with questions", ""},
		{"duplicate step", "name: apply", "name: provision", `"provision" is already a step`, "- name: provision\n    tofu"},
		{"step with two tools", "    confirm: true\n", "    confirm: true\n    run: [bin/extra]\n", "exactly one of ansible, tofu and run", "- name: apply"},
		{"step with no tool", "    ansible:\n      playbook: ansible/provision.yml\n      inventory: ansible/inventory\n", "", "exactly one of ansible, tofu and run", "- name: provision"},
		{"missing playbook", "playbook: ansible/provision.yml", "playbook: ansible/missing.yml", "ansible/missing.yml does not exist", "playbook: ansible/missing.yml"},
		{"inventory that is a file", "inventory: ansible/inventory", "inventory: ansible/provision.yml", "is not a directory", "inventory:"},
		{"unknown tofu action", "action: apply", "action: destroy", `"destroy" is not an action`, "action: destroy"},
		{"missing policy dir", "policy: {dir: policy,", "policy: {dir: policies,", "policies does not exist", "policy:"},
		{"env template that does not parse", "https://{{ firstHost .config.network.cidr }}", "https://{{ firstHost .config.network.cidr", "expected :=", "NOMAD_ADDR"},
		{"keep outside the project", "file: .damstack/work/vault-init.json", "file: ../vault-init.json", "a path inside the project", "file: ../vault-init.json"},
		{"command shadowing a damstack command", "backup-pull:", "deploy:", `"deploy" is a command of damstack itself`, "deploy:"},
		{"run program outside the stack", "run: [bin/extra]", "run: [../other/tool]", "leaves the stack directory", "run: [../other/tool]"},
		{"no check answers", "  answers: test/answers.yaml\n", "", "check.answers: is required", ""},
		{"check step with no tool", "      ansible: {playbook: ansible/provision.yml, inventory: ansible/inventory}\n", "", "exactly one of ansible, tofu and run", "- name: resolve"},
		{"missing check answers", "answers: test/answers.yaml", "answers: test/other.yaml", "test/other.yaml does not exist", "answers:"},
		{"secret named as a question", "name: api_token", "name: mail", `"mail" is already a question`, "- name: mail\n    ask"},
		{"question named project", "name: provider", "name: project", "damstack asks itself", "name: project\n"},
		{"server without first user", "  first_user: root\n", "", "server.first_user: is required", "server:"},
		{"server template that does not parse", "{{ .config.server.address }}", "{{ .config.server.address", "unclosed action", "  address:"},
		{"tunnel step without a tunnel", "  tunnel: \"{{ firstHost .config.network.cidr }}\"\n", "", "needs server.tunnel", "    tunnel: true"},
		{"backup without a password", "  password: '{{ secret \"backup_password\" }}'\n", "", "backup.password: is required", "backup:"},
		{"backup keeping an unknown count", "weekly: \"{{", "fortnightly: \"{{", "not a count restic keeps", "  keep:"},
		{"backup template that does not parse", "to: ~/Backups/{{ .config.name }}", "to: ~/Backups/{{ .config.name", "unclosed action", "  to:"},
		{"no steps", "steps:\n", "steps: []\nold_steps:\n", "unknown field old_steps", "old_steps"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(valid, tc.from) {
				t.Fatalf("the valid manifest has no %q", tc.from)
			}
			doc := strings.Replace(valid, tc.from, tc.to, 1)
			_, err := Load(stackDir(t, doc), "0.1.0")
			var merr *Error
			if !errors.As(err, &merr) {
				t.Fatalf("want a manifest error, got %v", err)
			}
			for _, p := range merr.Problems {
				if strings.Contains(p.String(), tc.want) {
					if tc.at != "" && p.Line != textLine(doc, tc.at) {
						t.Errorf("%q at line %d, want line %d (%q)", tc.want, p.Line, textLine(doc, tc.at), tc.at)
					}
					return
				}
			}
			t.Errorf("no problem with %q in:\n%v", tc.want, err)
		})
	}
}

func TestConfigTemplateThatDoesNotParse(t *testing.T) {
	dir := stackDir(t, valid)
	if err := os.WriteFile(filepath.Join(dir, "template/stack.yaml.tmpl"), []byte("name: {{ .project_name\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir, "0.1.0"); err == nil || !strings.Contains(err.Error(), "unclosed action") {
		t.Errorf("got %v", err)
	}
}

func TestNotExecutable(t *testing.T) {
	dir := stackDir(t, valid)
	if err := os.Chmod(filepath.Join(dir, "bin/extra"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir, "0.1.0"); err == nil || !strings.Contains(err.Error(), "bin/extra is not executable") {
		t.Errorf("got %v", err)
	}
}

func TestSatisfies(t *testing.T) {
	tests := []struct {
		version, constraint string
		want                bool
	}{
		{"0.3.0", ">=0.3.0", true},
		{"0.2.9", ">=0.3.0", false},
		{"v1.4.0", ">=1.0.0, <2.0.0", true},
		{"2.0.0", ">=1.0.0, <2.0.0", false},
		{"1.0.0", "=1.0.0", true},
		{"dev", ">=9.0.0", true},
	}
	for _, tc := range tests {
		got, err := Satisfies(tc.version, tc.constraint)
		if err != nil || got != tc.want {
			t.Errorf("Satisfies(%q, %q) = %v, %v; want %v", tc.version, tc.constraint, got, err, tc.want)
		}
	}
	if _, err := Satisfies("1.0.0", "~1.0"); err == nil {
		t.Error("~1.0 was accepted")
	}
}

const validApp = `apiVersion: damstack/v1
name: mail
kind: app
description: Mail on the platform
requires:
  provides: [nomad, vault-kv]
questions:
  - name: hostname
    prompt: The mail host name
secrets:
  - name: mail_admin_password
    generate: password
config: template/stack.yaml.tmpl
targets:
  nomad:
    steps:
      - name: apply
        tofu:
          dir: infra
          action: apply
          outputs: {dns_records: dns/mail.json}
        confirm: true
    commands:
      output:
        tofu: {dir: infra, action: output}
check:
  answers: test/answers.yaml
  platform: test/answers.yaml
`

func TestValidAppLoads(t *testing.T) {
	m, err := Load(stackDir(t, validApp), "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	name, target, ok := m.Target([]string{"consul", "nomad", "vault-kv"})
	if !m.IsApp() || !ok || name != "nomad" || target.Steps[0].Tofu.Outputs["dns_records"] != "dns/mail.json" || m.SecretPrefix() != "mail_" {
		t.Errorf("got %+v, target %s %+v", m, name, target)
	}
	if _, _, ok := m.Target([]string{"kubernetes"}); ok {
		t.Error("a target for a platform that does not provide it")
	}
}

func TestAppProblems(t *testing.T) {
	tests := []struct{ name, from, to, want string }{
		{"unknown kind", "kind: app", "kind: service", `"service" is not a kind`},
		{"app with steps", "targets:", "steps:\n  - name: x\n    run: [bin/extra]\ntargets:", "under targets"},
		{"app without targets", "targets:\n  nomad:", "other:\n  nomad:", "unknown field other"},
		{"app without requires", "  provides: [nomad, vault-kv]\n", "", "requires.provides: is required"},
		{"secret without the app's name", "name: mail_admin_password", "name: admin_password", "begin with its name: mail_"},
		{"app step after apps", "        confirm: true", "        after_apps: true", "is for a step of a platform"},
		{"output outside the project", "dns/mail.json", "../mail.json", "a path inside the project"},
		{"app with a backup", "kind: app", "kind: app\nbackup: {from: x, to: y, password: z}", "is the platform's; it backs up"},
		{"no platform to check on", "  platform: test/answers.yaml\n", "", "check.platform: is required"},
		{"target without steps", "    steps:\n      - name: apply\n        tofu:\n          dir: infra\n          action: apply\n          outputs: {dns_records: dns/mail.json}\n        confirm: true\n", "", "targets.nomad.steps: at least one"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(validApp, tc.from) {
				t.Fatalf("the valid app has no %q", tc.from)
			}
			_, err := Load(stackDir(t, strings.Replace(validApp, tc.from, tc.to, 1)), "0.1.0")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %v, want %q", err, tc.want)
			}
		})
	}
}

func TestPlatformProblems(t *testing.T) {
	for _, tc := range []struct{ from, to, want string }{
		{"steps:\n  - name: provision", "targets:\n  nomad: {steps: []}\nsteps:\n  - name: provision", "are for an app"},
		{"  host: [docker, ssh-key, wireguard]", "  host: [docker, ssh-key, wireguard]\n  provides: [nomad]", "is for an app"},
	} {
		_, err := Load(stackDir(t, strings.Replace(valid, tc.from, tc.to, 1)), "0.1.0")
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: got %v", tc.want, err)
		}
	}
}

func TestWhenOnAnOption(t *testing.T) {
	doc := strings.Replace(valid, "when: mail", "when: provider=ovh", 1)
	if _, err := Load(stackDir(t, doc), "0.1.0"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		when    string
		answers map[string]any
		want    bool
	}{
		{"", nil, true},
		{"mail", map[string]any{"mail": true}, true},
		{"mail", map[string]any{"mail": false}, false},
		{"provider=ovh", map[string]any{"provider": "ovh"}, true},
		{"provider=ovh", map[string]any{"provider": "ssh"}, false},
	} {
		if got := Holds(tc.when, tc.answers); got != tc.want {
			t.Errorf("Holds(%q, %v) = %v", tc.when, tc.answers, got)
		}
	}
}
