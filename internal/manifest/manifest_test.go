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
steps:
  - name: secrets
    run: [bin/stack, secrets]
    once: true
  - name: apply
    run: [make, infra-apply]
    confirm: true
commands:
  backup-pull: [bin/stack, backup, pull]
check: [bin/check]
`

func stackDir(t *testing.T, manifest string, executables ...string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, File), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range executables {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestValidManifestLoads(t *testing.T) {
	m, err := Load(stackDir(t, valid, "bin/stack", "bin/check"), "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "hashistack" || len(m.Steps) != 2 || !m.Steps[1].Confirm || m.Commands["backup-pull"][2] != "pull" {
		t.Errorf("got %+v", m)
	}
}

func TestProblems(t *testing.T) {
	tests := []struct {
		name  string
		edit  func(string) string
		files []string
		want  string
		line  int
	}{
		{
			name: "unknown field",
			edit: func(s string) string { return strings.Replace(s, "steps:", "stpes: []\nsteps:", 1) },
			want: "unknown field stpes", line: 21,
		},
		{
			name: "newer contract",
			edit: func(s string) string { return strings.Replace(s, "damstack/v1", "damstack/v2", 1) },
			want: "newer than this damstack knows", line: 1,
		},
		{
			name: "not a damstack manifest",
			edit: func(s string) string { return strings.Replace(s, "damstack/v1", "v1", 1) },
			want: "must be damstack/v1", line: 1,
		},
		{
			name: "bad name",
			edit: func(s string) string { return strings.Replace(s, "name: hashistack", "name: Hashi Stack", 1) },
			want: "lowercase letters", line: 2,
		},
		{
			name: "needs a newer damstack",
			edit: func(s string) string { return strings.Replace(s, `damstack: ">=0.1.0"`, `damstack: ">=0.9.0"`, 1) },
			want: "update damstack", line: 5,
		},
		{
			name: "bad toolbox constraint",
			edit: func(s string) string { return strings.Replace(s, `">=1.0.0, <2.0.0"`, `"latest"`, 1) },
			want: "not a version constraint", line: 6,
		},
		{
			name: "unknown host check",
			edit: func(s string) string {
				return strings.Replace(s, "[docker, ssh-key, wireguard]", "[docker, kubernetes]", 1)
			},
			want: `"kubernetes" is not a check`, line: 7,
		},
		{
			name: "enum default outside the options",
			edit: func(s string) string { return strings.Replace(s, "default: ssh", "default: hetzner", 1) },
			want: "must be one of the options", line: 16,
		},
		{
			name: "bool question with a string default",
			edit: func(s string) string { return strings.Replace(s, "default: false", "default: nope", 1) },
			want: "must be true or false", line: 20,
		},
		{
			name: "duplicate step",
			edit: func(s string) string { return strings.Replace(s, "name: apply", "name: secrets", 1) },
			want: `"secrets" is already a step`, line: 25,
		},
		{
			name:  "missing program",
			edit:  func(s string) string { return s },
			files: []string{"bin/check"},
			want:  "bin/stack does not exist in the stack", line: 23,
		},
		{
			name: "program outside the stack",
			edit: func(s string) string { return strings.Replace(s, "check: [bin/check]", "check: [../other/check]", 1) },
			want: "leaves the stack directory", line: 30,
		},
		{
			name: "command shadowing a damstack command",
			edit: func(s string) string { return strings.Replace(s, "backup-pull:", "deploy:", 1) },
			want: `"deploy" is a command of damstack itself`, line: 29,
		},
		{
			name: "no check",
			edit: func(s string) string { return strings.Replace(s, "check: [bin/check]\n", "", 1) },
			want: "check: is required",
		},
		{
			name: "no steps",
			edit: func(s string) string {
				start := strings.Index(s, "steps:")
				end := strings.Index(s, "commands:")
				return s[:start] + "steps: []\n" + s[end:]
			},
			want: "at least one step",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			files := tc.files
			if files == nil {
				files = []string{"bin/stack", "bin/check"}
			}
			_, err := Load(stackDir(t, tc.edit(valid), files...), "0.1.0")
			var merr *Error
			if !errors.As(err, &merr) {
				t.Fatalf("want a manifest error, got %v", err)
			}
			for _, p := range merr.Problems {
				if strings.Contains(p.String(), tc.want) {
					if tc.line != 0 && p.Line != tc.line {
						t.Errorf("%q at line %d, want line %d", tc.want, p.Line, tc.line)
					}
					return
				}
			}
			t.Errorf("no problem with %q in:\n%v", tc.want, err)
		})
	}
}

func TestNotExecutable(t *testing.T) {
	dir := stackDir(t, valid, "bin/stack", "bin/check")
	if err := os.Chmod(filepath.Join(dir, "bin/check"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir, "0.1.0"); err == nil || !strings.Contains(err.Error(), "bin/check is not executable") {
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
