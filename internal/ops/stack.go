package ops

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/eugene-panin/damstack/internal/config"
	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/project"
	"github.com/eugene-panin/damstack/internal/release"
	"github.com/eugene-panin/damstack/internal/stack"
)

// DevTag marks a project set up from a stack directory rather than a release.
const DevTag = "dev"

// ProjectStack is the stack of a project at the release it was deployed
// with, or in the directory it was set up from, or the stack in from.
func ProjectStack(ctx context.Context, p *project.Project, from string) (*manifest.Manifest, string, error) {
	if from != "" {
		dir, err := filepath.Abs(from)
		if err != nil {
			return nil, "", err
		}
		m, err := manifest.Load(dir, release.Version)
		return m, dir, err
	}
	return RefStack(ctx, p.Meta.Stack)
}

// RefStack is a stack at a release, fetched when it is not on this machine,
// or in the directory a dev release names.
func RefStack(ctx context.Context, ref project.StackRef) (*manifest.Manifest, string, error) {
	dir := strings.TrimPrefix(ref.URL, "file://")
	if ref.Tag != DevTag {
		cache, err := config.CacheDir()
		if err != nil {
			return nil, "", err
		}
		if dir, err = stack.Fetch(ctx, cache, ref.URL, stack.Release{Tag: ref.Tag, Commit: ref.Commit}); err != nil {
			return nil, "", err
		}
	}
	m, err := manifest.Load(dir, release.Version)
	return m, dir, err
}

// CachedStack is the stack of a project if it is on this machine already.
func CachedStack(p *project.Project) (*manifest.Manifest, string) {
	return CachedRef(p.Meta.Stack)
}

// CachedRef is a stack at a release if it is on this machine already.
func CachedRef(ref project.StackRef) (*manifest.Manifest, string) {
	dir := strings.TrimPrefix(ref.URL, "file://")
	if ref.Tag != DevTag {
		cache, err := config.CacheDir()
		if err != nil {
			return nil, ""
		}
		dir = stack.Dir(cache, ref.URL, ref.Tag)
	}
	m, err := manifest.Load(dir, release.Version)
	if err != nil {
		return nil, ""
	}
	return m, dir
}

// AppEnv is the app_env of a platform, rendered for its project.
func AppEnv(p *project.Project, m *manifest.Manifest, dir, password string) (map[string]string, error) {
	config, err := p.Config()
	if err != nil {
		return nil, err
	}
	secrets, err := p.Secrets(password)
	if err != nil {
		return nil, err
	}
	data := map[string]any{"config": config, "dir": map[string]string{"project": p.Dir, "stack": dir}}
	env := map[string]string{}
	for key, text := range m.AppEnv {
		if env[key], err = manifest.RenderData("app_env."+key, text, data, secrets); err != nil {
			return nil, fmt.Errorf("app_env.%s of %s: %w", key, m.Name, err)
		}
	}
	return env, nil
}
