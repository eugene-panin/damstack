package library

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
