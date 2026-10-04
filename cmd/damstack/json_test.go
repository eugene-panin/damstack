package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeProject makes a project in a temporary directory with history, one
// line of JSON per run.
func writeProject(t *testing.T, history ...string) string {
	t.Helper()
	dir := t.TempDir()
	meta := "name: demo\nstack:\n  name: demo\n  url: https://example.com/damstack-demo\n  tag: v1.0.0\n  commit: abc123\ncreated: 2026-10-01T10:00:00Z\n"
	if err := os.MkdirAll(filepath.Join(dir, ".damstack"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".damstack", "project.yaml"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	if len(history) > 0 {
		if err := os.WriteFile(filepath.Join(dir, ".damstack", "history.jsonl"), []byte(strings.Join(history, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestStatusJSON(t *testing.T) {
	dir := writeProject(t,
		`{"time":"2026-10-01T10:05:00Z","command":"deploy","step":"configure","result":"failed","stack":"v1.0.0","commit":"abc123","damstack":"0.2.0","error":"boom"}`,
		`{"time":"2026-10-01T10:09:00Z","command":"deploy","step":"configure","result":"ok","stack":"v1.0.0","commit":"abc123","damstack":"0.2.0"}`,
	)
	t.Chdir(dir)
	code, out, errOut := runCLI(t, context.Background(), "status", "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	var got statusJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	if got.Project != "demo" || got.Stack.Tag != "v1.0.0" || got.Apps == nil || len(got.Steps) != 1 ||
		got.Steps[0].Step != "configure" || got.Steps[0].Result != "ok" || got.Steps[0].LastRun == nil {
		t.Errorf("got %+v", got)
	}

	if code, _, errOut := runCLI(t, context.Background(), "status", "--json", "--live"); code != exitUsage {
		t.Errorf("--json --live: exit %d, %s", code, errOut)
	}
}

func TestHistoryJSON(t *testing.T) {
	t.Chdir(writeProject(t))
	code, out, _ := runCLI(t, context.Background(), "history", "--json")
	if code != 0 || strings.TrimSpace(out) != "[]" {
		t.Errorf("empty history: exit %d, %q", code, out)
	}

	t.Chdir(writeProject(t, `{"time":"2026-10-01T10:05:00Z","command":"deploy","step":"configure","result":"failed","seconds":2.5,"stack":"v1.0.0","commit":"abc123","damstack":"0.2.0","error":"boom"}`))
	code, out, _ = runCLI(t, context.Background(), "history", "--json")
	var runs []map[string]any
	if err := json.Unmarshal([]byte(out), &runs); code != 0 || err != nil || len(runs) != 1 {
		t.Fatalf("exit %d, %v:\n%s", code, err, out)
	}
	for _, k := range []string{"time", "command", "step", "result", "seconds", "stack", "commit", "damstack", "error"} {
		if _, ok := runs[0][k]; !ok {
			t.Errorf("no %s in %v", k, runs[0])
		}
	}
}
