package ops

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/eugene-panin/damstack/internal/event"
	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/project"
)

// URLRe finds the addresses in a line.
var URLRe = regexp.MustCompile(`https?://[^\s,;)]+`)

// DoneLines are the done lines of a stack, rendered; app names the app they
// are of, empty for the platform.
func DoneLines(p *project.Project, m *manifest.Manifest, app string) []string {
	config, err := p.Config()
	if err != nil {
		return nil
	}
	data := map[string]any{"config": config}
	if app != "" {
		apps, _ := config["apps"].(map[string]any)
		data["app"] = apps[app]
	}
	var lines []string
	for i, text := range m.Done {
		line, err := manifest.RenderData(fmt.Sprintf("done[%d]", i), text, data, nil)
		if err != nil || strings.TrimSpace(line) == "" {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

// ConsulNames ends the names of the services of a platform in Consul's DNS,
// such as nomad.service.consul, reached over its private network.
const ConsulNames = ".service.consul"

// AdminHost is whether host is an admin page of a project whose stack.yaml is
// config: a name under its infra_domain, or under .service.consul, both
// reached over the private network. token is the token of m the first label
// of host names, such as nomad, empty when it names none.
func AdminHost(config map[string]any, m *manifest.Manifest, host string) (token string, ok bool) {
	infra, _ := config["infra_domain"].(string)
	if !strings.HasSuffix(host, ConsulNames) && (infra == "" || !strings.HasSuffix(host, "."+infra)) {
		return "", false
	}
	token, _, _ = strings.Cut(host, ".")
	if _, known := m.Tokens[token]; !known {
		token = ""
	}
	return token, true
}

// RunDone says what a deploy of p set up: the addresses in the done lines of
// its platform and of its apps, the admin pages first, and what to run next.
func RunDone(p *project.Project, m *manifest.Manifest) event.RunDone {
	config, _ := p.Config()
	var admin, apps []event.Link
	seen := map[string]bool{}
	add := func(lines []string) {
		for _, l := range lines {
			for _, raw := range URLRe.FindAllString(l, -1) {
				link := strings.TrimRight(raw, ".")
				u, err := url.Parse(link)
				if err != nil || seen[link] {
					continue
				}
				seen[link] = true
				if token, ok := AdminHost(config, m, u.Hostname()); ok {
					admin = append(admin, event.Link{URL: link, Admin: true, Token: token})
				} else {
					apps = append(apps, event.Link{URL: link})
				}
			}
		}
	}
	add(DoneLines(p, m, ""))
	for _, ref := range p.Meta.Apps {
		if am, _ := CachedRef(ref); am != nil {
			add(DoneLines(p, am, ref.Name))
		}
	}
	run := event.RunDone{Links: append(admin, apps...)}
	if network, ok := config["network"].(map[string]any); ok {
		if clients, ok := network["clients"].([]any); ok && len(clients) > 0 {
			run.Next = append(run.Next, event.NextStep{Command: fmt.Sprintf("damstack tunnel show %v", clients[0]),
				Why: "the WireGuard config, as a QR code too"})
		}
	}
	if m.Backup != nil {
		run.Next = append(run.Next, event.NextStep{Command: "damstack backup status", Why: "what is backed up, and where"})
	}
	return run
}

// RunStarted says which steps a run is going to take.
func RunStarted(p *project.Project, command string, jobs []Job) event.RunStarted {
	run := event.RunStarted{Command: command, Project: p.Meta.Name,
		Stack: strings.TrimSpace(p.Meta.Stack.Name + " " + p.Meta.Stack.Tag)}
	if config, err := p.Config(); err == nil {
		if server, ok := config["server"].(map[string]any); ok {
			run.Server, _ = server["address"].(string)
		}
	}
	for i, j := range jobs {
		run.Steps = append(run.Steps, event.PlannedStep{
			Step: event.Step{N: i + 1, Total: len(jobs), Name: j.Name, Title: j.Title()}, Takes: j.Step.Takes})
	}
	return run
}
