package settings

import (
	"os"
	"strings"
	"testing"
)

const stack = `# The one file to edit.

# Name of the stack, also the Consul and Nomad datacenter.
name: twinmind

server:
  # Public address of the server, as the provider gave it.
  address: 169.58.210.131
  # Name of the server in Consul. Chosen once: renaming breaks the raft.
  node_name: server
  # Ports open to the internet.
  public_ports: [80, 443]

# Admin pages, through the private network only.
infra_domain: "twinmind.damstack.dev" # where the admin pages are

network:
  clients:
    - laptop
    - phone

vault:
  # true keeps the unseal keys on the server.
  auto_unseal: true

backup:
  schedule: "03:00"
  keep:
    daily: 7

# The apps of the project.
apps:
  sauron:
    console_hostname: sauron.twinmind.pro
    replicas:
      ingest: 1
`

func parse(t *testing.T, src string) *Doc {
	t.Helper()
	d, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func find(t *testing.T, d *Doc, path string) Field {
	t.Helper()
	for _, s := range d.Sections() {
		for _, f := range s.Fields {
			if strings.Join(f.Path, ".") == path {
				return f
			}
		}
	}
	t.Fatalf("no field %s", path)
	return Field{}
}

func TestTheFileIsSectionsOfFields(t *testing.T) {
	d := parse(t, stack)
	var titles []string
	for _, s := range d.Sections() {
		titles = append(titles, s.Title)
	}
	if got := strings.Join(titles, ","); got != "General,server,network,vault,backup,sauron" {
		t.Errorf("sections: %s", got)
	}
	for path, want := range map[string]Kind{
		"name": Fixed, "infra_domain": Text, "server.address": Text, "server.node_name": Fixed,
		"server.public_ports": List, "network.clients": List, "vault.auto_unseal": Toggle,
		"backup.keep": Heading, "backup.keep.daily": Number, "apps.sauron.replicas.ingest": Number,
	} {
		if f := find(t, d, path); f.Kind != want {
			t.Errorf("%s is kind %d, want %d", path, f.Kind, want)
		}
	}
	if f := find(t, d, "server.address"); f.Help != "Public address of the server, as the provider gave it." || f.Label != "address" {
		t.Errorf("the comment above is the help: %+v", f)
	}
	if f := find(t, d, "server.node_name"); !f.Once {
		t.Error("a value chosen once is marked so")
	}
}

func TestAChangeRewritesOnlyItsValue(t *testing.T) {
	d := parse(t, stack)
	for _, c := range []struct {
		path, value, want string
	}{
		{"server.address", "5.6.7.8", "  address: 5.6.7.8\n"},
		{"infra_domain", "admin.example.com", "infra_domain: \"admin.example.com\" # where the admin pages are\n"},
		{"vault.auto_unseal", "false", "  auto_unseal: false\n"},
		{"backup.schedule", "04:30", "  schedule: \"04:30\"\n"},
		{"backup.keep.daily", "14", "    daily: 14\n"},
		{"apps.sauron.console_hostname", "yes", "    console_hostname: \"yes\"\n"},
	} {
		before := string(d.Bytes())
		if err := d.Set(strings.Split(c.path, "."), c.value); err != nil {
			t.Fatalf("%s: %v", c.path, err)
		}
		after := string(d.Bytes())
		if !strings.Contains(after, c.want) {
			t.Errorf("%s: no %q in\n%s", c.path, c.want, after)
		}
		if strings.Count(after, "\n") != strings.Count(before, "\n") {
			t.Errorf("%s: a change of a value must keep the lines", c.path)
		}
	}
	if !strings.Contains(string(d.Bytes()), "# Name of the stack, also the Consul and Nomad datacenter.\n") {
		t.Error("comments stay")
	}
	if err := d.Set([]string{"backup", "keep", "daily"}, "a week"); err == nil {
		t.Error("a number takes a number only")
	}
}

// DAMSTACK_SETTINGS_FILE names a real stack.yaml to read: every field set to
// the value it has must leave the file as it was.
func TestARealFileStaysAsItIsWhenItsValuesAreSetAgain(t *testing.T) {
	path := os.Getenv("DAMSTACK_SETTINGS_FILE")
	if path == "" {
		t.Skip("DAMSTACK_SETTINGS_FILE is not set")
	}
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	d := parse(t, string(src))
	n := 0
	for _, s := range d.Sections() {
		for _, f := range s.Fields {
			switch f.Kind {
			case Text, Number, Toggle:
				err = d.Set(f.Path, f.Value)
			case List:
				err = d.SetList(f.Path, f.Items)
			default:
				continue
			}
			if err != nil {
				t.Errorf("%s: %v", strings.Join(f.Path, "."), err)
			}
			n++
		}
	}
	if string(d.Bytes()) != string(src) {
		t.Errorf("the file changed though no value did")
	}
	t.Logf("%d sections, %d fields set again", len(d.Sections()), n)
}

func TestAListGetsAndLosesItems(t *testing.T) {
	d := parse(t, stack)
	if err := d.SetList([]string{"server", "public_ports"}, []string{"80", "443", "25"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(d.Bytes()), "  public_ports: [80, 443, 25]\n") {
		t.Errorf("a list on one line stays on one line:\n%s", d.Bytes())
	}
	if err := d.SetList([]string{"server", "public_ports"}, []string{"http"}); err == nil {
		t.Error("a list of numbers takes numbers only")
	}
	if err := d.SetList([]string{"network", "clients"}, []string{"laptop", "phone", "tablet"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(d.Bytes()), "    - phone\n    - tablet\n\nvault:") {
		t.Errorf("an item per line, indented as the first:\n%s", d.Bytes())
	}
	if err := d.SetList([]string{"network", "clients"}, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(d.Bytes()), "  clients: []\n\nvault:") {
		t.Errorf("no items left is []:\n%s", d.Bytes())
	}
	if f := find(t, d, "network.clients"); f.Kind != List || len(f.Items) != 0 {
		t.Errorf("an empty list is still a list: %+v", f)
	}
}
