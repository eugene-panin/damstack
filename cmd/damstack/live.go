package main

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"text/tabwriter"

	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/project"
)

var changedRe = regexp.MustCompile(`changed=([0-9]+)`)

// liveCheck compares the server with what each step makes it, changing
// nothing: playbooks run in check mode, OpenTofu plans.
func liveCheck(ctx context.Context, s *streams, p *project.Project, m *manifest.Manifest, dir string) error {
	e, _, err := newEngine(ctx, s, p, m, dir)
	if err != nil {
		return err
	}
	jobs, err := buildJobs(ctx, s, p, m, e, false)
	if err != nil {
		return err
	}
	quiet := *s
	quiet.in = nil
	fmt.Fprintf(s.out, "Checking %s against the server, changing nothing.\n\n", p.Meta.Name)
	tw := tabwriter.NewWriter(s.out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "STEP\tNOW")
	differs, unknown := 0, 0
	for _, j := range jobs {
		now := liveStep(ctx, &quiet, p, j, m.Server)
		switch now.state {
		case "differs":
			differs++
		case "unknown":
			unknown++
		}
		fmt.Fprintf(tw, "%s\t%s\n", j.name, now.text)
	}
	tw.Flush()
	fmt.Fprintln(s.out)
	switch {
	case differs > 0:
		fmt.Fprintf(s.out, "%d step(s) differ from stack.yaml: damstack deploy %s brings them back, asking first.\n", differs, p.Meta.Name)
	case unknown > 0:
		fmt.Fprintln(s.out, "Nothing differs where it could be checked.")
	default:
		fmt.Fprintln(s.out, "The server is as stack.yaml says.")
	}
	return nil
}

type liveResult struct{ state, text string }

func liveStep(ctx context.Context, s *streams, p *project.Project, j job, srv *manifest.Server) liveResult {
	switch {
	case j.step.Once:
		return liveResult{"skipped", "runs once, not checked"}
	case j.step.Run != nil:
		return liveResult{"skipped", "a program of the stack, not checked"}
	case j.step.Tofu != nil && j.step.Tofu.Action == "output":
		return liveResult{"skipped", "not checked"}
	}
	if j.step.Tunnel && srv != nil {
		if err := waitTunnel(ctx, s, p, srv); err != nil {
			return liveResult{"unknown", "not checked: the tunnel is off"}
		}
	}
	log, restore, err := logTo(p, j)
	if err != nil {
		return liveResult{"unknown", err.Error()}
	}
	defer restore()
	if j.step.Ansible != nil {
		if err := j.e.Run(ctx, j.step, []string{"--check", "--diff"}); err != nil {
			return liveResult{"unknown", "the check failed: " + firstLine(err.Error()) + "; see " + log}
		}
		data, _ := os.ReadFile(log)
		changed := 0
		for _, m := range changedRe.FindAllStringSubmatch(string(data), -1) {
			n, _ := strconv.Atoi(m[1])
			changed += n
		}
		if changed > 0 {
			return liveResult{"differs", fmt.Sprintf("differs: %d task(s) would change; see %s", changed, log)}
		}
		return liveResult{"ok", "as stack.yaml says"}
	}
	counts, err := j.e.Drift(ctx, j.step)
	if err != nil {
		return liveResult{"unknown", "the check failed: " + firstLine(err.Error()) + "; see " + log}
	}
	if counts != "" {
		return liveResult{"differs", "differs: " + counts}
	}
	return liveResult{"ok", "as stack.yaml says"}
}
