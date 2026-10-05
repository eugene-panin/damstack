package toolbox

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnvironTakesNothingOfTheUserButThePassthrough(t *testing.T) {
	t.Setenv("TF_VAR_greeting", "poison")
	t.Setenv("ANSIBLE_CONFIG", "/poison/ansible.cfg")
	t.Setenv("PYTHONPATH", "/poison")
	t.Setenv("NOMAD_TOKEN", "poison")
	t.Setenv("PATH", "/poison/bin:/usr/bin")
	t.Setenv("HTTPS_PROXY", "http://proxy:3128")
	t.Setenv("TMPDIR", "/short")
	r := &Runner{Toolbox: "/tb", Stack: "/s", Project: "/p", Password: "/c/vault-pass", Key: "/h/.ssh/id_ed25519", Cache: "/cache"}
	env := strings.Join(r.Environ(Cmd{Env: map[string]string{"ANSIBLE_CONFIG": "/s/ansible.cfg", "TF_VAR_x": "1"}}), "\n")
	for _, want := range []string{"PATH=/tb/bin:/usr/bin:/bin:/usr/sbin:/sbin", "ANSIBLE_CONFIG=/s/ansible.cfg", "TF_VAR_x=1",
		"TF_CLI_CONFIG_FILE=/tb/etc/tofurc", "TF_PLUGIN_CACHE_DIR=/cache/tofu-plugins", "ANSIBLE_HOME=/p/.damstack/work/ansible",
		"DAMSTACK_PROJECT=/p", "DAMSTACK_SSH_KEY=/h/.ssh/id_ed25519", "ANSIBLE_SSH_CONTROL_PATH_DIR=/short/damstack-cp", "HTTPS_PROXY=http://proxy:3128", "LANG=en_US.UTF-8"} {
		if !strings.Contains(env, want+"\n") && !strings.HasSuffix(env, want) {
			t.Errorf("no %s in\n%s", want, env)
		}
	}
	if strings.Contains(env, "poison") {
		t.Errorf("a variable of the user reached the command:\n%s", env)
	}
}

func TestRun(t *testing.T) {
	tb := t.TempDir()
	os.MkdirAll(filepath.Join(tb, "bin"), 0o755)
	os.WriteFile(filepath.Join(tb, "bin", "probe"), []byte("#!/bin/sh\necho \"$DAMSTACK_PROJECT $PWD $TF_VAR_greeting\"\nexit \"${1:-0}\"\n"), 0o755)
	project := t.TempDir()
	t.Setenv("TF_VAR_greeting", "poison")
	var out, errOut bytes.Buffer
	r := &Runner{Toolbox: tb, Project: project, Stdout: &out, Stderr: &errOut}
	if err := r.Run(context.Background(), Cmd{Args: []string{"probe"}}); err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(project)
	if got := strings.TrimSpace(out.String()); got != project+" "+real && got != project+" "+project {
		t.Errorf("got %q", got)
	}
	err := r.Run(context.Background(), Cmd{Args: []string{"probe", "3"}, Quiet: true})
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 3 || exit.Name != "probe" || !strings.Contains(errOut.String(), project) {
		t.Errorf("got %v, stderr %q", err, errOut.String())
	}
	if _, err := r.Path("no-such-tool"); err == nil {
		t.Error("a tool that is nowhere was found")
	}
}

func archive(t *testing.T, entries ...*tar.Header) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, h := range entries {
		if h.Typeflag == tar.TypeReg {
			h.Size = int64(len(h.Name))
		}
		tw.WriteHeader(h)
		if h.Typeflag == tar.TypeReg {
			tw.Write([]byte(h.Name))
		}
	}
	tw.Close()
	gz.Close()
	return &buf
}

func TestUnpack(t *testing.T) {
	dest := t.TempDir()
	err := unpack(archive(t,
		&tar.Header{Name: "./", Typeflag: tar.TypeDir, Mode: 0o755},
		&tar.Header{Name: "./python/bin/python3.13", Typeflag: tar.TypeReg, Mode: 0o755},
		&tar.Header{Name: "./python/bin/python3", Typeflag: tar.TypeSymlink, Linkname: "python3.13"},
	), dest)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(dest, "python/bin/python3")); err != nil || info.Mode().Perm() != 0o755 {
		t.Errorf("the link: %v, %v", info, err)
	}
	for _, bad := range []*tar.Header{
		{Name: "../escape", Typeflag: tar.TypeReg, Mode: 0o644},
		{Name: "bin/link", Typeflag: tar.TypeSymlink, Linkname: "../../etc/passwd"},
		{Name: "bin/abs", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
	} {
		if err := unpack(archive(t, bad), t.TempDir()); err == nil {
			t.Errorf("%s -> %s was unpacked", bad.Name, bad.Linkname)
		}
	}
}
