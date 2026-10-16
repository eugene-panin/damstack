package stack

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
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
	if err := os.MkdirAll(filepath.Dir(filepath.Join(o.dir, file)), 0o755); err != nil {
		o.t.Fatal(err)
	}
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

	r, err := Latest(t.Context(), o.url(), "")
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
	if _, err := Latest(t.Context(), o.url(), ""); err != ErrNoRelease {
		t.Errorf("got %v, want ErrNoRelease", err)
	}
}

func TestFetchChecksOutTheReleaseAndReusesIt(t *testing.T) {
	o := newOrigin(t)
	o.tag("v0.1.0", o.commit("damstack.yaml", "name: first\n"), false)
	o.commit("damstack.yaml", "name: unreleased\n")
	cache := t.TempDir()

	r, err := Latest(t.Context(), o.url(), "")
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
	r, err := Latest(t.Context(), o.url(), "")
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
	moved, err := Latest(t.Context(), o.url(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Fetch(t.Context(), cache, o.url(), moved); err == nil || !strings.Contains(err.Error(), "someone moved it") {
		t.Errorf("a moved tag gave %v", err)
	}
}

func TestLatestWithAPrefixKeepsToItsStack(t *testing.T) {
	o := newOrigin(t)
	first := o.commit("platforms/hashi/damstack.yaml", "name: hashi\n")
	o.tag("hashi/v0.1.0", first, false)
	o.tag("v9.0.0", first, false)
	second := o.commit("platforms/hashi/damstack.yaml", "name: hashi\ndescription: two\n")
	o.tag("hashi/v0.10.0", second, true)
	third := o.commit("apps/mail/damstack.yaml", "name: mail\n")
	o.tag("mail/v1.0.0", third, false)
	o.tag("hashi/v0.11.0-rc.1", third, false)
	o.tag("hashi/x/v2.0.0", third, false)
	o.tag("hashicorp/v3.0.0", third, false)

	r, err := Latest(t.Context(), o.url(), Prefix("hashi", "platforms/hashi"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Tag != "hashi/v0.10.0" || r.Commit != second.String() {
		t.Errorf("got %s at %s; want hashi/v0.10.0 at %s", r.Tag, r.Commit, second)
	}
	if r, err := Latest(t.Context(), o.url(), "mail/"); err != nil || r.Tag != "mail/v1.0.0" {
		t.Errorf("mail: %+v, %v", r, err)
	}
	if r, err := Latest(t.Context(), o.url(), ""); err != nil || r.Tag != "v9.0.0" {
		t.Errorf("without a prefix: %+v, %v; want the plain tag only", r, err)
	}
	if _, err := Latest(t.Context(), o.url(), "odoo/"); !errors.Is(err, ErrNoRelease) {
		t.Errorf("a stack without releases: %v", err)
	}
}

func TestFetchTakesOnlyReleaseTags(t *testing.T) {
	cache := t.TempDir()
	for _, tag := range []string{"", "../x", "a/b/v1.0.0", "hashi/../v1.0.0", "../v1.0.0", "Hashi/v1.0.0", "hashi/", "hashi/latest", "/v1.0.0", "v1.0.0/.."} {
		if _, err := Fetch(t.Context(), cache, "file:///nowhere", Release{Tag: tag}); err == nil || !strings.Contains(err.Error(), "is not a release damstack takes") {
			t.Errorf("tag %q: %v", tag, err)
		}
	}
	for _, tag := range []string{"v1.2.3", "hashi/v1.2.3", "damstack-mail/v0.1.0-rc.1"} {
		if !validTag(tag) {
			t.Errorf("tag %q refused", tag)
		}
		if dir := Dir(cache, "https://github.com/o/r", tag); !strings.HasPrefix(dir, filepath.Join(cache, "stacks", "github.com", "o", "r")+string(filepath.Separator)) {
			t.Errorf("tag %q is kept at %s", tag, dir)
		}
	}
}

func TestStacksOfOneRepositoryAtTheirOwnReleases(t *testing.T) {
	o := newOrigin(t)
	o.commit("apps/mail/damstack.yaml", "name: mail\n")
	hashi := o.commit("platforms/hashi/damstack.yaml", "name: hashi\n")
	o.tag("hashi/v0.1.0", hashi, false)
	mail := o.commit("apps/mail/damstack.yaml", "name: mail\ndescription: newer\n")
	o.tag("mail/v0.2.0", mail, false)
	cache := t.TempDir()

	dirs := map[string]string{}
	for name, path := range map[string]string{"hashi": "platforms/hashi", "mail": "apps/mail"} {
		r, err := Latest(t.Context(), o.url(), Prefix(name, path))
		if err != nil {
			t.Fatal(err)
		}
		dir, err := Fetch(t.Context(), cache, o.url(), r)
		if err != nil {
			t.Fatal(err)
		}
		if dir != Dir(cache, o.url(), r.Tag) {
			t.Errorf("%s fetched into %s", name, dir)
		}
		if dirs[name], err = Sub(dir, path); err != nil {
			t.Fatal(err)
		}
	}
	if dirs["hashi"] == dirs["mail"] || filepath.Dir(filepath.Dir(dirs["hashi"])) == filepath.Dir(filepath.Dir(dirs["mail"])) {
		t.Errorf("the two share a checkout: %v", dirs)
	}
	if data, err := os.ReadFile(filepath.Join(dirs["mail"], "damstack.yaml")); err != nil || string(data) != "name: mail\ndescription: newer\n" {
		t.Errorf("mail at its release: %q, %v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(dirs["hashi"], "..", "..", "apps", "mail", "damstack.yaml")); err != nil || string(data) != "name: mail\n" {
		t.Errorf("the checkout of hashi is at its own release: %q, %v", data, err)
	}
}

func TestCheckPath(t *testing.T) {
	for _, path := range []string{"", "platforms/hashi", "apps/mail", "x"} {
		if err := CheckPath(path); err != nil {
			t.Errorf("%q: %v", path, err)
		}
	}
	for _, path := range []string{"/etc", "..", "../x", "apps/../../x", "apps/../mail", ".", "./apps", "apps/mail/", "apps//mail"} {
		if err := CheckPath(path); err == nil {
			t.Errorf("%q was taken", path)
		}
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

func TestAuthUsesAGitHubTokenForGitHubOnly(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	saved := ghToken
	defer func() { ghToken = saved }()
	ghToken = func() string { return "" }
	if auth("https://github.com/acme/damstack-x") != nil {
		t.Fatal("no token in the environment, nor gh signed in, must mean no credentials")
	}

	ghToken = func() string { return "gho_cli" }
	if a, ok := auth("https://github.com/acme/damstack-x").(*githttp.BasicAuth); !ok || a.Password != "gho_cli" {
		t.Fatalf("the token gh signed in with must be used when the environment has none, got %#v", a)
	}

	t.Setenv("GH_TOKEN", "gho_fromgh")
	a, ok := auth("https://github.com/acme/damstack-x").(*githttp.BasicAuth)
	if !ok || a.Password != "gho_fromgh" {
		t.Fatalf("GH_TOKEN must be used for github.com, got %#v", a)
	}

	t.Setenv("GITHUB_TOKEN", "ghp_first")
	if a := auth("https://github.com/acme/damstack-x").(*githttp.BasicAuth); a.Password != "ghp_first" {
		t.Fatalf("GITHUB_TOKEN must win over GH_TOKEN, got %q", a.Password)
	}

	for _, u := range []string{"https://gitlab.com/acme/damstack-x", "http://github.com/acme/damstack-x", "file:///tmp/repo"} {
		if auth(u) != nil {
			t.Fatalf("%s must get no GitHub token", u)
		}
	}
}
