package ops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/eugene-panin/damstack/internal/config"
	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/project"
	"github.com/eugene-panin/damstack/internal/stack"
)

// gitStack is a repository in a directory of the test's own with a stack in
// each of dirs, "" for its root, committed and tagged with tag.
func gitStack(t *testing.T, tag string, dirs ...string) string {
	t.Helper()
	root := t.TempDir()
	repo, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range dirs {
		for path, text := range map[string]string{
			manifest.File: "apiVersion: " + manifest.APIVersion + "\nname: demo\ndescription: a stack of a test\n" +
				"check: {answers: answers.yaml}\nsteps:\n  - name: up\n    run: [bin/up]\n",
			"answers.yaml":     "{}\n",
			"bin/up":           "#!/bin/sh\n",
			"ansible/site.yml": "- hosts: all\n",
		} {
			full := filepath.Join(root, dir, path)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(text), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := wt.AddGlob("."); err != nil {
		t.Fatal(err)
	}
	hash, err := wt.Commit("stacks", &git.CommitOptions{Author: &object.Signature{Name: "t", Email: "t@example.org", When: time.Now()}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateTag(tag, hash, nil); err != nil {
		t.Fatal(err)
	}
	return "file://" + root
}

func TestAStackInADirectoryOfARepositoryIsLoadedFromThere(t *testing.T) {
	p, password := newProject(t, "{}\n")
	url := gitStack(t, "demo/v0.1.0", "platforms/demo", "apps/other")

	m, dir, ref, err := LatestRef(t.Context(), project.StackRef{Name: "demo", URL: url, Path: "platforms/demo"})
	if err != nil {
		t.Fatal(err)
	}
	cache, err := config.CacheDir()
	if err != nil {
		t.Fatal(err)
	}
	checkout := stack.Dir(cache, url, "demo/v0.1.0")
	if want := (project.StackRef{Name: "demo", URL: url, Path: "platforms/demo", Tag: "demo/v0.1.0", Commit: ref.Commit}); ref != want || ref.Commit == "" {
		t.Errorf("recorded %+v, want %+v", ref, want)
	}
	if m.Name != "demo" || dir != filepath.Join(checkout, "platforms", "demo") {
		t.Errorf("loaded %s from %s; want it from platforms/demo of %s", m.Name, dir, checkout)
	}
	if cm, cdir := CachedRef(ref); cm == nil || cdir != dir {
		t.Errorf("cached: %v in %s", cm, cdir)
	}
	if rm, rdir, err := RefStack(t.Context(), ref); err != nil || rm.Name != "demo" || rdir != dir {
		t.Errorf("again: %v in %s, %v", rm, rdir, err)
	}

	before, err := PlatformInputs(p, dir, "ansible/site.yml", password)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "apps", "other", "ansible", "site.yml"), []byte("- hosts: none\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if after, err := PlatformInputs(p, dir, "ansible/site.yml", password); err != nil || after != before {
		t.Errorf("another stack of the repository changed the inputs of this one: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ansible", "site.yml"), []byte("- hosts: none\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if after, err := PlatformInputs(p, dir, "ansible/site.yml", password); err != nil || after == before {
		t.Errorf("its own Ansible did not change the inputs: %v", err)
	}

	for _, bad := range []string{"../x", "/etc", "platforms/../.."} {
		escaped := ref
		escaped.Path = bad
		if _, _, err := RefStack(t.Context(), escaped); err == nil {
			t.Errorf("path %q was taken", bad)
		}
		if m, _ := CachedRef(escaped); m != nil {
			t.Errorf("path %q was loaded from the cache", bad)
		}
	}
	if _, _, _, err := LatestRef(t.Context(), project.StackRef{Name: "other", URL: url, Path: "apps/other"}); err == nil || !strings.Contains(err.Error(), "other/") {
		t.Errorf("a stack of the repository without a release of its own: %v", err)
	}
}

func TestAStackThatIsAWholeRepositoryIsLoadedAsBefore(t *testing.T) {
	newProject(t, "{}\n")
	url := gitStack(t, "v0.1.0", "")

	m, dir, ref, err := LatestRef(t.Context(), project.StackRef{URL: url})
	if err != nil {
		t.Fatal(err)
	}
	cache, err := config.CacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := stack.Dir(cache, url, "v0.1.0"); m.Name != "demo" || dir != want {
		t.Errorf("loaded %s from %s, want %s", m.Name, dir, want)
	}
	if ref.Path != "" || ref.Tag != "v0.1.0" || ref.Name != "demo" {
		t.Errorf("recorded %+v", ref)
	}
	if cm, cdir := CachedRef(ref); cm == nil || cdir != dir {
		t.Errorf("cached: %v in %s", cm, cdir)
	}
	if again, _, next, err := LatestRef(t.Context(), ref); err != nil || again == nil || next != ref {
		t.Errorf("its newest release again: %+v, %v", next, err)
	}
}
