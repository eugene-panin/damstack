package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func fakeGitHub(t *testing.T, login string) *gitHub {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		write := func(v any) { json.NewEncoder(w).Encode(v) }
		switch r.URL.Path {
		case "/orgs/twinmind-pro/repos":
			write([]repo{{Name: "damstack-sauron", FullName: "twinmind-pro/damstack-sauron", Private: true}, {Name: "sauron", FullName: "twinmind-pro/sauron"}})
		case "/user":
			write(map[string]string{"login": login})
		case "/user/repos":
			write([]repo{{Name: "damstack-lab", FullName: "eugene-panin/damstack-lab", Private: true}})
		case "/users/someone/repos":
			write([]repo{{Name: "damstack-x", FullName: "someone/damstack-x"}})
		case "/users/many/repos":
			n := map[string]int{"1": 100, "2": 100, "3": 5}[r.URL.Query().Get("page")]
			page := make([]repo, n)
			for i := range page {
				page[i] = repo{Name: fmt.Sprintf("r%d", i), FullName: fmt.Sprintf("many/r%s-%d", r.URL.Query().Get("page"), i)}
			}
			write(page)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return &gitHub{token: "t", http: srv.Client(), api: srv.URL}
}

func TestTheRepositoriesOfAnOwnerAreFound(t *testing.T) {
	ctx := context.Background()
	g := fakeGitHub(t, "eugene-panin")
	for owner, want := range map[string]string{
		"twinmind-pro": "twinmind-pro/damstack-sauron", // an organization, its private ones too
		"eugene-panin": "eugene-panin/damstack-lab",    // the user signed in: their private ones
		"someone":      "someone/damstack-x",           // another user: what is public
	} {
		repos, err := g.repos(ctx, owner)
		if err != nil || len(repos) == 0 || repos[0].FullName != want {
			t.Errorf("%s: %v %v", owner, repos, err)
		}
	}
	if repos, err := g.repos(ctx, "many"); err != nil || len(repos) != 205 {
		t.Errorf("every page of an owner with many repositories: %d, %v", len(repos), err)
	}
	if _, err := g.repos(ctx, "nobody"); err == nil {
		t.Error("an owner github.com does not know is an error")
	}
	if Owner("twinmind-pro") != true || Owner("twinmind-pro/damstack-sauron") || Owner("https://git.example.com/x.git") {
		t.Error("an owner is a name alone")
	}
}
