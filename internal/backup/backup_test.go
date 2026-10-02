package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eugene-panin/damstack/internal/manifest"
)

func TestResolve(t *testing.T) {
	config := map[string]any{"backup": map[string]any{"path": "/var/backups/restic", "daily": 30, "every": "1h"}}
	b := &manifest.Backup{
		From:     "{{ .config.backup.path }}",
		To:       "~/Backups/demo",
		Password: `{{ secret "pass" }}`,
		Keep:     map[string]string{"weekly": "0", "daily": "{{ .config.backup.daily }}", "last": "3"},
		Every:    "{{ .config.backup.every }}",
	}
	p, err := Resolve(b, config, map[string]any{"pass": "s3cret"}, "/project")
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	if p.From != "/var/backups/restic" || p.To != filepath.Join(home, "Backups/demo") || p.Password != "s3cret" || p.Every != time.Hour {
		t.Errorf("got %+v", p)
	}
	if got := strings.Join(p.Keep, " "); got != "--keep-last 3 --keep-daily 30" {
		t.Errorf("keep: %s", got)
	}

	b.To = "backups"
	if p, _ := Resolve(b, config, map[string]any{"pass": "s3cret"}, "/project"); p.To != "/project/backups" {
		t.Errorf("a relative to: %s", p.To)
	}
	if _, err := Resolve(b, config, nil, "/project"); err == nil || !strings.Contains(err.Error(), "no password") {
		t.Errorf("no password: %v", err)
	}
	b.Keep["daily"] = "many"
	if _, err := Resolve(b, config, map[string]any{"pass": "x"}, "/project"); err == nil || !strings.Contains(err.Error(), "not a count") {
		t.Errorf("a keep that is not a count: %v", err)
	}
}

func TestParseEvery(t *testing.T) {
	for in, want := range map[string]time.Duration{"off": 0, "": 0, "false": 0, "30m": 30 * time.Minute, "2h": 2 * time.Hour} {
		if got, err := ParseEvery(in); err != nil || got != want {
			t.Errorf("%q: got %v, %v", in, got, err)
		}
	}
	for _, d := range []time.Duration{30 * time.Minute, 2 * time.Hour, 90 * time.Minute} {
		if got, _ := ParseEvery(FormatEvery(d)); got != d {
			t.Errorf("%v says %s", d, FormatEvery(d))
		}
	}
	for _, in := range []string{"1d", "0m", "1.5h", "hourly"} {
		if _, err := ParseEvery(in); err == nil {
			t.Errorf("%q: no error", in)
		}
	}
}

func TestSFTP(t *testing.T) {
	s := SFTP("ops", "10.77.0.1", "/var/backups/restic", "/home/me/my projects/known_hosts", "/home/me/.ssh/id_ed25519")
	if s.Repo != "sftp:ops@10.77.0.1:/var/backups/restic" {
		t.Errorf("repo: %s", s.Repo)
	}
	cmd := strings.Join(s.Options, " ")
	for _, want := range []string{"UserKnownHostsFile='/home/me/my projects/known_hosts'", "StrictHostKeyChecking=accept-new",
		"-i /home/me/.ssh/id_ed25519", "ops@10.77.0.1 sudo /usr/lib/openssh/sftp-server"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("no %q in %s", want, cmd)
		}
	}
}

func TestPlist(t *testing.T) {
	j := Job{Project: "ovh", Args: []string{"/opt/homebrew/bin/damstack", "backup", "pull", "ovh", "--scheduled"},
		Env: map[string]string{"PATH": "/usr/bin:/bin", "DAMSTACK_HOME": "/a&b"}, Every: time.Hour}
	got := string(Plist(j, "/log"))
	for _, want := range []string{"<string>dev.damstack.backup.ovh</string>", "<string>--scheduled</string>",
		"<key>DAMSTACK_HOME</key>\n\t\t<string>/a&amp;b</string>", "<integer>3600</integer>", "<key>RunAtLoad</key>\n\t<true/>"} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in\n%s", want, got)
		}
	}
	service, timer := Units(j)
	if !strings.Contains(string(service), `ExecStart="/opt/homebrew/bin/damstack" "backup" "pull" "ovh" "--scheduled"`) ||
		!strings.Contains(string(service), `Environment="DAMSTACK_HOME=/a&b"`) || !strings.Contains(string(timer), "OnUnitActiveSec=3600s") {
		t.Errorf("units:\n%s\n%s", service, timer)
	}
}

func TestState(t *testing.T) {
	dir := t.TempDir()
	if s, err := LoadState(dir); err != nil || !s.Pulled.IsZero() {
		t.Fatalf("a project never pulled: %+v, %v", s, err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".damstack"), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Round(time.Second)
	latest := now.Add(-time.Hour)
	if _, err := SaveState(dir, now, []Snapshot{{Time: latest.Add(-time.Hour)}, {Time: latest}}); err != nil {
		t.Fatal(err)
	}
	s, err := LoadState(dir)
	if err != nil || !s.Pulled.Equal(now) || !s.Latest.Equal(latest) || s.Snapshots != 2 {
		t.Errorf("got %+v, %v", s, err)
	}
}
