package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteReplacesTheFileWithItsMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.yml")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	info, _ := os.Stat(path)
	if string(data) != "new" || info.Mode().Perm() != 0o600 {
		t.Fatalf("got %q, %v", data, info.Mode().Perm())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("no temporary file must stay: %v", entries)
	}
}
