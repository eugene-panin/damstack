package library

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eugene-panin/damstack/internal/config"
)

const index = `stacks:
  - name: hashi
    url: https://github.com/eugene-panin/damstack-hashi
    description: a platform
  - name: k3s
    url: https://github.com/eugene-panin/damstack-k3s
    description: another platform
`

func TestGet(t *testing.T) {
	served := index
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		if served == "" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, served)
	}))
	defer srv.Close()
	now := time.Now()
	l := &Library{URL: srv.URL, Cache: filepath.Join(t.TempDir(), "library.yaml"), Client: srv.Client(), Now: func() time.Time { return now }}

	if got := l.Get(t.Context()); len(got) != 2 || got[1].Name != "k3s" || !got[1].Builtin {
		t.Fatalf("fetched %+v", got)
	}
	l.Get(t.Context())
	if hits != 1 {
		t.Errorf("a fresh copy was fetched again: %d fetches", hits)
	}

	served = ""
	now = now.Add(2 * Fresh)
	if got := l.Get(t.Context()); len(got) != 2 || hits != 2 {
		t.Errorf("an old copy, the library down: %+v after %d fetches", got, hits)
	}

	if err := os.Remove(l.Cache); err != nil {
		t.Fatal(err)
	}
	if got := l.Get(t.Context()); len(got) != len(config.Builtin) || got[0].Name != config.Builtin[0].Name {
		t.Errorf("no copy, the library down: %+v", got)
	}
}

func TestTheRegistryIndexGivesTheDirectoryOfEachStack(t *testing.T) {
	got, err := parse([]byte(`stacks:
  - {name: hashi, url: https://github.com/damstack-dev/damstack-registry, path: platforms/hashi, description: a platform}
  - {name: mail, url: https://github.com/damstack-dev/damstack-registry, path: apps/mail, kind: app, platform: hashi}
  - {name: evil, url: https://github.com/damstack-dev/damstack-registry, path: ../../etc, kind: app}
  - {name: k3s, url: https://github.com/eugene-panin/damstack-k3s}
`))
	if err != nil {
		t.Fatal(err)
	}
	want := []config.Stack{
		{Name: "hashi", URL: "https://github.com/damstack-dev/damstack-registry", Path: "platforms/hashi", Description: "a platform", Builtin: true},
		{Name: "mail", URL: "https://github.com/damstack-dev/damstack-registry", Path: "apps/mail", Kind: "app", Platform: "hashi", Builtin: true},
		{Name: "k3s", URL: "https://github.com/eugene-panin/damstack-k3s", Builtin: true},
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	for _, s := range config.Builtin {
		if s.URL != config.RegistryURL || (s.Path != "platforms/"+s.Name && s.Path != "apps/"+s.Name) || (s.Kind == "app") != strings.HasPrefix(s.Path, "apps/") {
			t.Errorf("built in: %+v", s)
		}
	}
}
