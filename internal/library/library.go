// Package library fetches the library of damstack: the platforms and apps it
// offers, kept in a repository of its own so that a new one needs no new
// damstack.
package library

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/eugene-panin/damstack/internal/config"
)

const URL = "https://raw.githubusercontent.com/eugene-panin/damstack-library/main/library.yaml"

// Fresh is how long a fetched library is used before it is fetched again.
const Fresh = time.Hour

type Library struct {
	URL    string
	Cache  string
	Client *http.Client
	Now    func() time.Time
}

func Default() (*Library, error) {
	cache, err := config.CacheDir()
	if err != nil {
		return nil, err
	}
	return &Library{
		URL:    URL,
		Cache:  filepath.Join(cache, "library.yaml"),
		Client: &http.Client{Timeout: 5 * time.Second},
		Now:    time.Now,
	}, nil
}

type file struct {
	Stacks []config.Stack `yaml:"stacks"`
}

// Get is the library: fetched when the copy in the cache is older than Fresh,
// the copy when it cannot be fetched, and the library built in when there is
// no copy either.
func (l *Library) Get(ctx context.Context) []config.Stack {
	if info, err := os.Stat(l.Cache); err == nil && l.Now().Sub(info.ModTime()) < Fresh {
		if stacks, err := l.read(); err == nil {
			return stacks
		}
	}
	if data, err := l.fetch(ctx); err == nil {
		if stacks, err := parse(data); err == nil {
			if os.MkdirAll(filepath.Dir(l.Cache), 0o755) == nil {
				_ = os.WriteFile(l.Cache, data, 0o644)
			}
			return stacks
		}
	}
	if stacks, err := l.read(); err == nil {
		return stacks
	}
	return config.Builtin
}

func (l *Library) read() ([]config.Stack, error) {
	data, err := os.ReadFile(l.Cache)
	if err != nil {
		return nil, err
	}
	return parse(data)
}

func (l *Library) fetch(ctx context.Context) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := l.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", l.URL, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

func parse(data []byte) ([]config.Stack, error) {
	var f file
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	if len(f.Stacks) == 0 {
		return nil, errors.New("the library is empty")
	}
	for i := range f.Stacks {
		f.Stacks[i].Builtin = true
	}
	return f.Stacks, nil
}
