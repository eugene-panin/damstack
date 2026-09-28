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
	"path/filepath"
	"regexp"
	"strings"

	"github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
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

// Latest finds the highest release tag of the repository at url.
func Latest(ctx context.Context, url string) (Release, error) {
	remote := git.NewRemote(memory.NewStorage(), &gitconfig.RemoteConfig{Name: "origin", URLs: []string{url}})
	refs, err := remote.ListContext(ctx, &git.ListOptions{PeelingOption: git.AppendPeeled})
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
		if semver.IsValid(tag) && semver.Prerelease(tag) == "" && (best == "" || semver.Compare(tag, best) > 0) {
			best = tag
		}
	}
	if best == "" {
		return Release{}, ErrNoRelease
	}
	return Release{Tag: best, Commit: commits[best]}, nil
}

// Fetch checks the release out into the cache and returns its directory. A
// checkout already there is reused when it is at the release's commit; one at
// another commit means the tag was moved, and is an error.
func Fetch(ctx context.Context, cacheDir, url string, r Release) (string, error) {
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
		return "", err
	}
	return dir, nil
}

// Dir is where Fetch checks a release out.
func Dir(cacheDir, url, tag string) string {
	return filepath.Join(cacheDir, "stacks", slug(url), tag)
}

// slug is where a repository lives in the cache: github.com/owner/repo.
func slug(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return strings.NewReplacer("://", "/", ":", "_").Replace(raw)
	}
	return filepath.Join(u.Host, filepath.FromSlash(strings.Trim(u.Path, "/")))
}
