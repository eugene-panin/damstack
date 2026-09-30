// Package checks checks answers as they are given: that a server answers,
// that a token sees what it must.
package checks

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/eugene-panin/damstack/internal/manifest"
)

type Result struct {
	OK   bool
	Text string
}

type Env struct {
	Dial          func(ctx context.Context, network, address string) (net.Conn, error)
	HTTP          *http.Client
	CloudflareAPI string
	// SSHPort is the port ssh-port dials.
	SSHPort string
}

func Default() *Env {
	return &Env{
		Dial:          (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		HTTP:          &http.Client{Timeout: 15 * time.Second},
		CloudflareAPI: "https://api.cloudflare.com/client/v4",
		SSHPort:       "22",
	}
}

// Run runs the checks of an answer, given the answers before it.
func (e *Env) Run(ctx context.Context, specs []manifest.CheckSpec, value any, answers map[string]any) []Result {
	var results []Result
	for _, spec := range specs {
		switch spec.Name {
		case "ssh-port":
			results = append(results, e.sshPort(ctx, fmt.Sprint(value)))
		case "cloudflare-token":
			results = append(results, e.cloudflareToken(ctx, fmt.Sprint(value), texts(answers[spec.Arg])))
		}
	}
	return results
}

func (e *Env) sshPort(ctx context.Context, address string) Result {
	conn, err := e.Dial(ctx, "tcp", net.JoinHostPort(address, e.SSHPort))
	if err != nil {
		return Result{false, fmt.Sprintf("nothing answers on port %s at %s: check the address, and that the server is on", e.SSHPort, address)}
	}
	conn.Close()
	return Result{true, fmt.Sprintf("it answers on port %s", e.SSHPort)}
}

func (e *Env) cloudflareToken(ctx context.Context, token string, zones []string) Result {
	var seen []string
	for page := 1; ; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/zones?per_page=50&page=%d", e.CloudflareAPI, page), nil)
		if err != nil {
			return Result{false, err.Error()}
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := e.HTTP.Do(req)
		if err != nil {
			return Result{false, "Cloudflare does not answer: " + err.Error()}
		}
		var body struct {
			Success bool `json:"success"`
			Result  []struct {
				Name string `json:"name"`
			} `json:"result"`
			ResultInfo struct {
				TotalPages int `json:"total_pages"`
			} `json:"result_info"`
		}
		err = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if err != nil || !body.Success {
			return Result{false, "Cloudflare does not take this token: copy it again, or make a new one"}
		}
		for _, z := range body.Result {
			seen = append(seen, z.Name)
		}
		if page >= body.ResultInfo.TotalPages {
			break
		}
	}
	var missing []string
	for _, z := range zones {
		if !slices.Contains(seen, z) {
			missing = append(missing, z)
		}
	}
	switch {
	case len(missing) > 0:
		return Result{false, fmt.Sprintf("the token does not see %s: give it Zone Read and DNS Edit on %s",
			strings.Join(missing, ", "), plural(missing))}
	case len(zones) == 0:
		return Result{true, "Cloudflare takes the token"}
	}
	return Result{true, "the token sees " + strings.Join(zones, ", ")}
}

func plural(items []string) string {
	if len(items) == 1 {
		return "it"
	}
	return "them"
}

func texts(v any) []string {
	var out []string
	switch items := v.(type) {
	case []any:
		for _, item := range items {
			out = append(out, fmt.Sprint(item))
		}
	case string:
		if items != "" {
			out = append(out, items)
		}
	}
	return out
}
