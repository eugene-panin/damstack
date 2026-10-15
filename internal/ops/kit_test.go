package ops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eugene-panin/damstack/internal/backup"
	"github.com/eugene-panin/damstack/internal/config"
	"github.com/eugene-panin/damstack/internal/kit"
	"github.com/eugene-panin/damstack/internal/project"
)

// otherComputer gives the test a damstack of its own again, with a library
// that needs no fetching.
func otherComputer(t *testing.T) {
	t.Helper()
	home(t)
	cache, err := config.CacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "library.yaml"), []byte("stacks:\n  - {name: demo, url: https://example.org/demo.git}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// madeKit is the recovery kit of a project MakeKit made, its passphrase, and
// the vault password it holds.
func madeKit(t *testing.T) (file, phrase, password string) {
	t.Helper()
	p, password := newProject(t, "greeting: hi\n")
	if err := p.SaveSecrets(password, map[string]any{"token": "abc"}); err != nil {
		t.Fatal(err)
	}
	ui := &fakeUI{}
	to := t.TempDir()
	if err := MakeKit(ui, p, to); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(to, "demo-kit-*.age"))
	if len(files) != 1 {
		t.Fatalf("kits made: %v", files)
	}
	lines := strings.Split(ui.said(), "\n")
	for i, line := range lines {
		if line == "Its passphrase, shown only now:" && i+2 < len(lines) {
			phrase = strings.TrimSpace(lines[i+2])
		}
	}
	if phrase == "" {
		t.Fatalf("no passphrase in %q", ui.said())
	}
	st, err := backup.LoadState(p.Dir)
	if err != nil || st.Kit.IsZero() {
		t.Fatalf("the kit is recorded: %+v, %v", st, err)
	}
	if warn := KitWarning(p, st); warn != "" {
		t.Errorf("a project with a fresh kit is warned: %q", warn)
	}
	return files[0], phrase, password
}

func given(phrase string) func() (string, error) {
	return func() (string, error) { return phrase, nil }
}

func TestARecoveryKitBringsTheProjectBackOnAnotherComputer(t *testing.T) {
	file, phrase, password := madeKit(t)
	otherComputer(t)
	ui := &fakeUI{}
	if err := OpenKit(context.Background(), ui, file, "", given(" "+strings.ToLower(phrase)+"\n")); err != nil {
		t.Fatal(err)
	}
	projects, _ := config.ProjectsDir()
	dest := filepath.Join(projects, "demo")
	p, err := project.Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := p.Password(); got != password {
		t.Errorf("the vault password is %q, want %q", got, password)
	}
	if secrets, err := p.Secrets(password); err != nil || secrets["token"] != "abc" {
		t.Errorf("secrets %v, %v", secrets, err)
	}
	if cfg, err := p.Config(); err != nil || cfg["greeting"] != "hi" {
		t.Errorf("config %v, %v", cfg, err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if known, ok := cfg.Project("demo"); !ok || known.Path != dest {
		t.Errorf("the project is known to damstack: %+v", cfg.Projects)
	}
	if !strings.Contains(ui.said(), "demo is back, in "+dest) {
		t.Errorf("said %q", ui.said())
	}
	leftovers, _ := filepath.Glob(filepath.Join(projects, ".opening-*"))
	if len(leftovers) != 0 {
		t.Errorf("left behind %v", leftovers)
	}

	err = OpenKit(context.Background(), &fakeUI{}, file, "", given(phrase))
	if err == nil || err.Error() != "demo is a project on this computer already, in "+dest+"; damstack does not write over it" {
		t.Errorf("a second open: %v", err)
	}
}

func TestARecoveryKitIsNotOpenedOverWhatIsThere(t *testing.T) {
	file, phrase, _ := madeKit(t)
	for _, c := range []struct {
		name   string
		phrase string
		before func(t *testing.T) string
		want   func(into, pass string) string
	}{
		{"a wrong passphrase", "WRONG-WRONG-WRONG", nil, nil},
		{"a directory that is there", phrase, func(t *testing.T) string {
			into := filepath.Join(t.TempDir(), "demo")
			if err := os.Mkdir(into, 0o755); err != nil {
				t.Fatal(err)
			}
			return into
		}, func(into, _ string) string { return into + " is there already; damstack does not write over it" }},
		{"another vault password", phrase, func(t *testing.T) string {
			pass, _ := project.PasswordPath("demo")
			if err := os.MkdirAll(filepath.Dir(pass), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(pass, []byte("another\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return ""
		}, func(_, pass string) string {
			return "another vault password of demo is at " + pass + "; move it away first"
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			otherComputer(t)
			into := ""
			if c.before != nil {
				into = c.before(t)
			}
			err := OpenKit(context.Background(), &fakeUI{}, file, into, given(c.phrase))
			pass, _ := project.PasswordPath("demo")
			switch {
			case c.want == nil:
				if !errors.Is(err, kit.ErrPassphrase) {
					t.Errorf("got %v", err)
				}
			case err == nil || err.Error() != c.want(into, pass):
				t.Errorf("got %v, want %q", err, c.want(into, pass))
			}
			projects, _ := config.ProjectsDir()
			if _, err := os.Stat(filepath.Join(projects, "demo")); err == nil {
				t.Error("the project was opened")
			}
			if cfg, _ := config.Load(); len(cfg.Projects) != 0 {
				t.Errorf("projects known: %+v", cfg.Projects)
			}
		})
	}
}

func TestAProjectWithoutAFreshKitIsWarned(t *testing.T) {
	p, _ := newProject(t, "{}\n")
	changed := time.Date(2026, 9, 1, 12, 0, 0, 0, time.Local)
	if err := os.Chtimes(filepath.Join(p.Dir, project.ConfigFile), changed, changed); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(p.Dir, project.MetaFile), changed, changed); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		kit  time.Time
		want string
	}{
		{"none", time.Time{}, "demo has no recovery kit: without this computer, nothing brings the project back. damstack backup kit demo makes one."},
		{"older", changed.Add(-24 * time.Hour), "demo changed since its recovery kit of 2026-08-31: damstack backup kit demo makes a new one; keep it instead of the old."},
		{"newer", changed.Add(time.Hour), ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := KitWarning(p, backup.State{Kit: c.kit}); got != c.want {
				t.Errorf("got %q", got)
			}
		})
	}
}
