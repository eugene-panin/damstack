package backup

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/eugene-panin/damstack/internal/manifest"
)

// Plan is the backup section of a stack, rendered over a project.
type Plan struct {
	// From is the repository on the server, a path there; To is the one on
	// this machine, an absolute path.
	From     string
	To       string
	Password string
	// Keep are the flags of restic forget, empty to keep every snapshot.
	Keep  []string
	Every time.Duration
}

// Resolve renders b over the stack.yaml and secrets of the project in dir.
func Resolve(b *manifest.Backup, config, secrets map[string]any, dir string) (*Plan, error) {
	render := func(field, text string) (string, error) {
		out, err := manifest.Render("backup."+field, text, config, secrets)
		if err != nil {
			return "", fmt.Errorf("backup.%s of the stack: %w", field, err)
		}
		return strings.TrimSpace(out), nil
	}
	p := &Plan{}
	var err error
	if p.From, err = render("from", b.From); err != nil {
		return nil, err
	}
	to, err := render("to", b.To)
	if err != nil {
		return nil, err
	}
	if p.To, err = expand(to, dir); err != nil {
		return nil, err
	}
	if p.Password, err = render("password", b.Password); err != nil {
		return nil, err
	}
	if p.Password == "" {
		return nil, errors.New("the project has no password for its backups")
	}
	for _, name := range manifest.KeepNames {
		text, ok := b.Keep[name]
		if !ok {
			continue
		}
		n, err := render("keep."+name, text)
		if err != nil {
			return nil, err
		}
		if v, err := strconv.Atoi(n); err != nil || v < 0 {
			return nil, fmt.Errorf("backup.keep.%s of the stack is %q, not a count", name, n)
		} else if v > 0 {
			p.Keep = append(p.Keep, "--keep-"+name, n)
		}
	}
	every, err := render("every", b.Every)
	if err != nil {
		return nil, err
	}
	if p.Every, err = ParseEvery(every); err != nil {
		return nil, err
	}
	return p, nil
}

func expand(path, dir string) (string, error) {
	switch {
	case path == "":
		return "", errors.New("backup.to of the stack is empty")
	case path == "~" || strings.HasPrefix(path, "~/"):
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, strings.TrimPrefix(path, "~")), nil
	case filepath.IsAbs(path):
		return filepath.Clean(path), nil
	}
	return filepath.Join(dir, path), nil
}

var everyRe = regexp.MustCompile(`^([1-9][0-9]*)([mh])$`)

// ParseEvery reads how often a pull runs: whole minutes or hours, such as
// 30m or 1h; off, or nothing, is never.
func ParseEvery(s string) (time.Duration, error) {
	switch s {
	case "", "off", "false", "0":
		return 0, nil
	}
	m := everyRe.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("backup.every is %q; want whole minutes or hours, such as 30m or 1h, or off", s)
	}
	n, _ := strconv.Atoi(m[1])
	if m[2] == "h" {
		return time.Duration(n) * time.Hour, nil
	}
	return time.Duration(n) * time.Minute, nil
}

// FormatEvery says d as ParseEvery reads it: 1h, 30m.
func FormatEvery(d time.Duration) string {
	if d%time.Hour == 0 {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}

// Source is the repository a pull copies from: a path, or sftp:user@host:path
// with the options restic reaches it with.
type Source struct {
	Repo    string
	Options []string
}

// SFTP is the repository at path on a server, reached as user at host by ssh
// with the known hosts and key given: sftp-server runs as root there, since
// the server's backups are root's.
func SFTP(user, host, path, knownHosts, key string) Source {
	ssh := []string{"ssh", "-F", "/dev/null", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10",
		"-o", "UserKnownHostsFile=" + quote(knownHosts), "-o", "StrictHostKeyChecking=accept-new"}
	if key != "" {
		ssh = append(ssh, "-i", quote(key))
	}
	ssh = append(ssh, user+"@"+host, "sudo", "/usr/lib/openssh/sftp-server")
	return Source{
		Repo:    "sftp:" + user + "@" + host + ":" + path,
		Options: []string{"-o", "sftp.command=" + strings.Join(ssh, " ")},
	}
}

func quote(s string) string {
	if strings.ContainsAny(s, " \t'\"") {
		return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
	}
	return s
}

// StateFile is where a project keeps what the last pull found.
const StateFile = ".damstack/backup.json"

// State is what the last pull found on this machine.
type State struct {
	Pulled    time.Time `json:"pulled"`
	Latest    time.Time `json:"latest"`
	Snapshots int       `json:"snapshots"`
	// Kit is when the last recovery kit of the project was made.
	Kit time.Time `json:"kit"`
}

// Saw notes the snapshots on this machine.
func (s *State) Saw(snaps []Snapshot) {
	s.Snapshots, s.Latest = len(snaps), time.Time{}
	if len(snaps) > 0 {
		s.Latest = snaps[len(snaps)-1].Time
	}
}

// Stale is the age past which the latest snapshot here is old: the server
// backs up every night.
const Stale = 48 * time.Hour

// LoadState reads the state of the project in dir; a project never pulled
// has the zero State.
func LoadState(dir string) (State, error) {
	var s State
	data, err := os.ReadFile(filepath.Join(dir, StateFile))
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(data, &s)
}

// SaveState writes the state of the project in dir.
func SaveState(dir string, s State) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, StateFile), append(data, '\n'), 0o644)
}
