package main

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"
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
