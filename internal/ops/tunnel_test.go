package ops

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/project"
	"github.com/eugene-panin/damstack/internal/wg"
)

func TestDevices(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"laptop", "phone"} {
		conf := "[Interface]\nPrivateKey = key-of-" + name + "\n"
		if err := os.MkdirAll(filepath.Join(dir, "clients"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "clients", name+".conf"), []byte(conf), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	devices(&out, &project.Project{Dir: dir}, &manifest.Server{TunnelConfigs: "clients/*.conf"}, "turn it on")
	text := out.String()
	if !strings.Contains(text, "This computer: import "+filepath.Join(dir, "clients", "laptop.conf")) ||
		!strings.Contains(text, "phone: scan this in the WireGuard app") || !strings.Contains(text, "█") {
		t.Errorf("got\n%s", text)
	}
	if strings.Contains(text, "laptop: scan") {
		t.Error("the configuration to import is also a code")
	}

	out.Reset()
	devices(&out, &project.Project{Dir: t.TempDir()}, &manifest.Server{TunnelConfigs: "clients/*.conf"}, "turn it on")
	if !strings.Contains(out.String(), "turn it on") {
		t.Errorf("without configurations: %s", out.String())
	}
}

type tunnelRun struct {
	p        *project.Project
	password string
	ui       *fakeUI
	runner   *fakeRunner
	t        *Tunnel
}

func newTunnel(t *testing.T, devices string) *tunnelRun {
	t.Helper()
	p, password := newProject(t, "# the devices that reach the server\nnetwork:\n  clients: ["+devices+"]\n")
	m := &manifest.Manifest{Name: "demo",
		Server: &manifest.Server{Address: "203.0.113.10", OpsUser: "ops", WireGuard: &manifest.WireGuard{
			Secret: "wireguard", Devices: "network.clients", Network: "10.77.0.0/24", Endpoint: "203.0.113.10:51820", Apply: "wireguard"}},
		Commands: map[string]manifest.Step{"wireguard": runStep("set-keys")},
	}
	r := &tunnelRun{p: p, password: password, ui: &fakeUI{yes: true}, runner: &fakeRunner{do: map[string]func(context.Context) error{}}}
	fakeEngines(t, r.runner)
	if _, err := EnsureKeys(p, m, password); err != nil {
		t.Fatal(err)
	}
	var err error
	if r.t, err = OpenTunnel(r.ui, true, p, m, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *tunnelRun) devices(t *testing.T) []string {
	t.Helper()
	config, err := r.p.Config()
	if err != nil {
		t.Fatal(err)
	}
	names, err := project.List(config, "network.clients")
	if err != nil {
		t.Fatal(err)
	}
	return names
}

func (r *tunnelRun) keys(t *testing.T) *wg.State {
	t.Helper()
	secrets, err := r.p.Secrets(r.password)
	if err != nil {
		t.Fatal(err)
	}
	st, err := wg.FromSecret(secrets["wireguard"])
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestAddingADeviceGivesItKeysAndSetsThemOnTheServer(t *testing.T) {
	r := newTunnel(t, "laptop")
	if err := r.t.Add(context.Background(), "phone"); err != nil {
		t.Fatal(err)
	}
	if got := r.devices(t); !slices.Equal(got, []string{"laptop", "phone"}) {
		t.Errorf("stack.yaml lists %v", got)
	}
	if data, _ := os.ReadFile(filepath.Join(r.p.Dir, project.ConfigFile)); !strings.HasPrefix(string(data), "# the devices that reach the server\n") {
		t.Errorf("the rest of stack.yaml stays: %s", data)
	}
	st := r.keys(t)
	if st.Devices["phone"] == nil || st.Devices["laptop"] == nil || st.Devices["phone"].Address == st.Devices["laptop"].Address {
		t.Errorf("vault.yml keeps a key and an address of its own for each: %+v", st.Devices)
	}
	if got := r.runner.steps(); got != "set-keys" {
		t.Errorf("the keys are set on the server: ran %q", got)
	}
	if !strings.Contains(r.ui.said(), "phone is on the private network at "+st.Devices["phone"].Address) {
		t.Errorf("said %q", r.ui.said())
	}
	if entries, _ := r.p.History(); len(entries) != 1 || entries[0].Command != "tunnel wireguard" || entries[0].Result != project.OK {
		t.Errorf("history: %+v", entries)
	}
	if err := r.t.Add(context.Background(), "phone"); err == nil || err.Error() != "phone is a device of demo already" {
		t.Errorf("got %v", err)
	}
}

func TestRemovingADeviceTheServerDidNotHearOfStillLetsItIn(t *testing.T) {
	r := newTunnel(t, "laptop, phone")
	if err := r.t.Remove(context.Background(), "tablet"); err == nil || err.Error() != "tablet is not a device of demo; damstack tunnel list shows them" {
		t.Errorf("got %v", err)
	}
	r.ui.yes = false
	if err := r.t.Remove(context.Background(), "phone"); err != nil || len(r.devices(t)) != 2 || r.runner.steps() != "" {
		t.Fatalf("a no keeps the device: %v, %v", err, r.devices(t))
	}
	r.ui.yes = true
	r.runner.do["set-keys"] = func(context.Context) error { return errors.New("the server does not answer") }
	err := r.t.Remove(context.Background(), "phone")
	want := "the server does not answer; phone left stack.yaml, but the server still lets it in until damstack deploy demo sets its keys there"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v", err)
	}
	if got := r.devices(t); !slices.Equal(got, []string{"laptop"}) {
		t.Errorf("stack.yaml lists %v", got)
	}
	if st := r.keys(t); st.Devices["phone"] != nil || st.Devices["laptop"] == nil {
		t.Errorf("vault.yml keeps the keys of %v", st.Devices)
	}
	if entries, _ := r.p.History(); len(entries) != 1 || entries[0].Result != project.Failed {
		t.Errorf("history: %+v", entries)
	}
}

func TestRotatingGivesNewKeysToWhatWasAsked(t *testing.T) {
	r := newTunnel(t, "laptop, phone")
	before := r.keys(t)
	if err := r.t.Rotate(context.Background(), "laptop", false, false); err != nil {
		t.Fatal(err)
	}
	after := r.keys(t)
	if after.Devices["laptop"].PrivateKey == before.Devices["laptop"].PrivateKey ||
		after.Devices["phone"].PrivateKey != before.Devices["phone"].PrivateKey || after.Server != before.Server {
		t.Error("only the laptop gets new keys")
	}
	if after.Devices["laptop"].Address != before.Devices["laptop"].Address {
		t.Error("a device keeps its address")
	}
	if !strings.Contains(r.ui.said(), "New keys for laptop are on the server.") || r.runner.steps() != "set-keys" {
		t.Errorf("said %q, ran %q", r.ui.said(), r.runner.steps())
	}

	if err := r.t.Rotate(context.Background(), "", false, true); err != nil {
		t.Fatal(err)
	}
	if server := r.keys(t); server.Server == after.Server || server.Devices["phone"].PrivateKey != after.Devices["phone"].PrivateKey {
		t.Error("only the server gets new keys")
	}

	r.runner.do["set-keys"] = func(context.Context) error { return errors.New("the server does not answer") }
	held := r.keys(t)
	err := r.t.Rotate(context.Background(), "", true, false)
	if err == nil || !strings.Contains(err.Error(), "the new keys are in vault.yml, and damstack tunnel rotate or damstack deploy sets them on the server") {
		t.Fatalf("got %v", err)
	}
	if st := r.keys(t); st.Devices["laptop"].PrivateKey == held.Devices["laptop"].PrivateKey || st.Devices["phone"].PrivateKey == held.Devices["phone"].PrivateKey {
		t.Error("the new keys of every device are kept, though not on the server yet")
	}
}
