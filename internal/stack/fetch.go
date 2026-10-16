// Package stack fetches stacks from their git repositories into the cache,
// at a release tag and the commit that tag points at.
package stack

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/storage/memory"
	"golang.org/x/mod/semver"
)

// Release is a version of a stack: its tag, and the commit the tag points at.
type Release struct {
	Tag    string
	Commit string
}

// ErrNoRelease is returned for a repository without a tag such as v0.1.0.
var ErrNoRelease = errors.New("the repository has no release tag such as v0.1.0")

var tagRe = regexp.MustCompile(`^(?:[a-z0-9-]+/)?(v[^/]+)$`)

// Prefix begins the release tags of the stack name at path in its
// repository: name/ for one in a directory of it, as in hashi/v0.23.0, and
// nothing for one at its root, released as v0.23.0.
func Prefix(name, path string) string {
	if path == "" {
		return ""
	}
	return name + "/"
}

// TagPrefix is the prefix of a release tag: hashi/ of hashi/v0.23.0, nothing
// of v0.23.0.
func TagPrefix(tag string) string {
	if i := strings.LastIndex(tag, "/"); i >= 0 {
		return tag[:i+1]
	}
	return ""
}

// CheckPath refuses a directory of a stack in its repository that is not
// one: empty is the root, otherwise it is a clean relative path, such as
// platforms/hashi, that stays in it.
func CheckPath(path string) error {
	if path == "" {
		return nil
	}
	if !filepath.IsLocal(path) || path == "." || path != filepath.ToSlash(filepath.Clean(path)) ||
		slices.Contains(strings.Split(path, "/"), "..") {
		return fmt.Errorf("%q is not a directory in a repository", path)
	}
	return nil
}

// Sub is the directory of the stack at path in the checkout dir.
func Sub(dir, path string) (string, error) {
	if err := CheckPath(path); err != nil {
		return "", err
	}
	return filepath.Join(dir, filepath.FromSlash(path)), nil
}

// validTag is whether tag is a release damstack takes: a semver, after one
// name/ at most.
func validTag(tag string) bool {
	m := tagRe.FindStringSubmatch(tag)
	return m != nil && semver.IsValid(m[1])
}

// RepoPrefix begins the name of the repository of a stack on GitHub, so that
// owner/name is short for github.com/owner/damstack-name.
const RepoPrefix = "damstack-"

var shortRe = regexp.MustCompile(`^[A-Za-z0-9-]+/[a-z][a-z0-9-]*$`)

// NormalizeURL turns what a person types into the address git uses:
// github.com/owner/repo becomes https://github.com/owner/repo, owner/name
// https://github.com/owner/damstack-name, a trailing .git goes. file://
// addresses are kept, for stacks on this machine.
func NormalizeURL(raw string) (string, error) {
	raw = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(raw), "/"), ".git")
	if strings.HasPrefix(raw, "file://") {
		return raw, nil
	}
	if shortRe.MatchString(raw) {
		owner, name, _ := strings.Cut(raw, "/")
		return "https://github.com/" + owner + "/" + RepoPrefix + strings.TrimPrefix(name, RepoPrefix), nil
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || !strings.Contains(u.Host, ".") || strings.Count(strings.Trim(u.Path, "/"), "/") < 1 {
		return "", fmt.Errorf("%q is not a repository address such as github.com/owner/repo", raw)
	}
	return u.String(), nil
}

// auth is the credential for a repository that is not public: a GitHub token
// from the environment (GITHUB_TOKEN or GH_TOKEN, as `gh auth token` gives),
// used over HTTPS to github.com so a private stack can be fetched. It is nil
// when there is no token or the host is not github.com, so public repositories
// are fetched as before, without credentials.
func auth(raw string) transport.AuthMethod {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" {
		return nil
	}
	token := GitHubToken()
	if token == "" {
		return nil
	}
	return &githttp.BasicAuth{Username: "x-access-token", Password: token}
}

// GitHubToken is the token damstack reads private repositories on github.com
// with: GITHUB_TOKEN, GH_TOKEN, or the one the GitHub CLI signed in with; empty
// when there is none.
func GitHubToken() string {
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		return token
	}
	if token := os.Getenv("GH_TOKEN"); token != "" {
		return token
	}
	return ghToken()
}

// ghToken is the token the GitHub CLI signed in with, when it is installed
// and signed in; asked once.
var ghToken = sync.OnceValue(func() string {
	if _, err := exec.LookPath("gh"); err != nil {
		return ""
	}
	out, err := exec.Command("gh", "auth", "token").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
})

// Latest finds the highest release tag of the repository at url among those
// that begin with prefix, as Prefix gives it; the tags of other stacks of the
// repository, and plain ones for a prefix, are not looked at.
func Latest(ctx context.Context, url, prefix string) (Release, error) {
	remote := git.NewRemote(memory.NewStorage(), &gitconfig.RemoteConfig{Name: "origin", URLs: []string{url}})
	refs, err := remote.ListContext(ctx, &git.ListOptions{Auth: auth(url), PeelingOption: git.AppendPeeled})
	if errors.Is(err, transport.ErrRepositoryNotFound) || errors.Is(err, transport.ErrAuthenticationRequired) {
		return Release{}, fmt.Errorf("there is no repository at %s that this machine may read; for a private one on github.com, set GITHUB_TOKEN (gh auth token gives one)", url)
	}
	if err != nil {
		return Release{}, fmt.Errorf("list the tags of %s: %w", url, err)
	}
	commits := map[string]string{}
	for _, ref := range refs {
		name := ref.Name().String()
		if !strings.HasPrefix(name, "refs/tags/") {
			continue
		}
		tag := strings.TrimPrefix(name, "refs/tags/")
		if peeled, ok := strings.CutSuffix(tag, "^{}"); ok {
			commits[peeled] = ref.Hash().String()
			continue
		}
		if _, seen := commits[tag]; !seen {
			commits[tag] = ref.Hash().String()
		}
	}
	best := ""
	for tag := range commits {
		v, ok := strings.CutPrefix(tag, prefix)
		if !ok || strings.Contains(v, "/") {
			continue
		}
		if semver.IsValid(v) && semver.Prerelease(v) == "" && (best == "" || semver.Compare(v, best) > 0) {
			best = v
		}
	}
	if best == "" {
		if prefix != "" {
			return Release{}, fmt.Errorf("%w, none that begins with %s", ErrNoRelease, prefix)
		}
		return Release{}, ErrNoRelease
	}
	return Release{Tag: prefix + best, Commit: commits[prefix+best]}, nil
}

// Fetch checks the release out into the cache and returns its directory. A
// checkout already there is reused when it is at the release's commit; one at
// another commit means the tag was moved, and is an error.
func Fetch(ctx context.Context, cacheDir, url string, r Release) (string, error) {
	if rel := filepath.Join(slug(url), filepath.FromSlash(r.Tag)); !validTag(r.Tag) || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("%s %s is not a release damstack takes", url, r.Tag)
	}
	dir := Dir(cacheDir, url, r.Tag)
	if repo, err := git.PlainOpen(dir); err == nil {
		head, err := repo.Head()
		if err != nil {
			return "", fmt.Errorf("read %s: %w", dir, err)
		}
		if head.Hash().String() != r.Commit {
			return "", fmt.Errorf("the tag %s of %s now points at %s, not %s as when it was fetched; "+
				"someone moved it, check the repository before trusting it", r.Tag, url, r.Commit, head.Hash())
		}
		return dir, nil
	} else if !errors.Is(err, git.ErrRepositoryNotExists) && !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("open %s: %w", dir, err)
	}

	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dir), ".fetch-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	repo, err := git.PlainCloneContext(ctx, tmp, false, &git.CloneOptions{
		URL:           url,
		Auth:          auth(url),
		ReferenceName: plumbing.NewTagReferenceName(r.Tag),
		SingleBranch:  true,
		Depth:         1,
		Tags:          git.NoTags,
	})
	if err != nil {
		return "", fmt.Errorf("fetch %s %s: %w", url, r.Tag, err)
	}
	head, err := repo.Head()
	if err != nil {
		return "", err
	}
	if head.Hash().String() != r.Commit {
		return "", fmt.Errorf("the tag %s of %s points at %s while fetching, not %s; it was moved meanwhile", r.Tag, url, head.Hash(), r.Commit)
	}
	if err := os.Rename(tmp, dir); err != nil {
		if other, oerr := git.PlainOpen(dir); oerr == nil {
			if h, herr := other.Head(); herr == nil && h.Hash().String() == r.Commit {
				return dir, nil
			}
		}
		return "", err
	}
	return dir, nil
}

// Dir is where Fetch checks a release out: one checkout of the repository
// for each tag, so stacks of one repository at their own releases each have
// theirs.
func Dir(cacheDir, url, tag string) string {
	return filepath.Join(cacheDir, "stacks", slug(url), filepath.FromSlash(tag))
}

// slug is where a repository lives in the cache: github.com/owner/repo.
func slug(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return strings.NewReplacer("://", "/", ":", "_").Replace(raw)
	}
	return filepath.Join(u.Host, filepath.FromSlash(strings.Trim(u.Path, "/")))
}
