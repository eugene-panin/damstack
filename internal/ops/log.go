package ops

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/eugene-panin/damstack/internal/project"
	"github.com/eugene-panin/damstack/internal/release"
	"github.com/eugene-panin/damstack/internal/toolbox"
)

// LogTo sends the output of the tools of a job to a log of the project, and
// returns its path and how to send it back.
func LogTo(p *project.Project, j Job) (string, func(), error) {
	runner, ok := j.Engine.Runner.(*toolbox.Runner)
	if !ok {
		return "", func() {}, nil
	}
	work, err := p.Work()
	if err != nil {
		return "", nil, err
	}
	dir := filepath.Join(work, "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, err
	}
	path := filepath.Join(dir, time.Now().Format("2006-01-02T15-04-05")+"-"+strings.ReplaceAll(j.Name, "/", "-")+".log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", nil, err
	}
	secrets, err := p.Secrets(j.Engine.Password)
	if err != nil {
		f.Close()
		return "", nil, err
	}
	out := newMasker(f, secrets)
	stdout, stderr, stdin, tty := runner.Stdout, runner.Stderr, runner.Stdin, runner.TTY
	runner.Stdout, runner.Stderr, runner.Stdin, runner.TTY = out, out, nil, false
	j.Engine.Brief = true
	return path, func() {
		runner.Stdout, runner.Stderr, runner.Stdin, runner.TTY = stdout, stderr, stdin, tty
		j.Engine.Brief = false
		out.Flush()
		f.Close()
	}, nil
}

// LastLines are the last n lines of a log; none when it cannot be read.
func LastLines(path string, n int) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	all := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(all) > n {
		all = all[len(all)-n:]
	}
	return all
}

// Record records a command that ran.
func Record(p *project.Project, command, step string, start time.Time, err error) error {
	return RecordRun(p, command, step, "", start, err)
}

// RecordRun records a step that ran, and the fingerprint of its inputs.
func RecordRun(p *project.Project, command, step, inputs string, start time.Time, err error) error {
	e := project.Entry{
		Time: start, Command: command, Step: step, Result: project.OK, Inputs: inputs,
		Seconds: time.Since(start).Round(100 * time.Millisecond).Seconds(),
		Stack:   p.Meta.Stack.Tag, Commit: p.Meta.Stack.Commit, Damstack: release.Version,
	}
	if err != nil {
		e.Result, e.Error = project.Failed, err.Error()
	}
	return p.Record(e)
}

// Ago says how long ago t was, roughly.
func Ago(t time.Time) string {
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

// FirstLine is the first line of text.
func FirstLine(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	return line
}
