package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/eugene-panin/damstack/internal/backup"
	"github.com/eugene-panin/damstack/internal/config"
	"github.com/eugene-panin/damstack/internal/doctor"
	"github.com/eugene-panin/damstack/internal/project"
)

// home is what damstack shows without a command: on the first run what it
// is, the machine and where to start; later the projects, and the machine
// only when something on it broke.
func home(ctx context.Context, w io.Writer) error {
	cfg, err := loadConfig(ctx)
	if err != nil {
		return err
	}
	results := doctor.Run(ctx, doctor.Host(nil))
	first := !checkedBefore()
	if first {
		fmt.Fprintln(w, "damstack sets up your own server and runs apps on it, from one file you edit.")
		fmt.Fprintln(w, "You install Docker; damstack brings the rest.")
		fmt.Fprintln(w)
	}
	if first || broken(results) {
		failed := doctor.PrintBrief(w, results)
		fmt.Fprintln(w)
		if failed {
			fmt.Fprintln(w, "Run damstack again when done.")
			return errProblems
		}
		markChecked()
	}
	if len(cfg.Projects) == 0 {
		platforms(w, cfg)
		return nil
	}
	projects(w, cfg)
	return nil
}

func broken(results []doctor.Result) bool {
	for _, r := range results {
		if r.Status == doctor.Fail {
			return true
		}
	}
	return false
}

func platforms(w io.Writer, cfg *config.Config) {
	fmt.Fprintln(w, "Platforms")
	tw := tabwriter.NewWriter(w, 0, 4, 3, ' ', 0)
	var start string
	for _, s := range cfg.AllStacks() {
		if s.Kind == "app" {
			continue
		}
		if start == "" {
			start = s.Name
		}
		fmt.Fprintf(tw, "  %s\t%s\n", s.Name, s.Description)
		var apps []string
		for _, a := range cfg.AllStacks() {
			if a.Kind == "app" && a.Platform == s.Name {
				apps = append(apps, a.Name)
			}
		}
		if len(apps) > 0 {
			fmt.Fprintf(tw, "  \tapps for it: %s\n", strings.Join(apps, ", "))
		}
	}
	tw.Flush()
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Start with")
	fmt.Fprintf(w, "  damstack deploy %s\n", start)
	fmt.Fprintln(w, "  It asks a few questions and sets up a project in ~/damstack/<name>.")
	fmt.Fprintln(w, "  You need: a server with Ubuntu 24.04 and its root password, and a domain.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "damstack help lists every command.")
}

func projects(w io.Writer, cfg *config.Config) {
	fmt.Fprintln(w, "Projects")
	tw := tabwriter.NewWriter(w, 0, 4, 3, ' ', 0)
	home, _ := os.UserHomeDir()
	for _, entry := range cfg.Projects {
		path := entry.Path
		if rel, err := filepath.Rel(home, path); err == nil && !strings.HasPrefix(rel, "..") {
			path = filepath.Join("~", rel)
		}
		p, err := project.Open(entry.Path)
		if err != nil {
			fmt.Fprintf(tw, "  %s\t\t\tnot found\t%s\n", entry.Name, path)
			continue
		}
		platform := p.Meta.Stack.Name + " " + p.Meta.Stack.Tag
		apps := ""
		if len(p.Meta.Apps) > 0 {
			names := make([]string, len(p.Meta.Apps))
			for i, a := range p.Meta.Apps {
				names[i] = a.Name
			}
			apps = "apps: " + strings.Join(names, ", ")
		}
		mark := " "
		if cfg.Current == p.Meta.Name {
			mark = "*"
		}
		fmt.Fprintf(tw, "%s %s\t%s\t%s\t%s\t%s\n", mark, p.Meta.Name, platform, apps, lastDeploy(p), path)
	}
	tw.Flush()
	fmt.Fprintln(w)
	warned := false
	for _, entry := range cfg.Projects {
		p, err := project.Open(entry.Path)
		if err != nil {
			continue
		}
		st, _ := backup.LoadState(p.Dir)
		var warns []string
		if m, _ := cachedStack(p); m != nil && m.Backup != nil {
			if warn := staleWarning(st); warn != "" {
				warns = append(warns, p.Meta.Name+": "+warn)
			}
		}
		warns = append(warns, kitWarning(p, st))
		for _, warn := range warns {
			if warn != "" {
				fmt.Fprintf(w, "! %s\n", warn)
				warned = true
			}
		}
	}
	if warned {
		fmt.Fprintln(w)
	}
	start := ""
	for _, s := range cfg.AllStacks() {
		if s.Kind != "app" {
			start = s.Name
			break
		}
	}
	if cfg.Current != "" {
		fmt.Fprintf(w, "* the current project: damstack deploy, edit, status work on it; damstack use <project> changes it.\n")
	} else if len(cfg.Projects) > 1 {
		fmt.Fprintln(w, "Name the project, such as damstack deploy <project>, or make one current: damstack use <project>.")
	} else {
		fmt.Fprintln(w, "damstack deploy, edit and status work on it.")
	}
	fmt.Fprintf(w, "A new project: damstack deploy %s\n", start)
}

// lastDeploy is how the last deploy of a project went, in a few words.
func lastDeploy(p *project.Project) string {
	entries, err := p.History()
	if err != nil {
		return "history unreadable"
	}
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.Command != "deploy" {
			continue
		}
		if e.Result == project.Failed {
			return e.Step + " failed " + ago(e.Time)
		}
		return "deployed " + ago(e.Time)
	}
	return "not deployed yet"
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	}
	return fmt.Sprintf("%d days ago", int(d.Hours()/24))
}
