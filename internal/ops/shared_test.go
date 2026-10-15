package ops

import (
	"strings"
	"testing"

	"github.com/eugene-panin/damstack/internal/project"
)

func TestTwoCommandsDoNotChangeAProjectAtOnce(t *testing.T) {
	p := &project.Project{Dir: t.TempDir(), Meta: project.Meta{Name: "demo"}}
	first, err := LockHere(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LockHere(p); err == nil || !strings.Contains(err.Error(), "another damstack") {
		t.Fatalf("a second lock must be refused, got %v", err)
	}
	first.Close()
	again, err := LockHere(p)
	if err != nil {
		t.Fatalf("once the first is done, the lock is free: %v", err)
	}
	again.Close()
}
