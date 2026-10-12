package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/eugene-panin/damstack/internal/project"
	"github.com/eugene-panin/damstack/internal/release"
)

// rerunAfter is how old the last run of an unchanged step may be: past it the
// step runs again, and puts back what was changed on the server by hand.
const rerunAfter = 7 * 24 * time.Hour

// platformInputs is a fingerprint of what a platform's Ansible step is given:
// the stack's Ansible as it is on this machine, stack.yaml but its apps, the
// platform's secrets, and this damstack. Apps change none of them, so a
// deploy for an app's key does not converge the platform again.
func platformInputs(p *project.Project, stackDir, playbook, password string) (string, error) {
	h := sha256.New()
	io.WriteString(h, release.Version+"\n")
	root := filepath.Join(stackDir, strings.SplitN(filepath.ToSlash(playbook), "/", 2)[0])
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(stackDir, path)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		io.WriteString(h, rel+"\n")
		h.Write(data)
		return nil
	})
	if err != nil {
		return "", err
	}
	config, err := p.Config()
	if err != nil {
		return "", err
	}
	delete(config, "apps")
	secrets, err := p.Secrets(password)
	if err != nil {
		return "", err
	}
	for name := range appSecretNames(p) {
		delete(secrets, name)
	}
	for _, v := range []any{config, secrets} {
		// yaml.v3 writes a map's keys in order: the same map, the same bytes.
		data, err := yaml.Marshal(v)
		if err != nil {
			return "", err
		}
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// appSecretNames are the secrets of a project's apps.
func appSecretNames(p *project.Project) map[string]bool {
	names := map[string]bool{}
	for _, ref := range p.Meta.Apps {
		if am, _ := cachedRef(ref); am != nil {
			for _, sec := range am.Secrets {
				names[sec.Name] = true
			}
		}
	}
	return names
}

// unchanged says why a step need not run: its last run went well, with the
// same inputs, not long ago. Empty when it must run.
func unchanged(p *project.Project, step, inputs string, now time.Time) string {
	if inputs == "" {
		return ""
	}
	last, ok, err := p.LastRun(step)
	if err != nil || !ok || last.Result != project.OK || last.Inputs != inputs || now.Sub(last.Time) > rerunAfter {
		return ""
	}
	return "unchanged since " + last.Time.Local().Format("Jan 2 15:04") + "; --all runs it"
}

// chooseJobs keeps the jobs a deploy runs: those picked, when some were, all
// of them run; otherwise every one, those of the platform's Ansible steps with
// the fingerprint of their inputs, to be skipped while it stays the same.
func chooseJobs(s *streams, p *project.Project, jobs []job, restore bool, password string) ([]job, error) {
	if s.only != nil {
		var parts []string
		for _, j := range jobs {
			if !slices.Contains(parts, j.part()) {
				parts = append(parts, j.part())
			}
		}
		for name := range s.only {
			if !slices.Contains(parts, name) {
				return nil, fmt.Errorf("%s is no part of a deploy of %s; the parts: %s", name, p.Meta.Name, strings.Join(parts, ", "))
			}
		}
		return slices.DeleteFunc(jobs, func(j job) bool { return !s.only[j.part()] }), nil
	}
	if restore || s.all {
		return jobs, nil
	}
	for i, j := range jobs {
		if j.step.Ansible == nil || j.step.Once || j.e.App != "" {
			continue
		}
		var err error
		if jobs[i].inputs, err = platformInputs(p, j.e.Stack, j.step.Ansible.Playbook, password); err != nil {
			return nil, err
		}
	}
	return jobs, nil
}
