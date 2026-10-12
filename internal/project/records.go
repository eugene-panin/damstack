package project

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"gopkg.in/yaml.v3"
)

// RecordsFile says, of each secret of vault.yml, when its value was set and
// whether it was seen in plain text: what the secrets screen sorts them by.
// It holds no value.
const RecordsFile = ".damstack/secrets.yaml"

// DefaultOldAfter is how old a secret is when it is old, unless the project
// says otherwise.
const DefaultOldAfter = 90

// SecretRecord is what damstack knows of a secret besides its value.
type SecretRecord struct {
	// Set is when the value was last set; zero when it came before damstack
	// kept records.
	Set time.Time `yaml:"set,omitempty"`
	// Seen is when the value was seen in plain text, and How how: printed by
	// a tool, pasted in a chat. A new value clears both.
	Seen time.Time `yaml:"seen,omitempty"`
	How  string    `yaml:"how,omitempty"`
}

// Records are the records of a project's secrets, and when they are old.
type Records struct {
	// OldAfter is the days after which a secret is old; 0 is DefaultOldAfter.
	OldAfter int                     `yaml:"old_after,omitempty"`
	Secrets  map[string]SecretRecord `yaml:"secrets,omitempty"`
}

// OldAfterDays is the days after which a secret is old.
func (r Records) OldAfterDays() int {
	if r.OldAfter > 0 {
		return r.OldAfter
	}
	return DefaultOldAfter
}

// Records reads the records of the project's secrets; none yet is no error.
func (p *Project) Records() (Records, error) {
	r := Records{Secrets: map[string]SecretRecord{}}
	path := filepath.Join(p.Dir, RecordsFile)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	if err := yaml.Unmarshal(data, &r); err != nil {
		return r, fmt.Errorf("%s: %w", path, err)
	}
	if r.Secrets == nil {
		r.Secrets = map[string]SecretRecord{}
	}
	return r, nil
}

// SaveRecords writes the records of the project's secrets.
func (p *Project) SaveRecords(r Records) error {
	data, err := yaml.Marshal(r)
	if err != nil {
		return err
	}
	path := filepath.Join(p.Dir, RecordsFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeFile(path, data, 0o644)
}

// MarkSeen records that a secret was seen in plain text, and how.
func (p *Project) MarkSeen(name, how string, at time.Time) error {
	r, err := p.Records()
	if err != nil {
		return err
	}
	rec := r.Secrets[name]
	rec.Seen, rec.How = at.UTC().Truncate(time.Second), how
	r.Secrets[name] = rec
	return p.SaveRecords(r)
}

// MarkSet records secrets kept outside vault.yml, such as an app's sign-ins
// in Vault, as set at, and no longer seen.
func (p *Project) MarkSet(names []string, at time.Time) error {
	r, err := p.Records()
	if err != nil {
		return err
	}
	for _, name := range names {
		r.Secrets[name] = SecretRecord{Set: at.UTC().Truncate(time.Second)}
	}
	return p.SaveRecords(r)
}

// stamp records the secrets whose value differs between before and after as
// set at, and no longer seen.
func (p *Project) stamp(before, after map[string]any, at time.Time) error {
	r, err := p.Records()
	if err != nil {
		return err
	}
	changed := false
	for name, v := range after {
		if old, ok := before[name]; ok && reflect.DeepEqual(old, v) {
			continue
		}
		r.Secrets[name] = SecretRecord{Set: at.UTC().Truncate(time.Second)}
		changed = true
	}
	for name := range before {
		if _, ok := after[name]; !ok {
			delete(r.Secrets, name)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return p.SaveRecords(r)
}
