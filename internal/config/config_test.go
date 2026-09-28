package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAddStackSaveAndLoad(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if added, err := cfg.AddStack(Stack{Name: "demo", URL: "https://github.com/o/demo"}); err != nil || !added {
		t.Fatalf("add: %v, %v", added, err)
	}
	if added, err := cfg.AddStack(Stack{Name: "demo", URL: "https://github.com/o/demo"}); err != nil || added {
		t.Errorf("the same stack again: %v, %v; want not added, no error", added, err)
	}
	if _, err := cfg.AddStack(Stack{Name: "demo", URL: "https://github.com/other/demo"}); err == nil || !strings.Contains(err.Error(), "--name") {
		t.Errorf("another stack under a taken name: %v", err)
	}
	if _, err := cfg.AddStack(Stack{Name: "hashi", URL: "https://github.com/o/fake"}); err == nil {
		t.Error("a stack took the name of a builtin one")
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	again, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	all := again.AllStacks()
	if len(all) != 2 || all[0].Name != "hashi" || !all[0].Builtin || all[1].Name != "demo" || all[1].Builtin {
		t.Errorf("got %+v", all)
	}
	data, _ := os.ReadFile(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "damstack", "config.yaml"))
	if strings.Contains(string(data), "hashi") {
		t.Errorf("a builtin stack was written to the config:\n%s", data)
	}
}

func TestDirDefaultsToDotConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/home/u")
	if dir, err := Dir(); err != nil || dir != "/home/u/.config/damstack" {
		t.Errorf("Dir() = %s, %v", dir, err)
	}
}
