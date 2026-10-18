package ops

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"maps"
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

// PlatformInputs is a fingerprint of what a platform's Ansible step is given:
// the stack's Ansible as it is on this machine, stack.yaml but its apps, the
// platform's secrets, and this damstack. Apps change none of them, so a
// deploy for an app's key does not converge the platform again.
func PlatformInputs(p *project.Project, stackDir, playbook, password string) (string, error) {
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
	ports := appPublicPorts(config["apps"])
	delete(config, "apps")
	if len(ports) > 0 {
		config["apps.public_ports"] = ports
	}
	secrets, err := p.Secrets(password)
	if err != nil {
		return "", err
	}
	for name := range AppSecretNames(p) {
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

// AppSecretNames are the secrets of a project's apps.
func AppSecretNames(p *project.Project) map[string]bool {
	names := map[string]bool{}
	for _, ref := range p.Meta.Apps {
		if am, _ := CachedRef(ref); am != nil {
			for _, sec := range am.Secrets {
				names[sec.Name] = true
			}
		}
	}
	return names
}

// Unchanged says why a step need not run: its last run went well, with the
// same inputs, not long ago. Empty when it must run.
func Unchanged(p *project.Project, step, inputs string, now time.Time) string {
	if inputs == "" {
		return ""
	}
	last, ok, err := p.LastRun(step)
	if err != nil || !ok || last.Result != project.OK || last.Inputs != inputs || now.Sub(last.Time) > rerunAfter {
		return ""
	}
	return "unchanged since " + last.Time.Local().Format("Jan 2 15:04") + "; --all runs it"
}

// chooseJobs keeps the jobs a deploy runs: those picked, when some were, with
// the steps after the apps when afterApps, all of them run; otherwise every one, those of the platform's Ansible steps with
// the fingerprint of their inputs, to be skipped while it stays the same.
func chooseJobs(only map[string]bool, afterApps, all bool, p *project.Project, jobs []Job, restore bool, password string) ([]Job, error) {
	if only != nil {
		var parts []string
		for _, j := range jobs {
			if !slices.Contains(parts, j.Part()) {
				parts = append(parts, j.Part())
			}
		}
		for name := range only {
			if !slices.Contains(parts, name) {
				return nil, fmt.Errorf("%s is no part of a deploy of %s; the parts: %s", name, p.Meta.Name, strings.Join(parts, ", "))
			}
		}
		return slices.DeleteFunc(jobs, func(j Job) bool { return !only[j.Part()] && !(afterApps && j.Step.AfterApps) }), nil
	}
	if restore || all {
		return jobs, nil
	}
	for i, j := range jobs {
		if j.Step.Ansible == nil || j.Step.Once || j.Engine.App != "" {
			continue
		}
		var err error
		if jobs[i].Inputs, err = PlatformInputs(p, j.Engine.Stack, j.Step.Ansible.Playbook, password); err != nil {
			return nil, err
		}
	}
	return jobs, nil
}

// appPublicPorts are the public ports the apps of a project open, the one
// setting of theirs a platform's Ansible reads, for its firewall: sorted and
// once each, so that an app added or removed with ports runs it again.
func appPublicPorts(apps any) []string {
	blocks, _ := apps.(map[string]any)
	seen := map[string]bool{}
	for _, block := range blocks {
		b, _ := block.(map[string]any)
		list, _ := b["public_ports"].([]any)
		for _, port := range list {
			seen[fmt.Sprint(port)] = true
		}
	}
	return slices.Sorted(maps.Keys(seen))
}
