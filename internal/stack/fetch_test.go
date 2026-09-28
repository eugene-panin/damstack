package stack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

type origin struct {
	t    *testing.T
	dir  string
	repo *git.Repository
}

func newOrigin(t *testing.T) *origin {
	t.Helper()
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	return &origin{t: t, dir: dir, repo: repo}
}

func (o *origin) commit(file, content string) plumbing.Hash {
	o.t.Helper()
	if err := os.WriteFile(filepath.Join(o.dir, file), []byte(content), 0o644); err != nil {
		o.t.Fatal(err)
	}
	wt, err := o.repo.Worktree()
	if err != nil {
		o.t.Fatal(err)
	}
	if _, err := wt.Add(file); err != nil {
		o.t.Fatal(err)
	}
	hash, err := wt.Commit("change "+file, &git.CommitOptions{Author: &object.Signature{Name: "t", Email: "t@example.org", When: time.Now()}})
	if err != nil {
		o.t.Fatal(err)
	}
	return hash
}

func (o *origin) tag(name string, at plumbing.Hash, annotated bool) {
	o.t.Helper()
	var opts *git.CreateTagOptions
	if annotated {
		opts = &git.CreateTagOptions{Message: name, Tagger: &object.Signature{Name: "t", Email: "t@example.org", When: time.Now()}}
	}
	if _, err := o.repo.CreateTag(name, at, opts); err != nil {
		o.t.Fatal(err)
	}
}

func (o *origin) url() string { return "file://" + o.dir }

func TestLatestTakesTheHighestReleaseTag(t *testing.T) {
	o := newOrigin(t)
	first := o.commit("damstack.yaml", "name: demo\n")
	o.tag("v0.1.0", first, false)
	second := o.commit("damstack.yaml", "name: demo\ndescription: two\n")
	o.tag("v0.10.0", second, true)
	third := o.commit("damstack.yaml", "name: demo\ndescription: three\n")
	o.tag("v0.11.0-rc.1", third, false)
	o.tag("latest", third, false)

	r, err := Latest(t.Context(), o.url())
	if err != nil {
		t.Fatal(err)
	}
	if r.Tag != "v0.10.0" || r.Commit != second.String() {
		t.Errorf("got %s at %s; want v0.10.0 at the commit an annotated tag points to, %s", r.Tag, r.Commit, second)
	}
}

func TestLatestWithoutReleases(t *testing.T) {
	o := newOrigin(t)
	o.tag("nightly", o.commit("damstack.yaml", "name: demo\n"), false)
	if _, err := Latest(t.Context(), o.url()); err != ErrNoRelease {
		t.Errorf("got %v, want ErrNoRelease", err)
	}
}

func TestFetchChecksOutTheReleaseAndReusesIt(t *testing.T) {
	o := newOrigin(t)
	o.tag("v0.1.0", o.commit("damstack.yaml", "name: first\n"), false)
	o.commit("damstack.yaml", "name: unreleased\n")
	cache := t.TempDir()

	r, err := Latest(t.Context(), o.url())
	if err != nil {
		t.Fatal(err)
	}
	dir, err := Fetch(t.Context(), cache, o.url(), r)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "damstack.yaml"))
	if err != nil || string(data) != "name: first\n" {
		t.Fatalf("checked out %q, %v; want the release, not the branch", data, err)
	}
	if !strings.HasPrefix(dir, cache) || filepath.Base(dir) != "v0.1.0" {
		t.Errorf("fetched into %s", dir)
	}

	if again, err := Fetch(t.Context(), cache, o.url(), r); err != nil || again != dir {
		t.Errorf("second fetch: %s, %v", again, err)
	}
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(dir), ".fetch-*"))
	if len(leftovers) != 0 {
		t.Errorf("temporary checkouts left: %v", leftovers)
	}
}

func TestFetchRefusesAMovedTag(t *testing.T) {
	o := newOrigin(t)
	o.tag("v0.1.0", o.commit("damstack.yaml", "name: first\n"), false)
	cache := t.TempDir()
	r, err := Latest(t.Context(), o.url())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Fetch(t.Context(), cache, o.url(), r); err != nil {
		t.Fatal(err)
	}

	if err := o.repo.DeleteTag("v0.1.0"); err != nil {
		t.Fatal(err)
	}
	o.tag("v0.1.0", o.commit("damstack.yaml", "name: swapped\n"), false)
	moved, err := Latest(t.Context(), o.url())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Fetch(t.Context(), cache, o.url(), moved); err == nil || !strings.Contains(err.Error(), "someone moved it") {
		t.Errorf("a moved tag gave %v", err)
	}
}

func TestNormalizeURL(t *testing.T) {
	tests := []struct{ in, want string }{
		{"github.com/owner/repo", "https://github.com/owner/repo"},
		{"https://github.com/owner/repo.git", "https://github.com/owner/repo"},
		{"https://gitlab.example.org/group/sub/repo/", "https://gitlab.example.org/group/sub/repo"},
		{"file:///srv/stacks/demo", "file:///srv/stacks/demo"},
		{"eugene-panin/hashi", "https://github.com/eugene-panin/damstack-hashi"},
		{"eugene-panin/damstack-hashi", "https://github.com/eugene-panin/damstack-hashi"},
	}
	for _, tc := range tests {
		if got, err := NormalizeURL(tc.in); err != nil || got != tc.want {
			t.Errorf("NormalizeURL(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"github.com", "http://github.com/owner/repo", "owner/Repo", "owner/repo/sub"} {
		if got, err := NormalizeURL(bad); err == nil {
			t.Errorf("NormalizeURL(%q) = %q, want an error", bad, got)
		}
	}
}
