// Package sources is where damstack takes apps from besides its library: a
// repository, or an owner on github.com, all of whose damstack-* repositories.
// A sync reads the newest release of each and keeps what it found.
package sources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/eugene-panin/damstack/internal/atomicfile"
	"github.com/eugene-panin/damstack/internal/config"
	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/release"
	"github.com/eugene-panin/damstack/internal/stack"
)

// Entry is what the last sync of a source found.
type Entry struct {
	Synced time.Time `yaml:"synced"`
	// Private says the source needs a sign-in; Login is whose.
	Private bool           `yaml:"private,omitempty"`
	Login   string         `yaml:"login,omitempty"`
	Apps    []config.Stack `yaml:"apps,omitempty"`
	Err     string         `yaml:"err,omitempty"`
}

// Cache is what the syncs of the sources found, by source.
type Cache map[string]Entry

func path() (string, error) {
	dir, err := config.CacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "sources.yaml"), nil
}

// Load reads what the syncs found; none yet is no error.
func Load() (Cache, error) {
	p, err := path()
	if err != nil {
		return nil, err
	}
	c := Cache{}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	return c, yaml.Unmarshal(data, &c)
}

// Save keeps what the syncs found.
func (c Cache) Save() error {
	p, err := path()
	if err != nil {
		return err
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return atomicfile.Write(p, data, 0o644)
}

// Apps are the apps every source found.
func (c Cache) Apps() []config.Stack {
	var out []config.Stack
	for _, name := range slices.Sorted(maps.Keys(c)) {
		out = append(out, c[name].Apps...)
	}
	return out
}

// Owner is whether a source names an owner rather than one repository.
func Owner(source string) bool {
	return !strings.ContainsAny(source, "/:")
}

// Sync reads a source: its repositories, the newest release of each, and the
// apps among them.
func Sync(ctx context.Context, source string) Entry {
	e := Entry{Synced: time.Now().UTC().Truncate(time.Second)}
	gh := newGitHub()
	var urls []string
	if Owner(source) {
		repos, err := gh.repos(ctx, source)
		if err != nil {
			e.Err = err.Error()
			return e
		}
		for _, r := range repos {
			if strings.HasPrefix(r.Name, "damstack-") {
				urls = append(urls, "https://github.com/"+r.FullName)
				e.Private = e.Private || r.Private
			}
		}
		if len(urls) == 0 {
			e.Err = "no damstack-* repository of " + source + " that this machine may read"
			return e
		}
	} else {
		u, err := stack.NormalizeURL(source)
		if err != nil {
			e.Err = err.Error()
			return e
		}
		urls = []string{u}
		if repo, ok := strings.CutPrefix(u, "https://github.com/"); ok {
			e.Private = gh.private(ctx, repo)
		}
	}
	if e.Private {
		e.Login = gh.login(ctx)
	}
	var problems []string
	for _, u := range urls {
		app, err := read(ctx, u)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", strings.TrimPrefix(u, "https://github.com/"), err))
			continue
		}
		if app.Kind == "app" {
			e.Apps = append(e.Apps, app)
		}
	}
	if len(e.Apps) == 0 && len(problems) > 0 {
		e.Err = strings.Join(problems, "; ")
	}
	return e
}

// read is the stack at the newest release of a repository.
func read(ctx context.Context, u string) (config.Stack, error) {
	r, err := stack.Latest(ctx, u)
	if err != nil {
		return config.Stack{}, err
	}
	cache, err := config.CacheDir()
	if err != nil {
		return config.Stack{}, err
	}
	dir, err := stack.Fetch(ctx, cache, u, r)
	if err != nil {
		return config.Stack{}, err
	}
	m, err := manifest.Load(dir, release.Version)
	if err != nil {
		return config.Stack{}, err
	}
	s := config.Stack{Name: m.Name, URL: u, Description: m.Description}
	if m.IsApp() {
		s.Kind = "app"
	}
	return s, nil
}

type gitHub struct {
	token string
	http  *http.Client
	api   string
}

func newGitHub() *gitHub {
	return &gitHub{token: stack.GitHubToken(), http: &http.Client{Timeout: 10 * time.Second}, api: "https://api.github.com"}
}

type repo struct {
	Name     string `json:"name"`
	FullName string `json:"full_name"`
	Private  bool   `json:"private"`
}

func (g *gitHub) get(ctx context.Context, path string, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.api+path, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}
	resp, err := g.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, fmt.Errorf("github: %s", resp.Status)
	}
	return resp.StatusCode, json.NewDecoder(resp.Body).Decode(out)
}

// repos are an owner's repositories this machine may read: an organization's,
// or a user's, the private ones too when the user is the one signed in.
func (g *gitHub) repos(ctx context.Context, owner string) ([]repo, error) {
	q := "?per_page=100&type=all"
	out, code, err := g.pages(ctx, "/orgs/"+url.PathEscape(owner)+"/repos"+q)
	if code != http.StatusNotFound {
		return out, err
	}
	if g.login(ctx) == owner {
		out, _, err = g.pages(ctx, "/user/repos?per_page=100&affiliation=owner")
		return out, err
	}
	out, code, err = g.pages(ctx, "/users/"+url.PathEscape(owner)+"/repos"+q)
	if code == http.StatusNotFound {
		return nil, fmt.Errorf("no owner %s on github.com", owner)
	}
	return out, err
}

// pages are the repositories of a listing, page after page, up to 1000.
func (g *gitHub) pages(ctx context.Context, path string) ([]repo, int, error) {
	var all []repo
	for page := 1; page <= 10; page++ {
		var out []repo
		code, err := g.get(ctx, path+"&page="+strconv.Itoa(page), &out)
		if err != nil {
			return all, code, err
		}
		all = append(all, out...)
		if len(out) < 100 {
			break
		}
	}
	return all, http.StatusOK, nil
}

func (g *gitHub) private(ctx context.Context, fullName string) bool {
	var r repo
	if _, err := g.get(ctx, "/repos/"+fullName, &r); err != nil {
		return g.token != ""
	}
	return r.Private
}

func (g *gitHub) login(ctx context.Context) string {
	if g.token == "" {
		return ""
	}
	var u struct {
		Login string `json:"login"`
	}
	if _, err := g.get(ctx, "/user", &u); err != nil {
		return ""
	}
	return u.Login
}
