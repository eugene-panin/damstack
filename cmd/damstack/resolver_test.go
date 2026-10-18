package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eugene-panin/damstack/internal/project"
)

// testResolver puts the resolvers of the test in a directory of its own, and
// runs what would run as root as it is; it gives the commands run.
func testResolver(t *testing.T) *[]string {
	t.Helper()
	oldDir, oldRoot := resolverDir, asRoot
	t.Cleanup(func() { resolverDir, asRoot = oldDir, oldRoot })
	resolverDir = filepath.Join(t.TempDir(), "resolver")
	var ran []string
	asRoot = func(ctx context.Context, _ *cli, args ...string) error {
		ran = append(ran, args[0])
		return exec.CommandContext(ctx, args[0], args[1:]...).Run()
	}
	return &ran
}

func resolverProject(t *testing.T, name string) *project.Project {
	t.Helper()
	p, err := project.Create(filepath.Join(t.TempDir(), name), project.Meta{Name: name}, map[string][]byte{project.ConfigFile: []byte("{}\n")})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(p.Dir, ".damstack"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func answering(t *testing.T, answers string) (*cli, *bytes.Buffer) {
	t.Helper()
	var out bytes.Buffer
	s := newCLI(t.Context(), strings.NewReader(answers), &out, &out)
	if answers != "" {
		s.prompt.NoTerminal = false
	}
	return s, &out
}

func resolverNow(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(resolverDir, "consul"))
	if err != nil {
		return ""
	}
	return string(data)
}

func TestTheResolverIsWrittenAndTakenBack(t *testing.T) {
	ran := testResolver(t)
	shop := resolverProject(t, "shop")
	s, _ := answering(t, "")
	if err := resolve(t.Context(), s, shop, "10.77.0.1"); err != nil {
		t.Fatal(err)
	}
	want := "# damstack shop " + shop.Dir + "\nnameserver 10.77.0.1\nport 53\n"
	if got := resolverNow(t); got != want {
		t.Errorf("the resolver:\n%s\nwant\n%s", got, want)
	}
	if info, err := os.Stat(filepath.Join(resolverDir, "consul")); err != nil || info.Mode().Perm() != 0o644 {
		t.Errorf("the resolver's mode: %v, %v", info, err)
	}
	if rec, err := recordedResolver(shop); err != nil || rec == nil || rec.Address != "10.77.0.1" || rec.Path != filepath.Join(resolverDir, "consul") {
		t.Errorf("the record: %+v, %v", rec, err)
	}
	*ran = nil
	if err := resolve(t.Context(), s, shop, "10.77.0.1"); err != nil || len(*ran) > 0 {
		t.Errorf("the same resolver is written again: %v, %v", *ran, err)
	}
	if err := resolve(t.Context(), s, shop, "10.88.0.1"); err != nil || !strings.Contains(resolverNow(t), "nameserver 10.88.0.1\n") {
		t.Errorf("a new address of the project's own is not written: %v\n%s", err, resolverNow(t))
	}

	removed, err := unresolve(t.Context(), s, shop)
	if err != nil || !removed || resolverNow(t) != "" {
		t.Errorf("taken back: %v, %v\n%s", removed, err, resolverNow(t))
	}
	if rec, _ := recordedResolver(shop); rec != nil {
		t.Errorf("the record stays: %+v", rec)
	}
	if removed, err := unresolve(t.Context(), s, shop); removed || err != nil {
		t.Errorf("taken back twice: %v, %v", removed, err)
	}
}

func TestAnotherProjectsResolverIsReplacedOnlyOnAYes(t *testing.T) {
	testResolver(t)
	shop, lab := resolverProject(t, "shop"), resolverProject(t, "lab")
	s, _ := answering(t, "")
	if err := resolve(t.Context(), s, lab, "10.99.0.1"); err != nil {
		t.Fatal(err)
	}
	labs := resolverNow(t)

	if err := resolve(t.Context(), s, shop, "10.77.0.1"); err == nil || resolverNow(t) != labs {
		t.Errorf("replaced without a terminal to ask: %v\n%s", err, resolverNow(t))
	}
	no, out := answering(t, "n\n")
	if err := resolve(t.Context(), no, shop, "10.77.0.1"); err != nil || resolverNow(t) != labs {
		t.Errorf("replaced on a no: %v\n%s", err, resolverNow(t))
	}
	if !strings.Contains(out.String(), "go to the tunnel of lab, at 10.99.0.1") {
		t.Errorf("the other project is not named:\n%s", out)
	}
	if rec, _ := recordedResolver(shop); rec != nil {
		t.Errorf("recorded on a no: %+v", rec)
	}

	yes, _ := answering(t, "y\n")
	if err := resolve(t.Context(), yes, shop, "10.77.0.1"); err != nil || !strings.HasPrefix(resolverNow(t), "# damstack shop ") {
		t.Errorf("not replaced on a yes: %v\n%s", err, resolverNow(t))
	}
	if removed, err := unresolve(t.Context(), s, lab); removed || err != nil || !strings.HasPrefix(resolverNow(t), "# damstack shop ") {
		t.Errorf("lab took back shop's resolver: %v, %v", removed, err)
	}
	if rec, _ := recordedResolver(lab); rec != nil {
		t.Errorf("lab's record stays: %+v", rec)
	}
}

func TestAResolverDamstackDidNotWriteIsReplacedOnlyOnAYes(t *testing.T) {
	testResolver(t)
	shop := resolverProject(t, "shop")
	if err := os.MkdirAll(resolverDir, 0o755); err != nil {
		t.Fatal(err)
	}
	own := "nameserver 192.168.1.1\n"
	if err := os.WriteFile(filepath.Join(resolverDir, "consul"), []byte(own), 0o644); err != nil {
		t.Fatal(err)
	}
	no, out := answering(t, "n\n")
	if err := resolve(t.Context(), no, shop, "10.77.0.1"); err != nil || resolverNow(t) != own {
		t.Errorf("replaced on a no: %v\n%s", err, resolverNow(t))
	}
	if !strings.Contains(out.String(), "damstack did not write it") {
		t.Errorf("not said:\n%s", out)
	}
	yes, _ := answering(t, "y\n")
	if err := resolve(t.Context(), yes, shop, "10.77.0.1"); err != nil || !strings.Contains(resolverNow(t), "nameserver 10.77.0.1\n") {
		t.Errorf("not replaced on a yes: %v\n%s", err, resolverNow(t))
	}
}
