package checks

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eugene-panin/damstack/internal/manifest"
)

func TestSSHPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	e := Default()
	e.SSHPort = port
	spec := []manifest.CheckSpec{{Name: "ssh-port"}}
	if r := e.Run(t.Context(), spec, "127.0.0.1", nil); !r[0].OK {
		t.Errorf("a server that answers: %+v", r)
	}
	ln.Close()
	if r := e.Run(t.Context(), spec, "127.0.0.1", nil); r[0].OK || !strings.Contains(r[0].Text, "nothing answers") {
		t.Errorf("a server that does not: %+v", r)
	}
}

func TestCloudflareToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"success":false,"errors":[{"code":1000}]}`)
			return
		}
		if r.URL.Query().Get("page") == "1" {
			fmt.Fprint(w, `{"success":true,"result":[{"name":"example.com"}],"result_info":{"total_pages":2}}`)
			return
		}
		fmt.Fprint(w, `{"success":true,"result":[{"name":"example.org"}],"result_info":{"total_pages":2}}`)
	}))
	defer srv.Close()
	e := Default()
	e.CloudflareAPI = srv.URL
	spec := []manifest.CheckSpec{{Name: "cloudflare-token", Arg: "dns_zones"}}
	for _, tc := range []struct {
		token string
		zones []any
		ok    bool
		want  string
	}{
		{"good", []any{"example.com", "example.org"}, true, "the token sees example.com, example.org"},
		{"good", []any{"example.com", "example.net"}, false, "does not see example.net: give it Zone Read and DNS Edit on it"},
		{"bad", []any{"example.com"}, false, "does not take this token"},
		{"good", nil, true, "Cloudflare takes the token"},
	} {
		r := e.Run(t.Context(), spec, tc.token, map[string]any{"dns_zones": tc.zones})
		if r[0].OK != tc.ok || !strings.Contains(r[0].Text, tc.want) {
			t.Errorf("%s %v: %+v, want %q", tc.token, tc.zones, r[0], tc.want)
		}
	}
}
