package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/eugene-panin/damstack/internal/release"
)

// runCLI runs damstack with args in a home of its own and returns the exit
// status, stdout and stderr.
func runCLI(t *testing.T, ctx context.Context, args ...string) (int, string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home+"/config")
	t.Setenv("XDG_CACHE_HOME", home+"/cache")
	t.Setenv("DAMSTACK_HOME", home+"/projects")
	var out, errOut bytes.Buffer
	code := report(ctx, run(ctx, args, strings.NewReader(""), &out, &errOut), &errOut)
	return code, out.String(), errOut.String()
}

func TestHelpWinsOverBadFlags(t *testing.T) {
	for _, args := range [][]string{{"deploy", "--bogus", "-h"}, {"--bogus", "--help"}, {"use", "a", "b", "-h"}} {
		code, out, errOut := runCLI(t, context.Background(), args...)
		if code != 0 || !strings.Contains(out, "Usage:") || errOut != "" {
			t.Errorf("%q: exit %d, out %q, err %q", args, code, out, errOut)
		}
	}
}

func TestUsageErrorsExitTwo(t *testing.T) {
	for _, args := range [][]string{{"--bogus"}, {"deploy", "--bogus"}, {"nosuchcommand"}, {"use", "a", "b"}} {
		code, _, errOut := runCLI(t, context.Background(), args...)
		if code != exitUsage || !strings.Contains(errOut, "--help") {
			t.Errorf("%q: exit %d, err %q", args, code, errOut)
		}
	}
}

// Without the argument it needs, a command says what it does and how to call it.
func TestMissingArgumentShowsShortHelp(t *testing.T) {
	code, _, errOut := runCLI(t, context.Background(), "use")
	if code != exitUsage || !strings.Contains(errOut, "Usage:\n  damstack use <project>") || !strings.Contains(errOut, "damstack use my-cloud") {
		t.Errorf("exit %d, err:\n%s", code, errOut)
	}
}

func TestVersionFlag(t *testing.T) {
	code, out, _ := runCLI(t, context.Background(), "--version")
	if want := "damstack " + release.Version + ", toolbox " + release.Toolbox + "\n"; code != 0 || out != want {
		t.Errorf("exit %d, out %q, want %q", code, out, want)
	}
	if code, _, _ := runCLI(t, context.Background(), "-v"); code != exitUsage {
		t.Errorf("-v: exit %d", code)
	}
}

func TestInterruptedExits130(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var errOut bytes.Buffer
	if code := report(ctx, context.Canceled, &errOut); code != exitInterrupt || errOut.String() != "\n" {
		t.Errorf("exit %d, err %q", code, errOut.String())
	}
}

func TestSSHTakesOneProjectBeforeTheCommand(t *testing.T) {
	if code, _, errOut := runCLI(t, context.Background(), "ssh", "a", "b"); code != exitUsage {
		t.Errorf("ssh a b: exit %d, err %q", code, errOut)
	}
	if code, _, errOut := runCLI(t, context.Background(), "ssh", "a", "--", "uptime", "-p"); code == exitUsage {
		t.Errorf("ssh a -- uptime -p: exit %d, err %q", code, errOut)
	}
}

func TestCommandElsewhereExitsWithItsStatus(t *testing.T) {
	var errOut bytes.Buffer
	if code := report(context.Background(), fmt.Errorf("ssh: %w", exitStatus(3)), &errOut); code != 3 || errOut.Len() != 0 {
		t.Errorf("exit %d, err %q", code, errOut.String())
	}
}
